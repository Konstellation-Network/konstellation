package app

import (
	"encoding/json"

	erc20types "github.com/cosmos/evm/x/erc20/types"
	feemarkettypes "github.com/cosmos/evm/x/feemarket/types"
	evmtypes "github.com/cosmos/evm/x/vm/types"

	minttypes "github.com/cosmos/cosmos-sdk/x/mint/types"

	"github.com/Konstellation-Network/konstellation/app/config"
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
// x/precisebank is not involved), enables all static precompiles, and includes
// the upstream default preinstalls.
func NewEVMGenesisState() *evmtypes.GenesisState {
	evmGenState := evmtypes.DefaultGenesisState()
	evmGenState.Params.EvmDenom = config.BaseDenom
	evmGenState.Params.ExtendedDenomOptions = &evmtypes.ExtendedDenomOptions{ExtendedDenom: config.BaseDenom}
	evmGenState.Params.ActiveStaticPrecompiles = evmtypes.AvailableStaticPrecompiles
	evmGenState.Preinstalls = evmtypes.DefaultPreinstalls

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
// Emission model is an open decision (ENGINEERING.md D4); SDK default curve
// until it is made, denominated in the base denom.
func NewMintGenesisState() *minttypes.GenesisState {
	mintGenState := minttypes.DefaultGenesisState()
	mintGenState.Params.MintDenom = config.BaseDenom

	return mintGenState
}

// NewFeeMarketGenesisState returns the default genesis state for the feemarket module.
//
// EIP-1559 base fee stays ENABLED (upstream evmd disables it for its example
// chain). Base fee disposition — distribute vs burn — is open decision D5.
func NewFeeMarketGenesisState() *feemarkettypes.GenesisState {
	return feemarkettypes.DefaultGenesisState()
}
