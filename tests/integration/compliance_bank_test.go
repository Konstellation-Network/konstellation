//go:build test

package integration

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	"github.com/cosmos/evm/precompiles/werc20"
	testkeyring "github.com/cosmos/evm/testutil/keyring"
	evmtypes "github.com/cosmos/evm/x/vm/types"

	sdkmath "cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	distrtypes "github.com/cosmos/cosmos-sdk/x/distribution/types"
	govv1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/Konstellation-Network/konstellation/app/config"
	"github.com/Konstellation-Network/konstellation/tests/integration/testdata"
	compliancetypes "github.com/Konstellation-Network/konstellation/x/compliance/types"
)

// STATUS.md §5a P20 (adversarial docs review, 2026-09-21; reproduced on the
// dev chain the same day and again on 2026-09-22 before this fix): the
// block list bound only at the ante handler — signers and the tx's `to` —
// while precompiles move bank balance through keepers. An allowance granted
// on the werc20 precompile before a freeze drained the frozen account, and
// any contract call funded one. These tests are that reproduction, now
// failing the way D6 promises (ENGINEERING.md §10/§11: "the chain itself
// refuses any tx that touches a listed address — EVM and Cosmos, native
// KASH included"), plus the protocol flows the fix deliberately lets
// through (x/compliance/keeper/restriction.go).

var wkash = common.HexToAddress(config.WKASHPrecompile)

// wkashC is the werc20 precompile as a callable contract.
var wkashC = evmtypes.CompiledContract{ABI: werc20.ABI}

func (h *harness) fundContract(from testkeyring.Key, to common.Address, amount *big.Int) {
	h.t.Helper()
	res, err := h.sendEVM(from, evmtypes.EvmTxArgs{To: &to, Amount: amount, GasLimit: 60_000})
	require.NoError(h.t, err, res.Log)
}

