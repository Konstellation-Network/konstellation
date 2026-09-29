package config

import "testing"

// stagingChainID is an unknown network that must never borrow a real one's
// replay domain.
const stagingChainID = "konstellation-staging-1"

func TestValidateEVMChainID(t *testing.T) {
	cases := []struct {
		name    string
		chainID string
		evmID   uint64
		wantErr bool
	}{
		{"mainnet correct", ChainIDMainnet, EVMChainIDMainnet, false},
		{"mainnet with default (local) id", ChainIDMainnet, DefaultEVMChainID, true},
		{"testnet correct", ChainIDTestnet, EVMChainIDTestnet, false},
		{"testnet with mainnet id", ChainIDTestnet, EVMChainIDMainnet, true},
		{"local correct", ChainIDLocal, EVMChainIDLocal, false},
		{"local with testnet id", ChainIDLocal, EVMChainIDTestnet, true},
		{"devnet correct", ChainIDDevnet, EVMChainIDDevnet, false},
		{"devnet with local id", ChainIDDevnet, EVMChainIDLocal, true},
		{"testnet with devnet id", ChainIDTestnet, EVMChainIDDevnet, true},
		{"unknown network with its own id", "some-other-net", 424242, false},
		{"unknown network with local id", "some-other-net", EVMChainIDLocal, false},
		{"unknown network with MAINNET id (replay domain)", stagingChainID, EVMChainIDMainnet, true},
		{"unknown network with TESTNET id (replay domain)", stagingChainID, EVMChainIDTestnet, true},
		{"unknown network with DEVNET id (replay domain)", stagingChainID, EVMChainIDDevnet, true},
		{"empty chain-id with mainnet id", "", EVMChainIDMainnet, true},
		{"empty chain-id with local id", "", EVMChainIDLocal, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateEVMChainID(tc.chainID, tc.evmID)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateEVMChainID(%q, %d) err=%v, wantErr=%v", tc.chainID, tc.evmID, err, tc.wantErr)
			}
		})
	}
}

// Decided 2026-09-15 (TOKENOMICS.md §5). Changing these is a re-decision:
// update TOKENOMICS.md in the same change.
func TestGovDepositsAreWholeTokens(t *testing.T) {
	one := kash(1)[0].Amount
	if got := GovMinDeposit[0].Amount.Quo(one).Int64(); got != 1_000 {
		t.Fatalf("min deposit = %d KASH, want 1000", got)
	}
	if got := GovExpeditedMinDeposit[0].Amount.Quo(one).Int64(); got != 5_000 {
		t.Fatalf("expedited min deposit = %d KASH, want 5000", got)
	}
	if !GovBurnVoteVeto || GovBurnVoteQuorum || GovBurnProposalDepositPrevote {
		t.Fatal("deposits must be refundable on every outcome except veto")
	}
	if GovMinDeposit[0].Denom != BaseDenom {
		t.Fatalf("denom = %q, want %q", GovMinDeposit[0].Denom, BaseDenom)
	}
}

func TestEVMChainIDFor(t *testing.T) {
	if got := EVMChainIDFor(ChainIDMainnet); got != EVMChainIDMainnet {
		t.Fatalf("mainnet: got %d", got)
	}
	if got := EVMChainIDFor(ChainIDTestnet); got != EVMChainIDTestnet {
		t.Fatalf("testnet: got %d", got)
	}
	if got := EVMChainIDFor(ChainIDDevnet); got != EVMChainIDDevnet {
		t.Fatalf("devnet: got %d", got)
	}
	// unknown networks must never inherit a real network's replay domain
	if got := EVMChainIDFor("anything-else"); got != EVMChainIDLocal {
		t.Fatalf("unknown: got %d, want local %d", got, EVMChainIDLocal)
	}
	for id := range realNetworkEVMChainIDs {
		if EVMChainIDLocal == id {
			t.Fatal("local id collides with a real network")
		}
	}
}

// ENGINEERING.md §18: only the exact mainnet chain-id gets mainnet governance
// timings; everything else — testnet-1, devnet-1, local, unknown, empty — is the short
// testnet/dev profile. Mainnet must be named, never defaulted into.
func TestProfileFor(t *testing.T) {
	if got := ProfileFor(ChainIDMainnet); got.Name != MainnetProfile.Name {
		t.Fatalf("mainnet chain-id got profile %q", got.Name)
	}
	for _, id := range []string{ChainIDTestnet, ChainIDDevnet, ChainIDLocal, "test-chain-abc123", "konstellation-2", ""} {
		if got := ProfileFor(id); got.Name != TestnetProfile.Name {
			t.Errorf("chain-id %q got profile %q, want testnet/dev", id, got.Name)
		}
	}
	m, tn := MainnetProfile, TestnetProfile
	if m.GovVotingPeriod != GovVotingPeriod || m.GovExpeditedVotingPeriod != GovExpeditedVotingPeriod ||
		!m.GovMinDeposit.Equal(GovMinDeposit) || !m.GovExpeditedMinDeposit.Equal(GovExpeditedMinDeposit) {
		t.Fatal("mainnet profile must be exactly the D11 constants")
	}
	if tn.GovVotingPeriod >= m.GovVotingPeriod || tn.GovExpeditedVotingPeriod >= m.GovExpeditedVotingPeriod {
		t.Fatal("testnet governance must be faster than mainnet")
	}
	if !tn.GovMinDeposit.IsAllLTE(m.GovMinDeposit) {
		t.Fatal("testnet deposit must not exceed mainnet")
	}
}
