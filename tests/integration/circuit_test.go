//go:build test

package integration

import (
	"testing"

	"github.com/stretchr/testify/require"

	evmtypes "github.com/cosmos/evm/x/vm/types"

	sdkmath "cosmossdk.io/math"

	circuittypes "github.com/cosmos/cosmos-sdk/contrib/x/circuit/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/authz"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"

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

	// Nested in authz MsgExec: the ante decorator only sees the outer
	// message; the router check at execution is what catches this.
	exec := authz.NewMsgExec(alice.AccAddr, []sdk.Msg{send})
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
