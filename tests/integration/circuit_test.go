//go:build test

package integration

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	ics20precompile "github.com/cosmos/evm/precompiles/ics20"
	stakingprecompile "github.com/cosmos/evm/precompiles/staking"
	evmtypes "github.com/cosmos/evm/x/vm/types"
	transfertypes "github.com/cosmos/ibc-go/v11/modules/apps/transfer/types"

	sdkmath "cosmossdk.io/math"

	circuittypes "github.com/cosmos/cosmos-sdk/contrib/x/circuit/types"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/authz"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	govv1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/Konstellation-Network/konstellation/app/config"
)

// Safety rail 1 (ENGINEERING.md §13): x/circuit. An account with
// super-admin permission (in production the 3-of-5 operations multisig, set
// in genesis) can stop specific message types without halting the chain.
func TestCircuitBreaker(t *testing.T) {
	h := newHarness(t)
	admin, alice, bob := h.authority, h.key(1), h.key(2)
	send := &banktypes.MsgSend{
		FromAddress: alice.AccAddr.String(), ToAddress: bob.AccAddr.String(),
		Amount: sdk.NewCoins(sdk.NewCoin(config.BaseDenom, sdkmath.NewIntFromBigInt(oneKASH))),
	}
	sendURL := sdk.MsgTypeURL(send)
	evmURL := sdk.MsgTypeURL(&evmtypes.MsgEthereumTx{})
	xfer := evmtypes.EvmTxArgs{To: &bob.Addr, Amount: oneKASH, GasLimit: 21_000}

	// Baseline: both work.
	res, err := h.sendCosmos(alice, send)
	require.NoError(t, err)
	require.Zero(t, res.Code, res.Log)
	res, err = h.sendEVM(alice, xfer)
	require.NoError(t, err, res.Log)

	// Trip bank sends and the EVM.
	res, err = h.sendCosmos(admin, &circuittypes.MsgTripCircuitBreaker{
		Authority: admin.AccAddr.String(), MsgTypeUrls: []string{sendURL, evmURL},
	})
	require.NoError(t, err)
	require.Zero(t, res.Code, res.Log)

	// Refused at mempool admission with the reason …
	chk := h.checkTxCosmos(alice, send)
	require.NotZero(t, chk.Code)
	require.Contains(t, chk.Log, "circuit breaker disables "+sendURL)
	chkEVM := h.checkTxEVM(alice, xfer)
	require.NotZero(t, chkEVM.Code)
	require.Contains(t, chkEVM.Log, "circuit breaker disables "+evmURL)

	// … and at delivery, with nothing charged.
	aliceBefore := h.balance(alice.Addr)
	res, err = h.sendCosmos(alice, send)
	require.NoError(t, err)
	require.NotZero(t, res.Code)
	res, err = h.sendEVM(alice, xfer)
	require.Error(t, err, res.Log)
	require.Equal(t, 0, aliceBefore.Cmp(h.balance(alice.Addr)))

	// Nested in authz MsgExec: refused at admission too (app/circuit.go
	// walks into MsgExec), not only by the router at execution, so a
	// tripped type cannot be smuggled into blocks for its fee.
	exec := authz.NewMsgExec(alice.AccAddr, []sdk.Msg{send})
	chk = h.checkTxCosmos(alice, &exec)
	require.NotZero(t, chk.Code, "authz-wrapped MsgSend admitted to the mempool")
	require.Contains(t, chk.Log, "circuit breaker disables "+sendURL)
	res, err = h.sendCosmos(alice, &exec)
	require.NoError(t, err)
	require.NotZero(t, res.Code, "authz-wrapped MsgSend slipped past the circuit breaker")
	require.Contains(t, res.Log, "circuit breaker")

	// Other message types keep working: the chain is not halted.
	res, err = h.sendCosmos(alice, &banktypes.MsgMultiSend{
		Inputs:  []banktypes.Input{{Address: alice.AccAddr.String(), Coins: send.Amount}},
		Outputs: []banktypes.Output{{Address: bob.AccAddr.String(), Coins: send.Amount}},
	})
	require.NoError(t, err)
	require.Zero(t, res.Code, res.Log)

	// A plain user cannot trip anything.
	res, err = h.sendCosmos(alice, &circuittypes.MsgTripCircuitBreaker{
		Authority: alice.AccAddr.String(), MsgTypeUrls: []string{sdk.MsgTypeURL(&banktypes.MsgMultiSend{})},
	})
	require.NoError(t, err)
	require.NotZero(t, res.Code, "unauthorised account tripped the breaker")

	// Reset restores both.
	res, err = h.sendCosmos(admin, &circuittypes.MsgResetCircuitBreaker{
		Authority: admin.AccAddr.String(), MsgTypeUrls: []string{sendURL, evmURL},
	})
	require.NoError(t, err)
	require.Zero(t, res.Code, res.Log)
	res, err = h.sendCosmos(alice, send)
	require.NoError(t, err)
	require.Zero(t, res.Code, res.Log)
	res, err = h.sendEVM(alice, xfer)
	require.NoError(t, err, res.Log)
}

