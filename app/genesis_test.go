package app

import (
	"slices"
	"testing"

	cosmosevmutils "github.com/cosmos/evm/utils"

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
