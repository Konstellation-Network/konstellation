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

// The block list binds at four levels (ENGINEERING.md §10, D6: "the chain
// itself refuses any tx that touches a listed address — EVM and Cosmos,
// native KASH included"):
//
//  1. the ante handler and mempool pre-check (ante/, app/blocked_recipient.go):
//     signers, EIP-7702 authorities, fee payers, the direct recipients a tx
//     names, and the parties of an ERC-20 precompile transfer — an
//     immediate, explained refusal, nothing charged;
//  2. the bank send restriction below: every SendCoins / InputOutputCoins
//     the chain makes, whoever asked — a Cosmos message, a precompile
//     (werc20 transferFrom on a pre-freeze allowance, ICS-20 escrow, erc20
//     conversion), authz, feegrant — so a frozen address can neither be
//     debited nor credited by anything the ante could not see. The EVM
//     surfaces it as a normal revert carrying the reason. StakingBankKeeper
//     extends it to x/staking's DelegateCoins, which x/bank routes around
//     the restriction;
//  3. the x/vm balance guard (EVMBankKeeper): the one bank write x/vm makes
//     outside SendCoins — UncheckedSetBalance when the stateDB commits — so
//     an internal CALL with value cannot fund a frozen address, and a frozen
//     contract cannot pay out when someone else calls it;
//  4. the tx-path precompiles (app/compliance_precompiles.go): a frozen
//     caller — a frozen *contract*, which anyone can make run — cannot
//     delegate, redirect or withdraw rewards, vote, unjail or send over
//     IBC, and no tx method may name a frozen address.
//
// Levels 2–4 were missing until 2026-09-22 (STATUS.md §5a P20): an
// allowance granted before the freeze drained a frozen account through the
// werc20 precompile, any contract could fund one, and a frozen contract
// could delegate and route its yield to a clean address.
//
// What is deliberately let through — a *recipient* exemption only, never a
// sender's: nothing anywhere debits a frozen account. Each case is tested.
//
//	ABCI phase       Everything BeginBlock and EndBlock do runs under
//	                 types.WithProtocolFlow (app.BeginBlocker/EndBlocker):
//	                 a frozen address may be credited there. This is not a
//	                 list of hooks but the phase itself, because any refusal
//	                 in that phase is a chain halt with no tx left to lift
//	                 the freeze or flip Enforce (found by the PR #15 review:
//	                 slashing a validator a frozen delegator had redelegated
//	                 from — evidence or downtime — reaches distribution's
//	                 reward withdrawal to the frozen address through
//	                 SlashRedelegation → Unbond → BeforeDelegationSharesModified,
//	                 and the downtime variant is forceable by a frozen
//	                 contract redelegating every block). Covers, among
//	                 others: gov deposit refunds, slashing-driven reward
//	                 withdrawals, validator-removal commission, gov-executed
//	                 messages such as a community-pool spend (governance can
//	                 unfreeze anyway), unbonding completion (which bypasses
//	                 the restriction regardless). The funds land on an
//	                 account that still cannot spend them.
//	tx path          Two marks for protocol completions inside a tx:
//	                 the transfer module's own refund on an error
//	                 acknowledgement or timeout (ibc.RefundMarker; a packet
//	                 escrowed before the freeze must not fail every relayer
//	                 retry), and x/distribution's AfterValidatorRemoved
//	                 commission payout when another delegator's undelegation
//	                 empties the validator (MarkValidatorRemoval; x/staking
//	                 only logs the hook's error, leaving records half
//	                 deleted). Plus sends from the x/gov module account
//	                 (MsgCancelProposal's refunds): deposits returning to
//	                 whoever made them.
//
// Refused on purpose, because a party asked: reward withdrawals and the
// automatic withdrawal a delegator's own staking action triggers when the
// withdraw address is frozen (set a new one first), erc20 re-conversion of
// an IBC refund for a frozen sender (x/erc20 records the failure and leaves
// the coin, still on the frozen account).
//
// Known limit for contract authors: a contract that pays several parties
// in one transaction fails as a whole if any payee is frozen — as a revert
// when it pays through the werc20 precompile, and at stateDB commit (not
// catchable by try/catch, invisible to eth_*) when it pays with a native
// CALL. Check isFrozen() on the compliance precompile before paying.

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

// StakingBankKeeper is the bank keeper x/staking is given. x/bank's
// DelegateCoins moves a delegator's stake into the pool with direct balance
// writes, never through the send restriction, so without this a frozen
// contract could still delegate its KASH (and route the yield elsewhere —
// PR #15 review). Undelegation is left alone: an unbonding completing into a
// frozen address is a protocol completion (the stake was already the
// delegator's and stays immobile).
type StakingBankKeeper struct {
	bankkeeper.Keeper
	compliance Keeper
}

// NewStakingBankKeeper wraps bk for x/staking.
func NewStakingBankKeeper(bk bankkeeper.Keeper, k Keeper) StakingBankKeeper {
	return StakingBankKeeper{Keeper: bk, compliance: k}
}

// DelegateCoinsFromAccountToModule refuses a frozen delegator.
func (b StakingBankKeeper) DelegateCoinsFromAccountToModule(ctx context.Context, senderAddr sdk.AccAddress, recipientModule string, amt sdk.Coins) error {
	if b.compliance.Enforce(ctx) && b.compliance.IsFrozen(ctx, senderAddr) {
		return errorsmod.Wrapf(types.ErrAddressFrozen, "delegator %s", types.Bech32(senderAddr))
	}
	return b.Keeper.DelegateCoinsFromAccountToModule(ctx, senderAddr, recipientModule, amt)
}

// MarkValidatorRemoval wraps staking hooks so AfterValidatorRemoved runs as
// a protocol flow: x/distribution pays the removed validator's outstanding
// commission to its withdraw address there, and the hook is reached from
// any delegator's undelegation that empties the validator (a tx; the
// EndBlock path is covered by the phase-wide mark). Refusing that send
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
