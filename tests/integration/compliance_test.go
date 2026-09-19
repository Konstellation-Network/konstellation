//go:build test

package integration

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	abcitypes "github.com/cometbft/cometbft/abci/types"

	"github.com/cosmos/evm/tests/contracts"
	evmtypes "github.com/cosmos/evm/x/vm/types"

	sdkmath "cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"

	"github.com/Konstellation-Network/konstellation/app/config"
	compliancetypes "github.com/Konstellation-Network/konstellation/x/compliance/types"
)

// These are the by-hand checks from konstellation PR #10 (2026-09-17),
// automated: real signed txs through the real ante chain on the real app.
// The unit suite in x/compliance/ante uses a fake tx and once missed a
// change that rejected every EVM tx (f63c1b7); this is the test that would
// have caught it.

var oneKASH = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)

func TestComplianceIsWired(t *testing.T) {
	h := newHarness(t)
	require.True(t, h.app.ComplianceKeeper.HasEVMKeeper(), "app.New did not hand x/vm to x/compliance: delegations would survive a freeze")
	p := h.app.ComplianceKeeper.GetParams(h.ctx())
	require.True(t, p.Enforce)
	require.Equal(t, h.authority.AccAddr.String(), p.Authority)
	require.Equal(t, evmChainID, evmtypes.GetChainConfig().GetChainId())
}

func TestCleanEVMTransferAndDeployWork(t *testing.T) {
	h := newHarness(t)
	alice, bob := h.key(1), h.key(2)

	bobBefore := h.balance(bob.Addr)
	res, err := h.transfer(alice, bob.Addr, oneKASH)
	require.NoError(t, err, res.Log)
	require.Equal(t, 0, new(big.Int).Add(bobBefore, oneKASH).Cmp(h.balance(bob.Addr)))

	erc20, err := contracts.LoadSimpleERC20()
	require.NoError(t, err)
	token := h.deploy(alice, erc20)
	ret, err := h.query(token, erc20, "balanceOf", alice.Addr)
	require.NoError(t, err)
	var bal *big.Int
	require.NoError(t, erc20.ABI.UnpackIntoInterface(&bal, "balanceOf", ret))
	require.Equal(t, 1, bal.Sign(), "deployer holds the initial supply")
}

func TestFrozenSenderRejectedEverywhere(t *testing.T) {
	h := newHarness(t)
	alice, bob := h.key(1), h.key(2)
	h.emergencyFreeze(alice.Addr)
	require.True(t, h.isFrozen(alice.Addr))
	aliceBefore, bobBefore := h.balance(alice.Addr), h.balance(bob.Addr)
	xfer := evmtypes.EvmTxArgs{To: &bob.Addr, Amount: oneKASH, GasLimit: 21_000}

	// Mempool admission: the synchronous pre-check answers with the reason.
	chk := h.checkTxEVM(alice, xfer)
	require.NotZero(t, chk.Code)
	require.Contains(t, chk.Log, compliancetypes.ErrAddressFrozen.Error())

	// Delivery (a proposer that skipped CheckTx): the ante rejects it, so
	// no gas is charged either.
	res, err := h.sendEVM(alice, xfer)
	require.Error(t, err)
	require.Contains(t, res.Log, compliancetypes.ErrAddressFrozen.Error())

	// Cosmos path too.
	res, err = h.sendCosmos(alice, &banktypes.MsgSend{
		FromAddress: alice.AccAddr.String(), ToAddress: bob.AccAddr.String(),
		Amount: sdk.NewCoins(sdk.NewCoin(config.BaseDenom, sdkmath.NewIntFromBigInt(oneKASH))),
	})
	require.NoError(t, err) // the factory only errors on build/broadcast for Cosmos txs
	require.NotZero(t, res.Code)
	require.Contains(t, res.Log, compliancetypes.ErrAddressFrozen.Error())

	require.Equal(t, 0, aliceBefore.Cmp(h.balance(alice.Addr)), "frozen sender was charged")
	require.Equal(t, 0, bobBefore.Cmp(h.balance(bob.Addr)))

	// The emergency freeze lapses after one timelock; alice can move again.
	h.nextBlockAfter(complianceTimelock + time.Second)
	require.False(t, h.isFrozen(alice.Addr))
	res, err = h.sendEVM(alice, xfer)
	require.NoError(t, err, res.Log)
}

