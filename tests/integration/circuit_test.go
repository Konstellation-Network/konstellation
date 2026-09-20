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
