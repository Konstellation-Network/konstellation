package app

import (
	"slices"
	"testing"

	dbm "github.com/cosmos/cosmos-db"
	cosmosevmutils "github.com/cosmos/evm/utils"
	evmtypes "github.com/cosmos/evm/x/vm/types"

	"cosmossdk.io/log/v2"

	circuittypes "github.com/cosmos/cosmos-sdk/contrib/x/circuit/types"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	genutiltypes "github.com/cosmos/cosmos-sdk/x/genutil/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/Konstellation-Network/konstellation/app/config"
	complianceprecompile "github.com/Konstellation-Network/konstellation/x/compliance/precompile"
)

func bech32OfHex(h string) string { return cosmosevmutils.Bech32StringFromHexAddress(h) }

func TestCompliancePrecompileAddressPinned(t *testing.T) {
	if config.CompliancePrecompileAddress != complianceprecompile.Address {
		t.Fatalf("config %s != precompile %s", config.CompliancePrecompileAddress, complianceprecompile.Address)
	}
	gs := NewEVMGenesisState()
	if !slices.Contains(gs.Params.ActiveStaticPrecompiles, complianceprecompile.Address) {
		t.Fatal("compliance precompile not active in genesis")
	}
	if !slices.IsSorted(gs.Params.ActiveStaticPrecompiles) {
		t.Fatal("active precompiles must be sorted")
	}
	if err := gs.Validate(); err != nil {
		t.Fatal(err)
	}
	if !config.BlockedAddresses()[bech32OfHex(complianceprecompile.Address)] {
		t.Fatal("compliance precompile must be a blocked bank address")
	}
}

// testApp builds the app the way cmd/konstellationd does for its "temp"
// instance: in-memory, no genesis, only to read module wiring and defaults.
func testApp(t *testing.T) *KonstellationApp {
	t.Helper()
	a := New(log.NewNopLogger(), dbm.NewMemDB(), true, simtestutil.EmptyAppOptions{})
	t.Cleanup(func() { _ = a.Close() })
	return a
}

// STATUS.md §5a P24: cosmos/evm v0.7.3 lists a vesting precompile it does
// not implement; marking it active makes every call to 0x…0803 fail. The
// active list must be what upstream serves plus ours, and nothing else.
func TestActivePrecompilesExcludeInertUpstream(t *testing.T) {
	gs := NewEVMGenesisState()
	active := gs.Params.ActiveStaticPrecompiles
	for _, inert := range InertUpstreamPrecompiles {
		if slices.Contains(active, inert) {
			t.Errorf("inert upstream precompile %s is active in genesis", inert)
		}
		// Still a reserved slot: it must not be able to hold funds.
		if !config.BlockedAddresses()[bech32OfHex(inert)] {
			t.Errorf("inert precompile %s must stay a blocked bank address", inert)
		}
	}
	if !slices.Contains(InertUpstreamPrecompiles, evmtypes.VestingPrecompileAddress) {
		t.Error("v0.7.3's vesting precompile (0x…0803) must be listed inert; re-verify on a cosmos/evm bump")
	}
	for _, addr := range evmtypes.AvailableStaticPrecompiles {
		if slices.Contains(InertUpstreamPrecompiles, addr) {
			continue
		}
		if !slices.Contains(active, addr) {
			t.Errorf("implemented upstream precompile %s missing from the active list", addr)
		}
	}
	if want := len(evmtypes.AvailableStaticPrecompiles) - len(InertUpstreamPrecompiles) + 1; len(active) != want {
		t.Errorf("active list has %d entries, want %d", len(active), want)
	}
	if err := gs.Validate(); err != nil {
		t.Fatal(err)
	}
}

// ENGINEERING.md D16: MsgCreateValidator ships disabled in x/circuit's
// genesis state. The list must name nothing the breaker protects (those are
// ignored at runtime, so listing one would be a silent no-op).
func TestCircuitGenesisDisablesCreateValidator(t *testing.T) {
	gs := NewCircuitGenesisState()
	createValidator := sdk.MsgTypeURL(&stakingtypes.MsgCreateValidator{})
	if !slices.Contains(gs.DisabledTypeUrls, createValidator) {
		t.Fatalf("circuit genesis disables %v, want %s", gs.DisabledTypeUrls, createValidator)
	}
	if len(gs.AccountPermissions) != 0 {
		t.Fatal("the super admin is a per-network genesis entry, never a code default")
	}
	for _, url := range gs.DisabledTypeUrls {
		if circuitProtected[url] {
			t.Errorf("%s is protected: the breaker ignores it, so disabling it in genesis does nothing", url)
		}
	}
	if err := gs.Validate(); err != nil {
		t.Fatal(err)
	}
}

// D16 applies to every network (ENGINEERING.md §18 "Validator set" row):
// mainnet, testnet-1, the local dev chain and anything unrecognised all get
// the disable list from `konstellationd init`.
func TestDefaultGenesisDisablesCreateValidatorOnEveryNetwork(t *testing.T) {
	a := testApp(t)
	createValidator := sdk.MsgTypeURL(&stakingtypes.MsgCreateValidator{})
	for _, chainID := range []string{config.ChainIDMainnet, config.ChainIDTestnet, config.ChainIDLocal, "test-chain-xyz", ""} {
		raw, ok := a.DefaultGenesis(chainID)[circuittypes.ModuleName]
		if !ok {
			t.Fatalf("%q: no circuit genesis", chainID)
		}
		var gs circuittypes.GenesisState
		a.AppCodec().MustUnmarshalJSON(raw, &gs)
		if !slices.Contains(gs.DisabledTypeUrls, createValidator) {
			t.Errorf("chain-id %q: circuit genesis %v does not disable %s", chainID, gs.DisabledTypeUrls, createValidator)
		}
	}
}

// The launch validators are gentxs, delivered by genutil's InitGenesis
// through the ante chain and the message router — both of which consult the
// circuit breaker. They pass only because the circuit module's InitGenesis,
// which writes D16's disable list, runs later. Reordering the two would
// refuse every gentx and no network could start.
func TestGenesisOrderDeliversGentxsBeforeCircuit(t *testing.T) {
	order := testApp(t).ModuleManager.OrderInitGenesis
	genutil, circuit := slices.Index(order, genutiltypes.ModuleName), slices.Index(order, circuittypes.ModuleName)
	if genutil < 0 || circuit < 0 {
		t.Fatalf("genutil (%d) or circuit (%d) missing from the init-genesis order", genutil, circuit)
	}
	if genutil > circuit {
		t.Fatalf("circuit InitGenesis (%d) runs before genutil (%d): gentxs would hit D16's disabled MsgCreateValidator", circuit, genutil)
	}
}
