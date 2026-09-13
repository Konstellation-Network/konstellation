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

	// EIP-155 ids (D1). All three verified absent from ethereum-lists/chains
	// on 2026-09-13. Local dev nets get their own id so a tx signed for a dev
	// chain can never replay on testnet-1.
	EVMChainIDMainnet uint64 = 5667
	EVMChainIDTestnet uint64 = 56671
	EVMChainIDLocal   uint64 = 56670
	// DefaultEVMChainID is what `konstellationd init` writes to app.toml for a
	// Cosmos chain-id it does not recognise (see EVMChainIDFor).
	DefaultEVMChainID = EVMChainIDLocal

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
	ChainIDLocal   = "konstellation-local-1"
)

// RequiredEVMChainID maps a Cosmos chain-id to the EIP-155 id it must run
// with. The EVM id lives in app.toml (per node), the Cosmos id in genesis
// (per network); a node with the two out of step signs blocks nobody else
// accepts. Chain-ids not listed here are unconstrained.
var RequiredEVMChainID = map[string]uint64{
	ChainIDMainnet: EVMChainIDMainnet,
	ChainIDTestnet: EVMChainIDTestnet,
	ChainIDLocal:   EVMChainIDLocal,
}

// EVMChainIDFor returns the EIP-155 id `init` should write to app.toml for
// the given Cosmos chain-id: the required one for known networks, otherwise
// the local dev id.
func EVMChainIDFor(cosmosChainID string) uint64 {
	if id, ok := RequiredEVMChainID[cosmosChainID]; ok {
		return id
	}
	return DefaultEVMChainID
}

// realNetworkEVMChainIDs are the replay domains of networks that hold (or will
// hold) real value. No chain-id other than the one that owns it may run with
// one of these, whatever app.toml says.
var realNetworkEVMChainIDs = map[uint64]string{
	EVMChainIDMainnet: ChainIDMainnet,
	EVMChainIDTestnet: ChainIDTestnet,
}

// ValidateEVMChainID enforces the pairing in both directions:
//   - a known chain-id must run with exactly the EVM id it requires;
//   - an unknown chain-id (dev/staging nets) must not run with a real
//     network's EVM id, or a tx signed there replays on that network.
func ValidateEVMChainID(cosmosChainID string, evmChainID uint64) error {
	if want, known := RequiredEVMChainID[cosmosChainID]; known {
		if evmChainID != want {
			return fmt.Errorf(
				"genesis chain-id %q requires evm-chain-id %d, app.toml has %d: set [evm] evm-chain-id = %d in app.toml before starting",
				cosmosChainID, want, evmChainID, want,
			)
		}
		return nil
	}
	if owner, reserved := realNetworkEVMChainIDs[evmChainID]; reserved {
		return fmt.Errorf(
			"genesis chain-id %q is not %s but app.toml has evm-chain-id %d, which belongs to %s: a tx signed here would replay there; use %d (local) or another unreserved id",
			cosmosChainID, owner, evmChainID, owner, EVMChainIDLocal,
		)
	}
	return nil
}

// IsRealNetworkEVMChainID reports whether id belongs to mainnet or testnet.
func IsRealNetworkEVMChainID(id uint64) bool {
	_, ok := realNetworkEVMChainIDs[id]
	return ok
}
