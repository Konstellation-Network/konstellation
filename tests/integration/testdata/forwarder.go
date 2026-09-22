// Package testdata holds the one contract tests/integration needs that
// cosmos/evm's test contracts do not provide.
//
// Forwarder.json is Forwarder.sol compiled with solc 0.8.37,
// evm_version = prague, optimizer on (200 runs), metadata stripped —
// the contracts repo's foundry.toml settings — and reshaped into the
// Hardhat artifact form cosmos/evm's loader reads:
//
//	forge build && jq '{_format:"hh-sol-artifact-1", contractName:"Forwarder",
//	  sourceName:"Forwarder.sol", abi:.abi, bytecode:.bytecode.object}' \
//	  out/Forwarder.sol/Forwarder.json > Forwarder.json
package testdata

import (
	_ "embed"

	contractutils "github.com/cosmos/evm/contracts/utils"
	evmtypes "github.com/cosmos/evm/x/vm/types"
)

//go:embed Forwarder.json
var forwarderJSON []byte

// LoadForwarder returns the compiled Forwarder contract.
func LoadForwarder() (evmtypes.CompiledContract, error) {
	return contractutils.ConvertHardhatBytesToCompiledContract(forwarderJSON)
}
