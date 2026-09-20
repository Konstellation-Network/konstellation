package app

import (
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"

	distributionprecompile "github.com/cosmos/evm/precompiles/distribution"
	govprecompile "github.com/cosmos/evm/precompiles/gov"
	ics02precompile "github.com/cosmos/evm/precompiles/ics02"
	ics20precompile "github.com/cosmos/evm/precompiles/ics20"
	slashingprecompile "github.com/cosmos/evm/precompiles/slashing"
	stakingprecompile "github.com/cosmos/evm/precompiles/staking"
	evmtypes "github.com/cosmos/evm/x/vm/types"
)

// TestPrecompileMsgTypesCoverEveryTxMethod pins precompileMsgTypes against
// the ABIs actually shipped by the pinned cosmos/evm: every transaction
// method of every guarded precompile must be classified, and nothing in
// the table may name a method that no longer exists. An unclassified
// method fails closed at runtime, but it should fail here first.
func TestPrecompileMsgTypesCoverEveryTxMethod(t *testing.T) {
	shipped := map[string]struct {
		abi  abi.ABI
		isTx func(*abi.Method) bool
	}{
		evmtypes.ICS20PrecompileAddress:        {ics20precompile.ABI, ics20precompile.Precompile{}.IsTransaction},
		evmtypes.StakingPrecompileAddress:      {stakingprecompile.ABI, stakingprecompile.Precompile{}.IsTransaction},
		evmtypes.DistributionPrecompileAddress: {distributionprecompile.ABI, distributionprecompile.Precompile{}.IsTransaction},
		evmtypes.GovPrecompileAddress:          {govprecompile.ABI, govprecompile.Precompile{}.IsTransaction},
		evmtypes.SlashingPrecompileAddress:     {slashingprecompile.ABI, slashingprecompile.Precompile{}.IsTransaction},
		evmtypes.ICS02PrecompileAddress:        {ics02precompile.ABI, ics02precompile.Precompile{}.IsTransaction},
	}
	for addr, table := range precompileMsgTypes {
		s, ok := shipped[addr]
		if !ok {
			t.Errorf("precompileMsgTypes names %s, which this test does not know", addr)
			continue
		}
		for name, m := range s.abi.Methods {
			if !s.isTx(&m) {
				continue
			}
			if _, classified := table[name]; !classified {
				t.Errorf("%s: tx method %q is not in precompileMsgTypes", addr, name)
			}
		}
		for name := range table {
			if _, exists := s.abi.Methods[name]; !exists {
				t.Errorf("%s: precompileMsgTypes names method %q, which the ABI does not have", addr, name)
			}
		}
	}
	for addr := range shipped {
		if _, ok := precompileMsgTypes[addr]; !ok {
			t.Errorf("tx-path precompile %s is not guarded", addr)
		}
	}
}
