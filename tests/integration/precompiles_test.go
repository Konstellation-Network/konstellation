//go:build test

package integration

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/stretchr/testify/require"

	evmtypes "github.com/cosmos/evm/x/vm/types"

	"github.com/Konstellation-Network/konstellation/app"
)

// TestActivePrecompilesAreServed: every address genesis marks active in
// x/vm's params must have an implementation registered in the keeper. On
// cosmos/evm v0.7.3 AvailableStaticPrecompiles lists a vesting precompile
// (0x…0803) that upstream never implemented; with it active, eth_call to
// that address answered "precompiled contract not stored in memory" and a
// tx to it failed the same way (STATUS.md §5a P24). Genesis now leaves it
// out (app.InertUpstreamPrecompiles); this proves the remaining list is
// served, and shows what activating an unimplemented address does.
func TestActivePrecompilesAreServed(t *testing.T) {
	h := newHarness(t)
	ek := h.app.GetEVMKeeper()
	params := ek.GetParams(h.ctx())
	require.NotEmpty(t, params.ActiveStaticPrecompiles)

	// junk selector: any served precompile answers (a revert for an unknown
	// method, or data), and never the keeper's "not stored in memory".
	selector := hexutil.Bytes{0x12, 0x34, 0x56, 0x78}
	for _, hexAddr := range params.ActiveStaticPrecompiles {
		addr := common.HexToAddress(hexAddr)

		// The keeper: an active address with no implementation panics here
		// on every EVM call.
		require.NotPanics(t, func() {
			p, found, err := ek.GetStaticPrecompileInstance(&params, addr)
			require.NoError(t, err, hexAddr)
			require.True(t, found, "%s is active but not a static precompile", hexAddr)
			require.NotNil(t, p, hexAddr)
		}, "%s is active in genesis but has no implementation", hexAddr)

		// eth_call, as a dapp would see it.
		args, err := json.Marshal(evmtypes.TransactionArgs{To: &addr, Input: &selector})
		require.NoError(t, err)
		res, err := h.nw.GetEvmClient().EthCall(context.Background(), &evmtypes.EthCallRequest{Args: args, GasCap: gasLimit})
		require.NoError(t, err, "eth_call to active precompile %s errored", hexAddr)
		require.NotContains(t, res.VmError, "not stored in memory", hexAddr)
	}

	// Negative control: the address genesis leaves out really is unserved,
	// so the test above would have caught P24 — and will catch the reverse
	// mistake after an upstream bump that implements it, via
	// app/genesis_test.go's pin on InertUpstreamPrecompiles.
	for _, inert := range app.InertUpstreamPrecompiles {
		require.NotContains(t, params.ActiveStaticPrecompiles, inert)
		withInert := params
		withInert.ActiveStaticPrecompiles = append(append([]string{}, params.ActiveStaticPrecompiles...), inert)
		require.PanicsWithError(t, "precompiled contract not stored in memory: "+inert, func() {
			_, _, _ = ek.GetStaticPrecompileInstance(&withInert, common.HexToAddress(inert))
		}, "%s is now served upstream: remove it from app.InertUpstreamPrecompiles", inert)
	}
}