// TestFrozenAllowanceCannotBeSpent is the P20 attack: approve a spender on
// WKASH, get frozen, spender calls transferFrom.
func TestFrozenAllowanceCannotBeSpent(t *testing.T) {
	h := newHarness(t)
	victim, spender, funder := h.key(1), h.key(2), h.key(3)
	forwarderC := mustLoad(t, testdata.LoadForwarder)
	forwarder := h.deploy(spender, forwarderC)
	h.fundContract(funder, forwarder, oneKASH)

	// Before the freeze: allowances to an EOA and to a contract, and proof
	// the EOA's allowance is live.
	two := new(big.Int).Mul(oneKASH, big.NewInt(2))
	res, err := h.call(victim, wkash, wkashC, "approve", spender.Addr, two)
	require.NoError(t, err, res.Log)
	res, err = h.call(victim, wkash, wkashC, "approve", forwarder, two)
	require.NoError(t, err, res.Log)
	victimBefore := h.balance(victim.Addr)
	res, err = h.call(spender, wkash, wkashC, "transferFrom", victim.Addr, spender.Addr, oneKASH)
	require.NoError(t, err, res.Log)
	require.Equal(t, 0, new(big.Int).Sub(victimBefore, oneKASH).Cmp(h.balance(victim.Addr)), "pre-freeze transferFrom did not move funds: the allowance setup is wrong")

	h.emergencyFreeze(victim.Addr)
	victimBefore, spenderBefore := h.balance(victim.Addr), h.balance(spender.Addr)

	// Direct: the spender's tx names the frozen address in calldata, not as
	// signer or `to`. Refused at admission with the reason, and at delivery
	// with nothing charged.
	transferFrom := evmtypes.EvmTxArgs{To: &wkash, GasLimit: gasLimit, Input: pack(t, wkashC, "transferFrom", victim.Addr, spender.Addr, oneKASH)}
	chk := h.checkTxEVM(spender, transferFrom)
	require.NotZero(t, chk.Code)
	require.Contains(t, chk.Log, compliancetypes.ErrAddressFrozen.Error())
	res, err = h.sendEVM(spender, transferFrom)
	require.Error(t, err)
	require.NotZero(t, res.Code, "ante rejection expected, got an executed tx: %s", res.Log)
	require.Contains(t, res.Log, compliancetypes.ErrAddressFrozen.Error())
	require.Equal(t, 0, victimBefore.Cmp(h.balance(victim.Addr)), "frozen allowance was spent")
	require.Equal(t, 0, spenderBefore.Cmp(h.balance(spender.Addr)), "spender charged for an ante rejection")

	// Direct: funding the frozen address through the precompile.
	funderBefore := h.balance(funder.Addr)
	transfer := evmtypes.EvmTxArgs{To: &wkash, GasLimit: gasLimit, Input: pack(t, wkashC, "transfer", victim.Addr, oneKASH)}
	chk = h.checkTxEVM(funder, transfer)
	require.NotZero(t, chk.Code)
	require.Contains(t, chk.Log, compliancetypes.ErrAddressFrozen.Error())
	res, err = h.sendEVM(funder, transfer)
	require.Error(t, err)
	require.NotZero(t, res.Code, res.Log)
	require.Equal(t, 0, victimBefore.Cmp(h.balance(victim.Addr)), "frozen address was funded")
	require.Equal(t, 0, funderBefore.Cmp(h.balance(funder.Addr)))

	// Internal: the same two calls made by a contract. The ante sees only
	// the contract; the bank send restriction refuses inside the precompile
	// and the EVM reverts with the reason — a mined, visible failure.
	res, err = h.call(spender, forwarder, forwarderC, "call", wkash, pack(t, wkashC, "transferFrom", victim.Addr, spender.Addr, oneKASH))
	require.Error(t, err)
	require.Zero(t, res.Code, "a precompile refusal must be an EVM revert, not an SDK failure: %s", res.Log)
	require.Contains(t, err.Error(), compliancetypes.ErrAddressFrozen.Error())
	res, err = h.call(spender, forwarder, forwarderC, "call", wkash, pack(t, wkashC, "transfer", victim.Addr, oneKASH))
	require.Error(t, err)
	require.Zero(t, res.Code, res.Log)
	require.Contains(t, err.Error(), compliancetypes.ErrAddressFrozen.Error())
	require.Equal(t, 0, victimBefore.Cmp(h.balance(victim.Addr)), "frozen balance moved through an internal call")
	require.Equal(t, 0, oneKASH.Cmp(h.balance(forwarder)))

	// Cosmos: a clean signer cannot fund the frozen address either (ante),
	// and the bank refuses even if the ante is bypassed — checked directly
	// against the keeper, the way a module or precompile calls it.
	one := sdk.NewCoins(sdk.NewCoin(config.BaseDenom, sdkmath.NewIntFromBigInt(oneKASH)))
	err = h.app.BankKeeper.SendCoins(h.ctx(), funder.AccAddr, victim.AccAddr, one)
	require.ErrorIs(t, err, compliancetypes.ErrAddressFrozen)
	err = h.app.BankKeeper.SendCoins(h.ctx(), victim.AccAddr, funder.AccAddr, one)
	require.ErrorIs(t, err, compliancetypes.ErrAddressFrozen)

	// The plain EVM transfer from the frozen address is still refused at
	// the ante, as before.
	res, err = h.transfer(victim, spender.Addr, oneKASH)
	require.Error(t, err)
	require.Contains(t, res.Log, compliancetypes.ErrAddressFrozen.Error())

	// Lifting the freeze restores the allowance: the list changed, not the
	// approval.
	h.liftEmergencyFreeze(victim.Addr)
	res, err = h.call(spender, wkash, wkashC, "transferFrom", victim.Addr, spender.Addr, oneKASH)
	require.NoError(t, err, res.Log)
	require.Equal(t, 0, new(big.Int).Sub(victimBefore, oneKASH).Cmp(h.balance(victim.Addr)))
}

