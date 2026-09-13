package config

import (
	"fmt"

	erc20types "github.com/cosmos/evm/x/erc20/types"
	evmtypes "github.com/cosmos/evm/x/vm/types"

	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
)

// Chain identity. Every value here is recorded as a decision in ENGINEERING.md
// §1 / §11 and is NOT changeable after genesis.
const (
	// BaseDenom is the on-chain base unit (18 decimals): 1 KASH = 10^18 esp.
	BaseDenom = "esp"
	// DisplayDenom is the human-readable unit.
	DisplayDenom = "kash"
	// Symbol is the ticker shown by wallets and explorers.
	Symbol = "KASH"
	// Decimals: cosmos/evm supports 18-decimal gas tokens only (§3).
	Decimals = evmtypes.EighteenDecimals

	// EVMChainIDMainnet / EVMChainIDTestnet are the EIP-155 ids (D1).
	// Both were verified absent from ethereum-lists/chains on 2026-09-13.
	EVMChainIDMainnet uint64 = 5667
	EVMChainIDTestnet uint64 = 56671
	// DefaultEVMChainID is written into app.toml by `konstellationd init`.
	// Mainnet operators MUST override it to EVMChainIDMainnet.
	DefaultEVMChainID = EVMChainIDTestnet

	// WKASHPrecompile is the address of the werc20 native precompile that wraps
	// the base denom. Kept at the upstream default so wallet/tooling assumptions
	// carry over; it is a precompile, not deployed bytecode.
	WKASHPrecompile = "0xD4949664cD82660AaE99bEdc034a0deA8A0bd517"
)

// NativeTokenPair registers the base denom with the erc20 module so it is
// reachable from Solidity through the WKASH precompile.
var NativeTokenPair = []erc20types.TokenPair{{
	Erc20Address:  WKASHPrecompile,
	Denom:         BaseDenom,
	Enabled:       true,
	ContractOwner: erc20types.OWNER_MODULE,
}}

// RegisterDenoms sets the display/base denom units in the SDK global registry
// so CLI amounts like "1kash" resolve to base units.
func RegisterDenoms() {
	if err := sdk.RegisterDenom(DisplayDenom, math.LegacyOneDec()); err != nil {
		panic(err)
	}
	if err := sdk.RegisterDenom(BaseDenom, math.LegacyNewDecWithPrec(1, int64(Decimals))); err != nil {
		panic(err)
	}
}

// DenomMetadata is the bank metadata that lets wallets and explorers render
// esp amounts as KASH.
func DenomMetadata() banktypes.Metadata {
	return banktypes.Metadata{
		Description: "The native token of Konstellation.",
		DenomUnits: []*banktypes.DenomUnit{
			{Denom: BaseDenom, Exponent: 0},
			{Denom: DisplayDenom, Exponent: uint32(Decimals)},
		},
		Base:    BaseDenom,
		Display: DisplayDenom,
		Name:    "Konstellation",
		Symbol:  Symbol,
	}
}

// Governance deposits (ENGINEERING.md D11, decided 2026-09-13): the SDK's
// default 10 / 50 tokens, expressed in 18-decimal base units.
var (
	GovMinDeposit          = kash(10)
	GovExpeditedMinDeposit = kash(50)
)

// kash converts a whole-KASH amount to base units (esp).
func kash(n int64) sdk.Coins {
	one := math.NewIntWithDecimal(1, int(Decimals))
	return sdk.NewCoins(sdk.NewCoin(BaseDenom, one.MulRaw(n)))
}

// Cosmos chain-id strings (ENGINEERING.md §1).
const (
	ChainIDMainnet = "konstellation-1"
	ChainIDTestnet = "testnet-1"
)

// RequiredEVMChainID maps a Cosmos chain-id to the EIP-155 id it must run
// with. The EVM id lives in app.toml (per node), the Cosmos id in genesis
// (per network); a node with the two out of step signs blocks nobody else
// accepts. Unknown chain-ids (local dev nets) are unconstrained.
var RequiredEVMChainID = map[string]uint64{
	ChainIDMainnet: EVMChainIDMainnet,
	ChainIDTestnet: EVMChainIDTestnet,
}

// ValidateEVMChainID returns an error if cosmosChainID is a known network and
// evmChainID is not the one it requires.
func ValidateEVMChainID(cosmosChainID string, evmChainID uint64) error {
	want, known := RequiredEVMChainID[cosmosChainID]
	if known && evmChainID != want {
		return fmt.Errorf(
			"chain-id %q requires evm-chain-id %d, app.toml has %d: fix [evm] evm-chain-id before starting",
			cosmosChainID, want, evmChainID,
		)
	}
	return nil
}