// TestCircuitBreakerCoversPrecompiles: cosmos/evm's tx-path precompiles
// call keepers and msg servers directly, never the router, so without
// app/circuit_precompiles.go a tripped MsgTransfer stopped Cosmos senders and
// left the ICS20 precompile open (PR #12 review). Now the precompile reverts
// with the same reason the ante gives.
func TestCircuitBreakerCoversPrecompiles(t *testing.T) {
	h := newHarness(t)
	admin, alice := h.authority, h.key(1)
	validator := h.nw.GetValidators()[0].OperatorAddress
	staking := common.HexToAddress(evmtypes.StakingPrecompileAddress)
	ics20 := common.HexToAddress(evmtypes.ICS20PrecompileAddress)
	stakingC := evmtypes.CompiledContract{ABI: stakingprecompile.ABI}
	ics20C := evmtypes.CompiledContract{ABI: ics20precompile.ABI}
	delegateURL := sdk.MsgTypeURL(&stakingtypes.MsgDelegate{})
	transferURL := sdk.MsgTypeURL(&transfertypes.MsgTransfer{})
	timeout := struct {
		RevisionNumber uint64
		RevisionHeight uint64
	}{0, 0}

	// Positive control: with nothing tripped the staking precompile
	// delegates, and the ICS20 precompile fails for the ordinary reason
	// (this chain has no channel), not the breaker's.
	res, err := h.call(alice, staking, stakingC, "delegate", alice.Addr, validator, oneKASH)
	require.NoError(t, err, res.Log)
	_, err = h.call(alice, ics20, ics20C, "transfer", "transfer", "channel-0", config.BaseDenom, oneKASH, alice.Addr, "kons1receiver", timeout, uint64(0), "")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "circuit breaker")

	// Trip both message types.
	res, err = h.sendCosmos(admin, &circuittypes.MsgTripCircuitBreaker{
		Authority: admin.AccAddr.String(), MsgTypeUrls: []string{delegateURL, transferURL},
	})
	require.NoError(t, err)
	require.Zero(t, res.Code, res.Log)

	// The precompiles now revert with the reason.
	_, err = h.call(alice, staking, stakingC, "delegate", alice.Addr, validator, oneKASH)
	require.Error(t, err)
	require.Contains(t, err.Error(), "circuit breaker disables "+delegateURL)
	_, err = h.call(alice, ics20, ics20C, "transfer", "transfer", "channel-0", config.BaseDenom, oneKASH, alice.Addr, "kons1receiver", timeout, uint64(0), "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "circuit breaker disables "+transferURL)

	// Other precompile methods, and queries on the same precompile, still work.
	_, err = h.query(staking, stakingC, "delegation", alice.Addr, validator)
	require.NoError(t, err)
	res, err = h.call(alice, staking, stakingC, "undelegate", alice.Addr, validator, oneKASH)
	require.NoError(t, err, res.Log)

	// Reset restores the precompile path too.
	res, err = h.sendCosmos(admin, &circuittypes.MsgResetCircuitBreaker{
		Authority: admin.AccAddr.String(), MsgTypeUrls: []string{delegateURL, transferURL},
	})
	require.NoError(t, err)
	require.Zero(t, res.Code, res.Log)
	res, err = h.call(alice, staking, stakingC, "delegate", alice.Addr, validator, oneKASH)
	require.NoError(t, err, res.Log)
}

// TestCircuitBreakerCannotWeldItselfShut: the breaker's own messages and
// governance's can never be disabled, so a trip is always resettable
// (PR #12 review).
func TestCircuitBreakerCannotWeldItselfShut(t *testing.T) {
	h := newHarness(t)
	admin := h.authority
	for _, url := range []string{
		sdk.MsgTypeURL(&circuittypes.MsgResetCircuitBreaker{}),
		sdk.MsgTypeURL(&circuittypes.MsgTripCircuitBreaker{}),
		sdk.MsgTypeURL(&govv1.MsgVote{}),
		sdk.MsgTypeURL(&govv1.MsgSubmitProposal{}),
	} {
		trip := &circuittypes.MsgTripCircuitBreaker{Authority: admin.AccAddr.String(), MsgTypeUrls: []string{url}}
		chk := h.checkTxCosmos(admin, trip)
		require.NotZero(t, chk.Code, url)
		require.Contains(t, chk.Log, "cannot be disabled")
		// Even delivered by a proposer that skipped CheckTx, it is refused.
		res, err := h.sendCosmos(admin, trip)
		require.NoError(t, err)
		require.NotZero(t, res.Code, url)
	}
}

