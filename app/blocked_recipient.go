package app

import (
	"github.com/ethereum/go-ethereum/common"

	erc20precompile "github.com/cosmos/evm/precompiles/erc20"
	evmtypes "github.com/cosmos/evm/x/vm/types"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"

	compliancetypes "github.com/Konstellation-Network/konstellation/x/compliance/types"
)

// checkBlockedRecipient rejects an EVM tx that sends value straight to an
// address x/vm will refuse at commit: a module account, or anything on the
// bank's blocked list (module accounts and precompiles — config.BlockedAddresses).
//
// x/vm's guard (SetBalanceWithLocked, ENGINEERING.md §4.1.1) fires when the
// stateDB is committed, i.e. after the tx is already in a block. That
// rejection is an SDK-level failure (code 4), and cosmos/evm does not index
// those as Ethereum txs: the sender is charged gas, eth_getTransactionReceipt
// answers "not found", and the reason is only in CometBFT's tx_search. A
// simulation never commits, so eth_estimateGas does not warn either. Doing
// the same check here, in the ante handler and the mempool pre-check, turns
// that into a synchronous, explained refusal from eth_sendRawTransaction with
// nothing charged — the guard itself is untouched and still the last line.
//
// What this cannot see: value sent to a blocked address by an internal call
// (contract → module account). That still fails at commit, invisibly to
// eth_*; a contract that forwards user-supplied addresses should check them.
func (app *KonstellationApp) checkBlockedRecipient(ctx sdk.Context, tx sdk.Tx) error {
	for _, msg := range tx.GetMsgs() {
		ethMsg, ok := msg.(*evmtypes.MsgEthereumTx)
		if !ok {
			continue
		}
		ethTx := ethMsg.AsTransaction()
		if ethTx == nil || ethTx.To() == nil || ethTx.Value().Sign() == 0 {
			continue
		}
		to := sdk.AccAddress(ethTx.To().Bytes())
		if acct, isModule := app.AccountKeeper.GetAccount(ctx, to).(sdk.ModuleAccountI); isModule {
			return errorsmod.Wrapf(sdkerrors.ErrUnauthorized, "%s (%s) is not allowed to receive funds: it is the %q module account", to, ethTx.To().Hex(), acct.GetName())
		}
		if app.BankKeeper.BlockedAddr(to) {
			return errorsmod.Wrapf(sdkerrors.ErrUnauthorized, "%s (%s) is not allowed to receive funds: blocked address (module account or precompile)", to, ethTx.To().Hex())
		}
	}
	return nil
}

// checkFrozenERC20Transfer rejects an EVM tx that calls transfer or
// transferFrom on an x/erc20-backed precompile — the WKASH werc20 at
// config.WKASHPrecompile, or the ERC-20 face of an IBC voucher — naming a
// frozen address as the sender or the recipient.
//
// Those calls move bank balance, so the compliance send restriction
// (x/compliance/keeper/restriction.go) refuses them inside the precompile
// and the EVM reverts with the reason: a mined, failed tx that consumed
// gas, the way any revert does. The ante only sees the tx's signer and its
// `to` (the precompile), so without this check the spender of a pre-freeze
// allowance — the P20 attack — gets that revert rather than an answer. Here
// it is refused at eth_sendRawTransaction and in the block alike, with
// nothing charged, like a direct transfer to a frozen address. A contract
// making the same call internally still gets the revert.
func (app *KonstellationApp) checkFrozenERC20Transfer(ctx sdk.Context, tx sdk.Tx) error {
	if !app.ComplianceKeeper.Enforce(ctx) {
		return nil
	}
	for _, msg := range tx.GetMsgs() {
		ethMsg, ok := msg.(*evmtypes.MsgEthereumTx)
		if !ok {
			continue
		}
		ethTx := ethMsg.AsTransaction()
		if ethTx == nil || ethTx.To() == nil || len(ethTx.Data()) < 4 {
			continue
		}
		to := *ethTx.To()
		if !app.Erc20Keeper.IsNativePrecompileAvailable(ctx, to) && !app.Erc20Keeper.IsDynamicPrecompileAvailable(ctx, to) {
			continue
		}
		for _, party := range erc20TransferParties(ethTx.Data()) {
			if app.ComplianceKeeper.IsFrozen(ctx, party.Bytes()) {
				return errorsmod.Wrapf(compliancetypes.ErrAddressFrozen, "%s (%s), named in the ERC-20 transfer to %s", sdk.AccAddress(party.Bytes()), party.Hex(), to.Hex())
			}
		}
	}
	return nil
}

// erc20TransferParties decodes the addresses an ERC-20 transfer(to, amount)
// or transferFrom(from, to, amount) call names; nil for any other input.
func erc20TransferParties(data []byte) []common.Address {
	method, err := erc20precompile.ABI.MethodById(data[:4])
	if err != nil {
		return nil
	}
	var n int
	switch method.Name {
	case erc20precompile.TransferMethod:
		n = 1
	case erc20precompile.TransferFromMethod:
		n = 2
	default:
		return nil
	}
	args, err := method.Inputs.Unpack(data[4:])
	if err != nil || len(args) < n {
		return nil
	}
	parties := make([]common.Address, 0, n)
	for _, a := range args[:n] {
		addr, ok := a.(common.Address)
		if !ok {
			return nil
		}
		parties = append(parties, addr)
	}
	return parties
}

// checkEVMRecipients is checkBlockedRecipient then checkFrozenERC20Transfer.
func (app *KonstellationApp) checkEVMRecipients(ctx sdk.Context, tx sdk.Tx) error {
	if err := app.checkBlockedRecipient(ctx, tx); err != nil {
		return err
	}
	return app.checkFrozenERC20Transfer(ctx, tx)
}

// withBlockedRecipientCheck runs inner and then checkEVMRecipients on the
// resulting context.
func (app *KonstellationApp) withBlockedRecipientCheck(inner sdk.AnteHandler) sdk.AnteHandler {
	return func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		newCtx, err := inner(ctx, tx, simulate)
		if err != nil {
			return newCtx, err
		}
		return newCtx, app.checkEVMRecipients(newCtx, tx)
	}
}

// anteHandlerDecorator lets a complete sdk.AnteHandler terminate a
// sdk.ChainAnteDecorators chain.
type anteHandlerDecorator struct{ h sdk.AnteHandler }

func (d anteHandlerDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, _ sdk.AnteHandler) (sdk.Context, error) {
	return d.h(ctx, tx, simulate)
}