func TestFrozenRecipientRejected(t *testing.T) {
	h := newHarness(t)
	alice, bob := h.key(1), h.key(2)
	h.emergencyFreeze(bob.Addr)
	aliceBefore, bobBefore := h.balance(alice.Addr), h.balance(bob.Addr)

	res, err := h.transfer(alice, bob.Addr, oneKASH)
	require.Error(t, err)
	require.Contains(t, res.Log, compliancetypes.ErrAddressFrozen.Error())
	require.Equal(t, 0, aliceBefore.Cmp(h.balance(alice.Addr)), "sender charged for an ante rejection")
	require.Equal(t, 0, bobBefore.Cmp(h.balance(bob.Addr)))
}

func TestRelayed7702AuthorizationByFrozenAuthorityRejected(t *testing.T) {
	// A clean relayer carries a set-code authorization signed by a frozen
	// key. Without the authority-is-a-signer rule (PR #10 review) this would
	// install code on the frozen EOA.
	h := newHarness(t)
	relayer, frozen := h.key(1), h.key(2)
	wallet := h.deploy(relayer, mustLoad(t, contracts.LoadSimpleSmartWallet))
	h.emergencyFreeze(frozen.Addr)

	auth := h.signedAuthorization(frozen, wallet, false)
	res, err := h.setCode(relayer, auth)
	require.Error(t, err)
	require.Contains(t, res.Log, compliancetypes.ErrAddressFrozen.Error())
	_, delegated := h.delegationOf(frozen.Addr)
	require.False(t, delegated)
}

// TestFreezeResetsExistingDelegation is the follow-up from the PR #10
// reviews: an EOA delegated (EIP-7702) to a smart-account wallet *before* it
// is frozen. The ante cannot see internal calls, so a clean relayer could
// still drain it through the wallet's EntryPoint. Reproduced live on
// cb6ace1; the fix clears the delegation at freeze time.
func TestFreezeResetsExistingDelegation(t *testing.T) {
	h := newHarness(t)
	relayer, victim, sink := h.key(1), h.key(2), h.key(3)
	entryPointC := mustLoad(t, contracts.LoadSimpleEntryPoint)
	walletC := mustLoad(t, contracts.LoadSimpleSmartWallet)
	entryPoint := h.deploy(relayer, entryPointC)
	wallet := h.deploy(relayer, walletC)

	// victim installs the wallet on its own account and initialises it.
	res, err := h.setCode(victim, h.signedAuthorization(victim, wallet, true))
	require.NoError(t, err, res.Log)
	delegate, ok := h.delegationOf(victim.Addr)
	require.True(t, ok)
	require.Equal(t, wallet, delegate)
	res, err = h.call(victim, victim.Addr, walletC, "initialize", victim.Addr, entryPoint)
	require.NoError(t, err, res.Log)
	require.Equal(t, victim.Addr, h.walletOwner(victim.Addr, walletC))

	// The drain path, while unfrozen, to prove the mechanism is real: a
	// UserOp signed by victim, relayed by someone else, moves victim's
	// native balance to sink through wallet.execute.
	drain := func() (abcitypes.ExecTxResult, error) {
		callData, err := walletC.ABI.Pack("execute", sink.Addr, oneKASH, []byte{})
		require.NoError(t, err)
		op := signedUserOp(t, victim, victim.Addr, entryPoint, 0, callData)
		return h.call(relayer, entryPoint, entryPointC, "handleOps", []UserOperation{op})
	}
	sinkBefore := h.balance(sink.Addr)
	res, err = drain()
	require.NoError(t, err, res.Log)
	require.Equal(t, 0, new(big.Int).Add(sinkBefore, oneKASH).Cmp(h.balance(sink.Addr)), "UserOp drain did not move funds while unfrozen: test setup is wrong")

	// Freeze. The delegation must go with it.
	freeze := h.emergencyFreeze(victim.Addr)
	require.True(t, h.isFrozen(victim.Addr))
	_, ok = h.delegationOf(victim.Addr)
	require.False(t, ok, "delegation survived the freeze")
	requireEvent(t, freeze.Events, compliancetypes.EventTypeDelegationReset, map[string]string{
		compliancetypes.AttributeKeyAddress:  victim.AccAddr.String(),
		compliancetypes.AttributeKeyDelegate: wallet.Hex(),
	})

	// Same relayed UserOp again: nothing moves. (SimpleEntryPoint's
	// validateUserOp call now hits an account without code.)
	victimBefore, sinkBefore := h.balance(victim.Addr), h.balance(sink.Addr)
	_, _ = drain()
	require.Equal(t, 0, victimBefore.Cmp(h.balance(victim.Addr)), "frozen delegated EOA was drained")
	require.Equal(t, 0, sinkBefore.Cmp(h.balance(sink.Addr)))
	// eth_call to a code-less account succeeds with no data; the delegate's
	// owner() would have returned 32 bytes.
	ret, err := h.query(victim.Addr, walletC, "owner")
	require.NoError(t, err)
	require.Empty(t, ret, "delegate code still runs at the frozen address")

	// Storage survives the reset: once lifted, one new authorization brings
	// the wallet back already initialised.
	h.liftEmergencyFreeze(victim.Addr)
	require.False(t, h.isFrozen(victim.Addr))
	res, err = h.setCode(victim, h.signedAuthorization(victim, wallet, true))
	require.NoError(t, err, res.Log)
	require.Equal(t, victim.Addr, h.walletOwner(victim.Addr, walletC), "wallet storage lost across freeze")
}