// TestValidatorAdmissionWindow is ENGINEERING.md D16 end to end: genesis
// ships /cosmos.staking.v1beta1.MsgCreateValidator in x/circuit's disable
// list, so an operator's create-validator is refused at the mempool and in
// a block; the super admin (the 3-of-5 operations multisig on a real
// network) resets the breaker, the operator's tx lands and the validator
// enters the set, the admin trips it again and the door is shut — the
// procedure in infra/runbooks/validator-admission.md. The launch validators
// are unaffected because they exist before the list is written (gentxs run
// first; app/genesis_test.go pins the order).
func TestValidatorAdmissionWindow(t *testing.T) {
	h := newHarness(t)
	admin, operator := h.authority, h.key(1)
	createURL := sdk.MsgTypeURL(&stakingtypes.MsgCreateValidator{})
	refusal := "circuit breaker disables " + createURL + ": unauthorized"

	// Straight from genesis: disabled, and it is the genesis that says so.
	allowed, err := h.app.CircuitKeeper.IsAllowed(h.ctx(), createURL)
	require.NoError(t, err)
	require.False(t, allowed, "MsgCreateValidator is not disabled at genesis (D16)")
	// bonded is the active set as the staking keeper sees it after the last
	// EndBlock (the harness's own validator list is static).
	bonded := func() int {
		vals, err := h.app.StakingKeeper.GetLastValidators(h.ctx())
		require.NoError(t, err)
		return len(vals)
	}
	launchSet := bonded()
	require.Equal(t, 1, launchSet, "the launch validator (genesis) must be in the set despite the disable list")

	createValidator, err := stakingtypes.NewMsgCreateValidator(
		sdk.ValAddress(operator.AccAddr).String(), ed25519.GenPrivKey().PubKey(),
		sdk.NewCoin(config.BaseDenom, sdkmath.NewIntFromBigInt(oneKASH)),
		stakingtypes.NewDescription("operator", "", "", "", ""),
		stakingtypes.NewCommissionRates(config.StakingMinCommissionRate, sdkmath.LegacyNewDecWithPrec(2, 1), sdkmath.LegacyNewDecWithPrec(1, 2)),
		sdkmath.OneInt(),
	)
	require.NoError(t, err)

	// Refused at admission with the exact reason, and at delivery.
	chk := h.checkTxCosmos(operator, createValidator)
	require.NotZero(t, chk.Code, "create-validator admitted to the mempool")
	require.Contains(t, chk.Log, refusal)
	res, err := h.sendCosmos(operator, createValidator)
	require.NoError(t, err)
	require.NotZero(t, res.Code, "create-validator executed while disabled")
	require.Contains(t, res.Log, refusal)
	require.Equal(t, launchSet, bonded())

	// The window: reset → create → disable again.
	res, err = h.sendCosmos(admin, &circuittypes.MsgResetCircuitBreaker{
		Authority: admin.AccAddr.String(), MsgTypeUrls: []string{createURL},
	})
	require.NoError(t, err)
	require.Zero(t, res.Code, res.Log)
	res, err = h.sendCosmos(operator, createValidator)
	require.NoError(t, err)
	require.Zero(t, res.Code, res.Log)
	// It enters the active set: 20 of the 30 seats are empty and 1 KASH is
	// one unit of power (D16's note on the window), so this is expected —
	// and harmless against the stake bonded at genesis.
	require.Equal(t, launchSet+1, bonded(), "admitted validator did not enter the set")

	res, err = h.sendCosmos(admin, &circuittypes.MsgTripCircuitBreaker{
		Authority: admin.AccAddr.String(), MsgTypeUrls: []string{createURL},
	})
	require.NoError(t, err)
	require.Zero(t, res.Code, res.Log)

	// Shut again: the next operator is refused exactly as the first was.
	next, err := stakingtypes.NewMsgCreateValidator(
		sdk.ValAddress(h.key(2).AccAddr).String(), ed25519.GenPrivKey().PubKey(),
		sdk.NewCoin(config.BaseDenom, sdkmath.NewIntFromBigInt(oneKASH)),
		stakingtypes.NewDescription("next", "", "", "", ""),
		stakingtypes.NewCommissionRates(config.StakingMinCommissionRate, sdkmath.LegacyNewDecWithPrec(2, 1), sdkmath.LegacyNewDecWithPrec(1, 2)),
		sdkmath.OneInt(),
	)
	require.NoError(t, err)
	chk = h.checkTxCosmos(h.key(2), next)
	require.NotZero(t, chk.Code)
	require.Contains(t, chk.Log, refusal)
	require.Equal(t, launchSet+1, bonded())

	// The admitted validator keeps working: delegation is open (D7 —
	// "permissioned" is who may validate, not who may delegate).
	res, err = h.sendCosmos(h.key(3), &stakingtypes.MsgDelegate{
		DelegatorAddress: h.key(3).AccAddr.String(),
		ValidatorAddress: sdk.ValAddress(operator.AccAddr).String(),
		Amount:           sdk.NewCoin(config.BaseDenom, sdkmath.NewIntFromBigInt(oneKASH)),
	})
	require.NoError(t, err)
	require.Zero(t, res.Code, res.Log)
}
