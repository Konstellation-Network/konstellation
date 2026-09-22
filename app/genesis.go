package app

import (
	"encoding/json"
	"slices"

	erc20types "github.com/cosmos/evm/x/erc20/types"
	feemarkettypes "github.com/cosmos/evm/x/feemarket/types"
	evmtypes "github.com/cosmos/evm/x/vm/types"

	"cosmossdk.io/math"

	circuittypes "github.com/cosmos/cosmos-sdk/contrib/x/circuit/types"
	minttypes "github.com/cosmos/cosmos-sdk/x/mint/types"

	"github.com/Konstellation-Network/konstellation/app/config"
	"github.com/Konstellation-Network/konstellation/app/preinstalls"
	complianceprecompile "github.com/Konstellation-Network/konstellation/x/compliance/precompile"
)

// GenesisState of the blockchain is represented here as a map of raw json
// messages key'd by an identifier string.
// The identifier is used to determine which module genesis information belongs
// to so it may be appropriately routed during init chain.
// Within this application default genesis information is retrieved from
// the ModuleBasicManager which populates json from each BasicModule
// object provided to it during init.
type GenesisState map[string]json.RawMessage

// NewEVMGenesisState returns the default genesis state for the EVM module.
//
// Sets the base denom (native 18 decimals, so extended denom == base denom and
// x/precisebank is not involved), enables all static precompiles (cosmos/evm's
// plus the compliance precompile at 0x…0900), and installs
// the upstream default preinstalls (Create2 factory, Multicall3, Permit2, Safe
// singleton factory, EIP-2935) plus Konstellation's own: ERC-4337 EntryPoint
// v0.7 and v0.8 (each with the SenderCreator its bytecode hard-references) and
// the hardhat-deploy/Defender Create2Deployer, all at their canonical mainnet
// addresses (ENGINEERING.md §6.3), bytecode pinned in `contracts` and
// verified against its codeHash on load.
func NewEVMGenesisState() *evmtypes.GenesisState {
	evmGenState := evmtypes.DefaultGenesisState()
	evmGenState.Params.EvmDenom = config.BaseDenom
	evmGenState.Params.ExtendedDenomOptions = &evmtypes.ExtendedDenomOptions{ExtendedDenom: config.BaseDenom}
	// cosmos/evm's static precompiles plus Konstellation's compliance
	// precompile (D6). x/vm requires the list sorted.
	active := append(slices.Clone(evmtypes.AvailableStaticPrecompiles), complianceprecompile.Address)
	slices.Sort(active)
	evmGenState.Params.ActiveStaticPrecompiles = active

	all, err := preinstalls.Merge(evmtypes.DefaultPreinstalls, preinstalls.MustLoad())
	if err != nil {
		panic(err)
	}
	evmGenState.Preinstalls = all

	return evmGenState
}

// NewErc20GenesisState returns the default genesis state for the ERC20 module.
//
// Registers the base denom as a token pair backed by the WKASH native precompile.
func NewErc20GenesisState() *erc20types.GenesisState {
	erc20GenState := erc20types.DefaultGenesisState()
	erc20GenState.TokenPairs = config.NativeTokenPair
	erc20GenState.NativePrecompiles = []string{config.WKASHPrecompile}

	return erc20GenState
}

// NewMintGenesisState returns the default genesis state for the mint module.
//
// Emission is D4's √bonded curve, implemented as the keeper's MintFn
// (app/issuance.go), so the SDK's bonded-ratio-targeting params are inert:
// InflationRateChange / InflationMin / InflationMax are zeroed and GoalBonded
// is 1 (the validator requires it non-zero) so nobody reads a 7–20 % band or a
// 67 % target out of genesis and thinks it is enforced. What does matter:
// MintDenom, BlocksPerYear (per-block provision), MaxSupply (0 = uncapped).
// The Minter's initial Inflation is 0; the MintFn overwrites it on block 1.
func NewMintGenesisState() *minttypes.GenesisState {
	mintGenState := minttypes.DefaultGenesisState()
	mintGenState.Params.MintDenom = config.BaseDenom
	mintGenState.Params.InflationRateChange = math.LegacyZeroDec()
	mintGenState.Params.InflationMin = math.LegacyZeroDec()
	mintGenState.Params.InflationMax = math.LegacyZeroDec()
	mintGenState.Params.GoalBonded = math.LegacyOneDec()
	mintGenState.Params.BlocksPerYear = config.MintBlocksPerYear
	mintGenState.Minter = minttypes.InitialMinter(math.LegacyZeroDec())

	return mintGenState
}

// NewFeeMarketGenesisState returns the default genesis state for the feemarket module.
//
// EIP-1559 base fee stays ENABLED (upstream evmd disables it for its example
// chain). The base fee is burned (D5, decided 2026-09-15; app/feeburn.go).
// MinGasMultiplier (decided 2026-09-14) and MinGasPrice (decided 2026-09-15)
// are both kept at the cosmos/evm default, set explicitly as recorded
// decisions — see config.FeeMarketMinGasMultiplier / FeeMarketMinGasPrice.
func NewFeeMarketGenesisState() *feemarkettypes.GenesisState {
	feeMarketGenState := feemarkettypes.DefaultGenesisState()
	feeMarketGenState.Params.MinGasMultiplier = config.FeeMarketMinGasMultiplier
	feeMarketGenState.Params.MinGasPrice = config.FeeMarketMinGasPrice

	return feeMarketGenState
}

// NewCircuitGenesisState returns the default genesis state for the circuit
// breaker (ENGINEERING.md §13.1, D14): no permissions — the super admin (the
// 3-of-5 operations multisig on a real network, the validator key on a dev
// chain) is written into networks/<net>/genesis.json, never into code — and
// D16's disable list, so MsgCreateValidator is refused from block 1 on every
// network (config.CircuitDisabledTypeURLs). Nothing protected can be listed:
// app/circuit.go ignores the breaker's own and governance's messages whatever
// the list says, so this can never weld the reset shut.
func NewCircuitGenesisState() *circuittypes.GenesisState {
	gs := circuittypes.DefaultGenesisState()
	gs.DisabledTypeUrls = slices.Clone(config.CircuitDisabledTypeURLs)
	return gs
}
