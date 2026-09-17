// Package ante is the chain-wide enforcement of the block list
// (ENGINEERING.md §10 "ante decorator" level, chosen by D6).
//
// A transaction is rejected if any address it involves is frozen. "Involves"
// means, for every message, including messages nested in authz MsgExec:
//
//   - every signer (Cosmos signers and the verified EVM sender);
//   - the fee payer and fee granter;
//   - direct transfer recipients the chain can see at ante time: bank
//     MsgSend / MsgMultiSend outputs, vesting-account creation targets,
//     erc20 convert receivers, distribution withdraw-address changes, and the
//     `to` of an EVM transaction.
//
// What it cannot see: value moved inside EVM execution (an ERC-20 transfer
// to a frozen address made by a contract call, an internal call). That is
// what the compliance precompile is for — contracts that must not serve a
// frozen address call isFrozen() themselves. Freezing an EOA therefore
// guarantees it can never sign again and can never be the direct recipient
// of a native transfer; it does not guarantee no token ever reaches it.
//
// The check runs after cosmos/evm's ante handler so EVM senders are the
// signature-verified ones, and it runs in CheckTx, the mempool recheck and
// DeliverTx alike, so a frozen address's transactions are dropped at the
// mempool and, if a proposer includes one anyway, fail in the block.
package ante

import (
	"context"
	"fmt"

	"github.com/ethereum/go-ethereum/common"

	erc20types "github.com/cosmos/evm/x/erc20/types"
	evmtypes "github.com/cosmos/evm/x/vm/types"

	errorsmod "cosmossdk.io/errors"

	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/auth/vesting/types"
	"github.com/cosmos/cosmos-sdk/x/authz"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	distrtypes "github.com/cosmos/cosmos-sdk/x/distribution/types"

	comptypes "github.com/Konstellation-Network/konstellation/x/compliance/types"
)

// FreezeChecker is the slice of the compliance keeper the check needs.
type FreezeChecker interface {
	IsFrozen(ctx context.Context, addr []byte) bool
	Enforce(ctx context.Context) bool
}

// maxAuthzDepth bounds MsgExec nesting so a hostile tx can't make the
// extractor recurse without limit. authz itself rejects deeper nesting.
const maxAuthzDepth = 4

// Wrap returns an ante handler that runs inner first and then the freeze
// check on the resulting context.
func Wrap(inner sdk.AnteHandler, cdc codec.Codec, k FreezeChecker) sdk.AnteHandler {
	return func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		newCtx, err := inner(ctx, tx, simulate)
		if err != nil {
			return newCtx, err
		}
		if err := Check(newCtx, cdc, k, tx); err != nil {
			return newCtx, err
		}
		return newCtx, nil
	}
}

// Check rejects tx if any involved address is frozen. Enforce=false makes it
// a no-op (governance kill switch).
func Check(ctx sdk.Context, cdc codec.Codec, k FreezeChecker, tx sdk.Tx) error {
	if !k.Enforce(ctx) {
		return nil
	}
	addrs, err := InvolvedAddresses(cdc, tx)
	if err != nil {
		return err
	}
	for _, a := range addrs {
		if k.IsFrozen(ctx, a) {
			return errorsmod.Wrapf(comptypes.ErrAddressFrozen, "%s", comptypes.Bech32(a))
		}
	}
	return nil
}

// InvolvedAddresses returns every 20-byte address the tx involves, as
// defined in the package doc. Duplicates are not removed; callers only test
// membership.
//
// Safe on an unvalidated tx: the SDK tx wrapper's FeePayer/FeeGranter panic
// on malformed AuthInfo, so any panic is turned into an error rather than
// taking down the CheckTx/InsertTx/RPC path that called us. Do not guard
// this with the tx's own ValidateBasic: the SDK's requires Cosmos
// signatures, which an EVM tx never has (its signature lives inside
// MsgEthereumTx), so that would reject every EVM tx on the chain.
func InvolvedAddresses(cdc codec.Codec, tx sdk.Tx) (out [][]byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			out, err = nil, fmt.Errorf("extracting addresses: %v", r)
		}
	}()
	if ft, ok := tx.(sdk.FeeTx); ok {
		if p := ft.FeePayer(); len(p) > 0 {
			out = append(out, p)
		}
		if g := ft.FeeGranter(); len(g) > 0 {
			out = append(out, g)
		}
	}
	for _, msg := range tx.GetMsgs() {
		more, err := msgAddresses(cdc, msg, 0)
		if err != nil {
			return nil, err
		}
		out = append(out, more...)
	}
	return out, nil
}

func msgAddresses(cdc codec.Codec, msg sdk.Msg, depth int) ([][]byte, error) {
	var out [][]byte

	switch m := msg.(type) {
	case *evmtypes.MsgEthereumTx:
		// Sender is From, which the EVM ante has verified against the
		// signature by the time Check runs. AsTransaction is nil for a
		// message with no raw tx; ValidateBasic rejects those, but the
		// mempool pre-check runs before ValidateBasic.
		out = append(out, m.GetSender().Bytes())
		if ethTx := m.AsTransaction(); ethTx != nil {
			if to := ethTx.To(); to != nil {
				out = append(out, to.Bytes())
			}
		}
		return out, nil

	case *authz.MsgExec:
		if depth >= maxAuthzDepth {
			return nil, fmt.Errorf("authz MsgExec nested deeper than %d", maxAuthzDepth)
		}
		inner, err := m.GetMessages()
		if err != nil {
			return nil, err
		}
		for _, im := range inner {
			more, err := msgAddresses(cdc, im, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, more...)
		}
		// fall through to add the grantee (signer) below
	}

	signers, _, err := cdc.GetMsgV1Signers(msg)
	if err != nil {
		return nil, fmt.Errorf("signers of %T: %w", msg, err)
	}
	out = append(out, signers...)

	// Direct recipients the chain can see.
	switch m := msg.(type) {
	case *banktypes.MsgSend:
		out = appendBech32(out, m.ToAddress)
	case *banktypes.MsgMultiSend:
		for _, o := range m.Outputs {
			out = appendBech32(out, o.Address)
		}
	case *types.MsgCreateVestingAccount:
		out = appendBech32(out, m.ToAddress)
	case *types.MsgCreatePermanentLockedAccount:
		out = appendBech32(out, m.ToAddress)
	case *types.MsgCreatePeriodicVestingAccount:
		out = appendBech32(out, m.ToAddress)
	case *distrtypes.MsgSetWithdrawAddress:
		out = appendBech32(out, m.WithdrawAddress)
	case *erc20types.MsgConvertERC20:
		out = appendBech32(out, m.Receiver)
	case *erc20types.MsgConvertCoin:
		if common.IsHexAddress(m.Receiver) {
			out = append(out, common.HexToAddress(m.Receiver).Bytes())
		}
	}
	return out, nil
}

// appendBech32 adds a bech32 address if it parses; malformed recipients are
// left for the message's own ValidateBasic to reject.
func appendBech32(out [][]byte, s string) [][]byte {
	if a, err := sdk.AccAddressFromBech32(s); err == nil {
		return append(out, a.Bytes())
	}
	return out
}