// TestFrozenBalanceCannotChangeInsideEVM covers x/vm's own bank write — the
// stateDB commit — which no SendCoins sees: an internal CALL with value to
// a frozen address, and a frozen contract paying out when someone else
// calls it. Both fail at commit, an SDK-level failure (STATUS.md §3: not
// indexed as an Ethereum tx), which is accepted for internal calls; the
// direct forms are refused at the ante.
func TestFrozenBalanceCannotChangeInsideEVM(t *testing.T) {
	h := newHarness(t)
	frozen, operator := h.key(1), h.key(2)
	forwarderC := mustLoad(t, testdata.LoadForwarder)
	payer := h.deploy(operator, forwarderC)
	relay := h.deploy(operator, forwarderC)
	five := new(big.Int).Mul(oneKASH, big.NewInt(5))
	h.fundContract(operator, payer, five)
	h.emergencyFreeze(frozen.Addr)
	frozenBefore := h.balance(frozen.Addr)

	// Internal CALL with value to a frozen EOA.
	res, err := h.call(operator, payer, forwarderC, "send", frozen.Addr, oneKASH)
	require.Error(t, err)
	require.NotZero(t, res.Code, "commit guard expected, got: %s", res.Log)
	require.Contains(t, res.Log, compliancetypes.ErrAddressFrozen.Error())
	require.Equal(t, 0, frozenBefore.Cmp(h.balance(frozen.Addr)), "internal call funded a frozen address")
	require.Equal(t, 0, five.Cmp(h.balance(payer)))

	// Touching is not changing: a 0-value call to the frozen address (its
	// balance is rewritten unchanged at commit) must not fail, otherwise
	// any contract could be griefed by having it touch a frozen address.
	res, err = h.call(operator, payer, forwarderC, "send", frozen.Addr, big.NewInt(0))
	require.NoError(t, err, res.Log)

	// A frozen contract cannot pay out. A direct tx to it is refused at the
	// ante (it is the tx's `to`); through a relay contract the EVM runs and
	// the commit refuses the contract's balance change.
	h.emergencyFreeze(payer)
	operatorBefore := h.balance(operator.Addr)
	res, err = h.call(operator, relay, forwarderC, "call", payer, pack(t, forwarderC, "send", operator.Addr, oneKASH))
	require.Error(t, err)
	require.NotZero(t, res.Code, res.Log)
	require.Contains(t, res.Log, compliancetypes.ErrAddressFrozen.Error())
	require.Equal(t, 0, five.Cmp(h.balance(payer)), "frozen contract was drained")
	require.Equal(t, 1, operatorBefore.Cmp(h.balance(operator.Addr)), "gas is charged for a commit-time refusal (the accepted, documented cost)")
	// ...but it can still be called without moving value.
	res, err = h.call(operator, relay, forwarderC, "call", payer, []byte{})
	require.NoError(t, err, res.Log)
}

