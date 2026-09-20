package app

import (
	"testing"

	"github.com/stretchr/testify/require"

	circuitkeeper "github.com/cosmos/cosmos-sdk/contrib/x/circuit/keeper"
	circuittypes "github.com/cosmos/cosmos-sdk/contrib/x/circuit/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	storetypes "github.com/cosmos/cosmos-sdk/store/v2/types"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	govv1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
)

// TestCircuitBreakerIgnoresProtectedTypes: even with a protected type in
// the disable list (a genesis could put it there; the ante refuses a trip
// that names one), the breaker every path consults lets it through.
func TestCircuitBreakerIgnoresProtectedTypes(t *testing.T) {
	key := storetypes.NewKVStoreKey(circuittypes.StoreKey)
	tc := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("t_circuit"))
	cdc := moduletestutil.MakeTestEncodingConfig().Codec
	gov := authtypes.NewModuleAddress(govtypes.ModuleName).String()
	k := circuitkeeper.NewKeeper(cdc, runtime.NewKVStoreService(key), gov, moduletestutil.MakeTestEncodingConfig().InterfaceRegistry.SigningContext().AddressCodec())
	b := circuitBreaker{&k}

	reset := sdk.MsgTypeURL(&circuittypes.MsgResetCircuitBreaker{})
	vote := sdk.MsgTypeURL(&govv1.MsgVote{})
	send := sdk.MsgTypeURL(&banktypes.MsgSend{})
	for _, url := range []string{reset, vote, send} {
		require.NoError(t, k.DisableList.Set(tc.Ctx, url))
	}
	for _, url := range []string{reset, vote} {
		allowed, err := b.IsAllowed(tc.Ctx, url)
		require.NoError(t, err)
		require.True(t, allowed, "%s must stay allowed", url)
		raw, err := k.IsAllowed(tc.Ctx, url)
		require.NoError(t, err)
		require.False(t, raw, "the raw keeper would have blocked %s; the wrapper is what protects it", url)
	}
	allowed, err := b.IsAllowed(tc.Ctx, send)
	require.NoError(t, err)
	require.False(t, allowed, "ordinary types are still governed by the disable list")
}
