package keeper

import (
	"bytes"
	"context"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	bankkeeper "github.com/cosmos/cosmos-sdk/x/bank/keeper"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/Konstellation-Network/konstellation/x/compliance/types"
)

// The block list binds at three levels (ENGINEERING.md §10, D6: "the chain
// itself refuses any tx that touches a listed address — EVM and Cosmos,
// native KASH included"):
//
//  1. the ante handler and mempool pre-check (ante/): signers, EIP-7702
//     authorities, fee payers and the direct recipients a tx names — an
//     immediate, explained refusal, nothing charged;
//  2. the bank send restriction below: every SendCoins / InputOutputCoins
//     the chain makes, whoever asked — a Cosmos message, a precompile
//     (werc20 transferFrom on a pre-freeze allowance, ICS-20 escrow, erc20
//     conversion), authz, feegrant — so a frozen address can neither be
//     debited nor credited by anything the ante could not see. The EVM
//     surfaces it as a normal revert carrying the reason;
//  3. the x/vm balance guard (EVMBankKeeper): the one bank write x/vm makes
//     outside SendCoins — UncheckedSetBalance when the stateDB commits — so
//     an internal CALL with value cannot fund a frozen address, and a frozen
//     contract cannot pay out when someone else calls it.
//
// Level 2 was missing until 2026-09-22 (STATUS.md §5a P20): an allowance
// granted before the freeze drained a frozen account through the werc20
// precompile, and any contract could fund one.
//
// What is deliberately let through — every exemption is a protocol
// completion, never a party's request, and each is tested:
//
//	frozen sender    nothing. A frozen address never signs (level 1), and no
//	                 module debits a user account on its own initiative.
//	                 x/staking's DelegateCoins/UndelegateCoins do not go
//	                 through the restriction at all, but they only move a
//	                 delegator's own stake between it and the pools.
//	frozen recipient (a) sends from the x/gov module account: deposits
//	                 returning to whoever made them when a proposal ends or
//	                 is cancelled. x/gov's EndBlock returns that error and
//	                 the chain would halt on the first proposal a frozen
//	                 depositor had funded (x/gov/abci.go).
//	                 (b) flows marked types.WithProtocolFlow: the transfer
//	                 module's own refund on an error acknowledgement or
//	                 timeout (ibc.RefundMarker; a packet escrowed before the
//	                 freeze must not be stuck for the relayer to retry
//	                 forever), and x/distribution's AfterValidatorRemoved
//	                 commission payout (MarkValidatorRemoval; the hook runs
//	                 from staking EndBlock and from other delegators'
//	                 undelegations, and stopping half-way leaves orphaned
//	                 distribution records).
//	                 (c) unbonding completion (x/staking EndBlock →
//	                 UndelegateCoins), which bypasses the restriction by
//	                 construction, as above.
//	                 In every case the funds land on an account that still
//	                 cannot spend them.
//
// Refused on purpose, because a party asked: reward withdrawals and the
// automatic withdrawal a delegator's own staking action triggers when the
// withdraw address is frozen (set a new one first), community-pool spends
// to a frozen address (the proposal fails; governance can unfreeze), erc20
// re-conversion of an IBC refund for a frozen sender (x/erc20 records the
// failure and leaves the coin, still on the frozen account).

// SendRestriction is the bank.SendRestrictionFn. It never changes the
// recipient.
func (k Keeper) SendRestriction(ctx context.Context, from, to sdk.AccAddress, _ sdk.Coins) (sdk.AccAddress, error) {
	if !k.Enforce(ctx) {
		return to, nil
	}
	if k.IsFrozen(ctx, from) {
		return to, errorsmod.Wrapf(types.ErrAddressFrozen, "sender %s", types.Bech32(from))
	}
	if k.IsFrozen(ctx, to) && !types.IsProtocolFlow(ctx) && !bytes.Equal(from, k.govAddr) {
		return to, errorsmod.Wrapf(types.ErrAddressFrozen, "recipient %s", types.Bech32(to))
	}
	return to, nil
}

// EVMBankKeeper is the bank keeper x/vm is given. It is the real keeper
// except for UncheckedSetBalance, which x/vm calls when the stateDB commits
// an account whose balance the EVM changed (SetBalanceWithLocked →
// bankWrapper.SetBalance; the only bank write in x/vm, ENGINEERING.md
// §4.1.1). That path never calls SendCoins, so the send restriction cannot
// see an internal CALL with value; this guard refuses to change a frozen
// address's balance there. A commit that merely touches a frozen account
// (a 0-value call, a BALANCE read) rewrites the same number and passes,
// otherwise touching a frozen address would be a way to grief any contract.
//
// The refusal fails the SDK tx after the EVM ran, which cosmos/evm does not
// index as an Ethereum tx (STATUS.md §3, "invisible to eth_*"): gas is
// charged and the reason is only in tx_search. That is the same class as
// the module-account guard and is accepted for internal calls; the direct
// cases — an EVM tx *to* a frozen address, or a werc20/erc20
// transfer/transferFrom naming one — are refused at submission by the ante
// and the mempool pre-check instead.
type EVMBankKeeper struct {
	bankkeeper.Keeper
	compliance Keeper
}

// NewEVMBankKeeper wraps bk for x/vm.
func NewEVMBankKeeper(bk bankkeeper.Keeper, k Keeper) EVMBankKeeper {
	return EVMBankKeeper{Keeper: bk, compliance: k}
}

// UncheckedSetBalance refuses a balance change on a frozen address.
func (b EVMBankKeeper) UncheckedSetBalance(ctx context.Context, addr sdk.AccAddress, balance sdk.Coin) error {
	if b.compliance.Enforce(ctx) && b.compliance.IsFrozen(ctx, addr) {
		if cur := b.GetBalance(ctx, addr, balance.Denom); !cur.Amount.Equal(balance.Amount) {
			return errorsmod.Wrapf(types.ErrAddressFrozen, "%s: balance change refused", types.Bech32(addr))
		}
	}
	return b.Keeper.UncheckedSetBalance(ctx, addr, balance)
}

// MarkValidatorRemoval wraps staking hooks so AfterValidatorRemoved runs as
// a protocol flow: x/distribution pays the removed validator's outstanding
// commission to its withdraw address there, and the hook is reached from
// staking's EndBlock (a matured unbonding validator) as well as from any
// delegator's undelegation that empties the validator. Refusing that send
// because the operator is frozen would not stop the operator from moving
// anything, but x/staking only logs the hook's error, so the commission
// would stay in the distribution module with its accounting records
// half-deleted. Every other hook is untouched: the reward withdrawal a
// delegator's own delegate/undelegate/redelegate triggers is that
// delegator's request.
func MarkValidatorRemoval(h stakingtypes.StakingHooks) stakingtypes.StakingHooks {
	return validatorRemovalHooks{StakingHooks: h}
}

type validatorRemovalHooks struct{ stakingtypes.StakingHooks }

func (h validatorRemovalHooks) AfterValidatorRemoved(ctx context.Context, consAddr sdk.ConsAddress, valAddr sdk.ValAddress) error {
	return h.StakingHooks.AfterValidatorRemoved(types.WithProtocolFlow(sdk.UnwrapSDKContext(ctx)), consAddr, valAddr)
}
