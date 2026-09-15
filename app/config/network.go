package config

import (
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// NetworkProfile holds the genesis parameters that legitimately differ between
// testnet-1 and konstellation-1 (ENGINEERING.md §18). One binary serves both
// networks; this is the only place the binary's *defaults* branch on which
// network it is initialising, and only for `konstellationd init` — a running
// node reads params from its genesis.json and never consults this.
//
// Everything not in this struct is identical on every network by design:
// issuance, burn, staking, slashing, quorum/thresholds. Testnet exists to
// measure what mainnet will do, so it must run the same economics.
type NetworkProfile struct {
	// Name is what `init` prints so an operator can see which set was used.
	Name string

	// Governance timing and deposit gate. Mainnet values are D11; testnet
	// values are short so upgrade drills and param changes take an afternoon,
	// not three days (decided 2026-09-15).
	GovVotingPeriod          time.Duration
	GovExpeditedVotingPeriod time.Duration
	GovMinDeposit            sdk.Coins
	GovExpeditedMinDeposit   sdk.Coins
}

// MainnetProfile is konstellation-1. The values are the recorded decisions in
// chain.go; this struct only groups them.
var MainnetProfile = NetworkProfile{
	Name:                     "mainnet (" + ChainIDMainnet + ")",
	GovVotingPeriod:          GovVotingPeriod,
	GovExpeditedVotingPeriod: GovExpeditedVotingPeriod,
	GovMinDeposit:            GovMinDeposit,
	GovExpeditedMinDeposit:   GovExpeditedMinDeposit,
}

// TestnetProfile is testnet-1 and every dev/local chain-id.
var TestnetProfile = NetworkProfile{
	Name:                     "testnet/dev",
	GovVotingPeriod:          2 * time.Hour,
	GovExpeditedVotingPeriod: 30 * time.Minute,
	GovMinDeposit:            kash(10),
	GovExpeditedMinDeposit:   kash(50),
}

// ProfileFor picks the profile for a Cosmos chain-id. Only the exact mainnet
// chain-id gets mainnet values; testnet-1, the local dev id and anything
// unrecognised get the testnet profile. The asymmetry is deliberate: a dev net
// accidentally running 3-day governance is an inconvenience, a mainnet
// accidentally running 2-hour governance is not, so mainnet must be named.
func ProfileFor(chainID string) NetworkProfile {
	if chainID == ChainIDMainnet {
		return MainnetProfile
	}
	return TestnetProfile
}