func TestScheduledFreezeResetsDelegationAtExecution(t *testing.T) {
	h := newHarness(t)
	victim := h.key(2)
	wallet := h.deploy(h.key(1), mustLoad(t, contracts.LoadSimpleSmartWallet))
	res, err := h.setCode(victim, h.signedAuthorization(victim, wallet, true))
	require.NoError(t, err, res.Log)

	executeAt := h.scheduleBlock(victim.Addr)
	_, ok := h.delegationOf(victim.Addr)
	require.True(t, ok, "reset at scheduling time; the address is not frozen yet")
	require.False(t, h.isFrozen(victim.Addr))

	h.nextBlockAfter(executeAt.Sub(h.ctx().BlockTime()) + time.Second)
	require.True(t, h.isFrozen(victim.Addr))
	_, ok = h.delegationOf(victim.Addr)
	require.False(t, ok, "delegation survived the scheduled freeze")
}

func TestFreezingAContractKeepsItsCode(t *testing.T) {
	// Only 0xef0100‖addr delegations are reset. Freezing a contract address
	// (a sanctioned token, say) must not delete its bytecode.
	h := newHarness(t)
	erc20 := mustLoad(t, contracts.LoadSimpleERC20)
	token := h.deploy(h.key(1), erc20)
	h.emergencyFreeze(token)
	require.True(t, h.isFrozen(token))
	ek := h.app.GetEVMKeeper()
	require.NotEmpty(t, ek.GetCode(h.ctx(), ek.GetCodeHash(h.ctx(), token)), "contract code deleted by a freeze")
	_, err := h.query(token, erc20, "balanceOf", h.key(1).Addr)
	require.NoError(t, err)
}

// ── helpers ───────────────────────────────────────────────────────────────

func mustLoad(t *testing.T, load func() (evmtypes.CompiledContract, error)) evmtypes.CompiledContract {
	t.Helper()
	c, err := load()
	require.NoError(t, err)
	return c
}

func (h *harness) walletOwner(at common.Address, walletC evmtypes.CompiledContract) common.Address {
	h.t.Helper()
	ret, err := h.query(at, walletC, "owner")
	require.NoError(h.t, err)
	var owner common.Address
	require.NoError(h.t, walletC.ABI.UnpackIntoInterface(&owner, "owner", ret))
	return owner
}

func requireEvent(t *testing.T, events []abcitypes.Event, typ string, attrs map[string]string) {
	t.Helper()
	for _, e := range events {
		if e.Type != typ {
			continue
		}
		got := map[string]string{}
		for _, a := range e.Attributes {
			got[a.Key] = a.Value
		}
		match := true
		for k, v := range attrs {
			if got[k] != v {
				match = false
			}
		}
		if match {
			return
		}
	}
	t.Fatalf("no %s event with %v in %d events", typ, attrs, len(events))
}