// TestProtocolFlowsReachFrozenAddress: what the fix lets through, each a
// completion the chain owes rather than a party's request, and each landing
// on an account that still cannot spend.
func TestProtocolFlowsReachFrozenAddress(t *testing.T) {
	coin := func(kash int64) sdk.Coin {
		return sdk.NewCoin(config.BaseDenom, sdkmath.NewIntFromBigInt(new(big.Int).Mul(oneKASH, big.NewInt(kash))))
	}

	t.Run("unbonding completion", func(t *testing.T) {
		h := newHarness(t)
		val := h.nw.GetValidators()[0]
		alice := h.key(1)
		res, err := h.sendCosmos(alice, stakingtypes.NewMsgDelegate(alice.AccAddr.String(), val.OperatorAddress, coin(5)))
		require.NoError(t, err)
		require.Zero(t, res.Code, res.Log)
		res, err = h.sendCosmos(alice, stakingtypes.NewMsgUndelegate(alice.AccAddr.String(), val.OperatorAddress, coin(5)))
		require.NoError(t, err)
		require.Zero(t, res.Code, res.Log)

		executeAt := h.scheduleBlock(alice.Addr)
		h.nextBlockAfter(executeAt.Sub(h.ctx().BlockTime()) + time.Second)
		require.True(t, h.isFrozen(alice.Addr))
		before := h.balance(alice.Addr)

		h.nextBlockAfter(config.StakingUnbondingTime)
		require.Equal(t, 0, new(big.Int).Add(before, coin(5).Amount.BigInt()).Cmp(h.balance(alice.Addr)), "unbonding completion did not credit the frozen delegator")
		// Credited, still immobile.
		res, err = h.transfer(alice, h.key(2).Addr, oneKASH)
		require.Error(t, err)
		require.Contains(t, res.Log, compliancetypes.ErrAddressFrozen.Error())
	})

	t.Run("gov deposit refund", func(t *testing.T) {
		// Without the x/gov exemption this is a chain halt: EndBlock returns
		// the refund's error (x/gov/abci.go) on the first proposal a frozen
		// depositor funded.
		h := newHarness(t)
		bob := h.key(2)
		params, err := h.app.GovKeeper.Params.Get(h.ctx())
		require.NoError(t, err)
		deposit := sdk.NewCoins(params.MinDeposit...)
		prop, err := govv1.NewMsgSubmitProposal(nil, deposit, bob.AccAddr.String(), "p20", "P20", "deposit refund to a frozen depositor", false)
		require.NoError(t, err)
		res, err := h.sendCosmos(bob, prop)
		require.NoError(t, err)
		require.Zero(t, res.Code, res.Log)
		proposal, err := h.app.GovKeeper.Proposals.Get(h.ctx(), 1)
		require.NoError(t, err)
		require.Equal(t, govv1.StatusVotingPeriod, proposal.Status)

		executeAt := h.scheduleBlock(bob.Addr)
		h.nextBlockAfter(executeAt.Sub(h.ctx().BlockTime()) + time.Second)
		require.True(t, h.isFrozen(bob.Addr))
		before := h.balance(bob.Addr)

		// No votes: the proposal fails quorum and, not being vetoed, refunds.
		h.nextBlockAfter(proposal.VotingEndTime.Sub(h.ctx().BlockTime()) + time.Second)
		proposal, err = h.app.GovKeeper.Proposals.Get(h.ctx(), 1)
		require.NoError(t, err)
		require.Equal(t, govv1.StatusRejected, proposal.Status)
		require.Equal(t, 0, new(big.Int).Add(before, deposit.AmountOf(config.BaseDenom).BigInt()).Cmp(h.balance(bob.Addr)), "deposit not refunded to the frozen depositor")
	})

	t.Run("validator removal pays commission", func(t *testing.T) {
		// x/distribution pays a removed validator's outstanding commission
		// to its withdraw address inside staking's AfterValidatorRemoved
		// hook, which runs from EndBlock when an emptied validator's
		// unbonding matures. cosmos/evm's harness cannot empty a validator
		// (its genesis delegation shape leaves tokens behind), so the real
		// hooks are driven directly, on a throwaway cache of the committed
		// state, through the staking keeper's hook set as app.New wired it.
		h := newHarness(t)
		operator, sink := h.key(2), h.key(3)
		val := h.nw.GetValidators()[0]
		valAddr, err := sdk.ValAddressFromBech32(val.OperatorAddress)
		require.NoError(t, err)
		consAddr, err := val.GetConsAddr()
		require.NoError(t, err)
		// The operator is not a harness key; the withdraw address it would
		// have set is what matters, and that is what the hook pays.
		_ = operator
		h.emergencyFreeze(sink.Addr)
		for i := 0; i < 3; i++ {
			h.nextBlock() // let fees and issuance reach the distribution module
		}

		owed := sdk.NewCoins(sdk.NewInt64Coin(config.BaseDenom, 1_000))
		distrAcc := h.app.AccountKeeper.GetModuleAddress(distrtypes.ModuleName)
		require.True(t, h.app.BankKeeper.GetBalance(h.ctx(), distrAcc, config.BaseDenom).Amount.GTE(owed[0].Amount), "distribution module holds nothing to pay from")

		run := func(ctx sdk.Context) error {
			require.NoError(t, h.app.DistrKeeper.SetDelegatorWithdrawAddr(ctx, sdk.AccAddress(valAddr), sink.AccAddr))
			require.NoError(t, h.app.DistrKeeper.SetValidatorAccumulatedCommission(ctx, valAddr, distrtypes.ValidatorAccumulatedCommission{Commission: sdk.NewDecCoinsFromCoins(owed...)}))
			require.NoError(t, h.app.DistrKeeper.SetValidatorOutstandingRewards(ctx, valAddr, distrtypes.ValidatorOutstandingRewards{Rewards: sdk.NewDecCoinsFromCoins(owed...)}))
			return h.app.DistrKeeper.Hooks().AfterValidatorRemoved(ctx, consAddr, valAddr)
		}
		// Unmarked, the bank refuses the payout to the frozen sink: this is
		// what the mark exists to avoid.
		bare, _ := h.ctx().CacheContext()
		require.ErrorIs(t, run(bare), compliancetypes.ErrAddressFrozen)

		// As wired — through the staking keeper's hooks — the payout lands.
		wired, _ := h.ctx().CacheContext()
		before := h.app.BankKeeper.GetBalance(wired, sink.AccAddr, config.BaseDenom)
		require.NoError(t, h.app.DistrKeeper.SetDelegatorWithdrawAddr(wired, sdk.AccAddress(valAddr), sink.AccAddr))
		require.NoError(t, h.app.DistrKeeper.SetValidatorAccumulatedCommission(wired, valAddr, distrtypes.ValidatorAccumulatedCommission{Commission: sdk.NewDecCoinsFromCoins(owed...)}))
		require.NoError(t, h.app.DistrKeeper.SetValidatorOutstandingRewards(wired, valAddr, distrtypes.ValidatorOutstandingRewards{Rewards: sdk.NewDecCoinsFromCoins(owed...)}))
		require.NoError(t, h.app.StakingKeeper.Hooks().AfterValidatorRemoved(wired, consAddr, valAddr))
		require.Equal(t, before.Add(owed[0]), h.app.BankKeeper.GetBalance(wired, sink.AccAddr, config.BaseDenom), "commission not paid to the frozen withdraw address on removal")
		left, err := h.app.DistrKeeper.GetValidatorAccumulatedCommission(wired, valAddr)
		require.NoError(t, err)
		require.True(t, left.Commission.IsZero(), "commission record survived removal: %s", left.Commission)
		require.False(t, compliancetypes.IsProtocolFlow(h.ctx()), "mark leaked")
	})
}

