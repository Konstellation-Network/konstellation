package config

import (
	"fmt"
	"time"

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

// Governance deposits (ENGINEERING.md D11; raised 2026-09-15 from the SDK
// default 10 / 50 once the 1 B KASH genesis supply was assumed): a spam bond,
// refunded on every outcome except veto, so the level is about who can afford
// to *propose*, not a cost. 1 000 KASH is 1e-6 of supply — comfortably inside
// what a serious proposer holds, far above what a spammer wants to lock for
// up to 5 days per proposal. See TOKENOMICS.md §5.
var (
	GovMinDeposit          = kash(1_000)
	GovExpeditedMinDeposit = kash(5_000)
)

// Governance deposit burn rules (decided 2026-09-15): burn only on veto. These
// are the SDK defaults, pinned so the "refundable unless vetoed" promise in
// TOKENOMICS.md is a recorded decision and not an incidental default.
const (
	GovBurnVoteVeto               = true
	GovBurnVoteQuorum             = false
	GovBurnProposalDepositPrevote = false
)

// DistributionCommunityTax (decided 2026-09-15): 2 % of every block's issuance
// and tips goes to the community pool, spendable only by governance. The SDK
// default, kept — it funds audits and grants without a treasury allocation,
// and is a gov param if that changes. TOKENOMICS.md §2.1.
var DistributionCommunityTax = math.LegacyNewDecWithPrec(2, 2)

// FeeMarketMinGasPrice (decided 2026-09-15): 0, the cosmos/evm default. The
// EIP-1559 base fee is allowed to decay toward zero on an idle chain; there is
// no protocol floor under it. A floor can be introduced later by gov param
// without an upgrade. TOKENOMICS.md §3.
var FeeMarketMinGasPrice = math.LegacyZeroDec()

// Governance voting period, quorum and threshold (ENGINEERING.md D11, decided
// 2026-09-14): 3 days at launch, per §11's "3-5 day, lengthen as the set
// decentralises" recommendation — the short end, since a small self-run
// validator set can review and react fast. Quorum and threshold are the SDK/
// Cosmos Hub defaults (33.4% / 50%), set explicitly here so the value is a
// recorded decision rather than an incidental default.
var (
	GovVotingPeriod = 3 * 24 * time.Hour
	GovQuorum       = "0.334"
	GovThreshold    = "0.5"
)

// FeeMarketMinGasMultiplier (decided 2026-09-14): kept at the cosmos/evm
// default (0.5 / 50%). Not itself a D10 or D11 item despite STATUS.md flagging
// it alongside them — it's an anti-manipulation floor on the feemarket
// module's recorded per-block gasWanted (used to update the EIP-1559 base
// fee), not a per-tx charge: at the end of each block,
// gasWanted = max(gasWanted * MinGasMultiplier, gasUsed), which stops a block
// proposer from reporting a high gasWanted with artificially low gasUsed to
// manipulate the base-fee adjustment. No Konstellation-specific reason to
// deviate from the upstream default.
var FeeMarketMinGasMultiplier = math.LegacyNewDecWithPrec(50, 2)

// MintIssuanceFactor (ENGINEERING.md D4; value decided 2026-09-15) is F in
//
//	annual issuance (KASH) = F × √(bonded KASH)
//
// so staking yield = F ÷ √(bonded KASH). Like Ethereum's BASE_REWARD_FACTOR
// it is a protocol constant, changed only by a coordinated upgrade, not a gov
// param. See app/issuance.go.
//
// F = 1265 was chosen against a 1,000,000,000 KASH genesis supply. On that
// supply the curve gives, by bonded share:
//
//	bonded   10 %  (100 M): 12.65 % APR, 12.65 M KASH/yr (1.27 % of supply)
//	bonded   25 %  (250 M):  8.00 % APR, 20.0  M KASH/yr (2.0  % of supply)
//	bonded   50 %  (500 M):  5.66 % APR, 28.3  M KASH/yr (2.8  % of supply)
//	bonded  100 %  (  1 B):  4.00 % APR, 40.0  M KASH/yr (4.0  % of supply)
//
// before the D5 base-fee burn, which nets against it.
var MintIssuanceFactor = math.LegacyNewDec(1265)

// MintBlocksPerYear feeds x/mint's per-block provision
// (AnnualProvisions ÷ BlocksPerYear). It is a gov param and must track real
// block time; set here for a ~1.5 s CometBFT block (60·60·8766 ÷ 1.5).
// TODO(D4): re-derive from observed testnet-1 block time before mainnet.
const MintBlocksPerYear uint64 = 60 * 60 * 8766 * 2 / 3

// kash converts a whole-KASH amount to base units (esp).
func kash(n int64) sdk.Coins {
	one := math.NewIntWithDecimal(1, int(Decimals))
	return sdk.NewCoins(sdk.NewCoin(BaseDenom, one.MulRaw(n)))
}

// Staking params (ENGINEERING.md D10, decided 2026-09-14): DPoS with a capped
// active set, not open validation. UnbondingTime matches the Cosmos Hub
// convention (SDK default); MaxValidators and MinCommissionRate are explicit
// Konstellation overrides of the SDK defaults (100 validators, 0% floor).
const (
	StakingUnbondingTime = 21 * 24 * time.Hour
	StakingMaxValidators = 30
)

// StakingMinCommissionRate is the chain-wide floor on validator commission:
// no validator may advertise less, so delegation can't race to the bottom on
// subsidized 0% offers.
var StakingMinCommissionRate = math.LegacyNewDecWithPrec(5, 2) // 5%

// Slashing params (ENGINEERING.md D10, decided 2026-09-14).
// SlashFractionDoubleSign matches the SDK default (5%) but is set explicitly
// here so the value is a recorded decision, not an incidental default.
// SlashFractionDowntime is lowered from the SDK default (1%) to 0.01%:
// downtime is treated as an operational mistake, not an attack.
var (
	SlashFractionDoubleSign = math.LegacyNewDecWithPrec(5, 2)
	SlashFractionDowntime   = math.LegacyNewDecWithPrec(1, 4)
)

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