// TestFrozenWithdrawAddressRefusesRewardWithdrawal: the flip side — a
// delegator's own request to pay rewards into a frozen withdraw address is
// refused (set a new one first); it is not a protocol flow.
func TestFrozenWithdrawAddressRefusesRewardWithdrawal(t *testing.T) {
	h := newHarness(t)
	val := h.nw.GetValidators()[0]
	alice, sink := h.key(1), h.key(2)
	coin := sdk.NewCoin(config.BaseDenom, sdkmath.NewIntFromBigInt(new(big.Int).Mul(oneKASH, big.NewInt(5))))
	res, err := h.sendCosmos(alice, stakingtypes.NewMsgDelegate(alice.AccAddr.String(), val.OperatorAddress, coin))
	require.NoError(t, err)
	require.Zero(t, res.Code, res.Log)
	res, err = h.sendCosmos(alice, distrtypes.NewMsgSetWithdrawAddress(alice.AccAddr, sink.AccAddr))
	require.NoError(t, err)
	require.Zero(t, res.Code, res.Log)
	h.nextBlock()
	h.emergencyFreeze(sink.Addr)
	sinkBefore := h.balance(sink.Addr)

	res, err = h.sendCosmos(alice, distrtypes.NewMsgWithdrawDelegatorReward(alice.AccAddr.String(), val.OperatorAddress))
	require.NoError(t, err)
	require.NotZero(t, res.Code)
	require.Contains(t, res.Log, compliancetypes.ErrAddressFrozen.Error())
	// The automatic withdrawal a further delegation triggers, too.
	res, err = h.sendCosmos(alice, stakingtypes.NewMsgDelegate(alice.AccAddr.String(), val.OperatorAddress, coin))
	require.NoError(t, err)
	require.NotZero(t, res.Code)
	require.Contains(t, res.Log, compliancetypes.ErrAddressFrozen.Error())
	require.Equal(t, 0, sinkBefore.Cmp(h.balance(sink.Addr)))

	// A new withdraw address, and everything works again.
	res, err = h.sendCosmos(alice, distrtypes.NewMsgSetWithdrawAddress(alice.AccAddr, alice.AccAddr))
	require.NoError(t, err)
	require.Zero(t, res.Code, res.Log)
	res, err = h.sendCosmos(alice, distrtypes.NewMsgWithdrawDelegatorReward(alice.AccAddr.String(), val.OperatorAddress))
	require.NoError(t, err)
	require.Zero(t, res.Code, res.Log)
}

// TestFrozenSenderCosmosPathsRefused: Cosmos flows that debit a frozen
// account through a module are refused at admission, as before.
func TestFrozenSenderCosmosPathsRefused(t *testing.T) {
	h := newHarness(t)
	alice, bob := h.key(1), h.key(2)
	h.emergencyFreeze(alice.Addr)
	amount := sdk.NewCoins(sdk.NewCoin(config.BaseDenom, sdkmath.NewIntFromBigInt(oneKASH)))
	for _, msg := range []sdk.Msg{
		&banktypes.MsgSend{FromAddress: alice.AccAddr.String(), ToAddress: bob.AccAddr.String(), Amount: amount},
		distrtypes.NewMsgFundCommunityPool(amount, alice.AccAddr.String()),
	} {
		chk := h.checkTxCosmos(alice, msg)
		require.NotZero(t, chk.Code)
		require.Contains(t, chk.Log, compliancetypes.ErrAddressFrozen.Error())
	}
}

// ── helpers ───────────────────────────────────────────────────────────────

func pack(t *testing.T, c evmtypes.CompiledContract, method string, args ...interface{}) []byte {
	t.Helper()
	bz, err := c.ABI.Pack(method, args...)
	require.NoError(t, err)
	return bz
}
