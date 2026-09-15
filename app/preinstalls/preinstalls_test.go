package preinstalls

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	evmtypes "github.com/cosmos/evm/x/vm/types"
)

// Canonical mainnet addresses and code hashes, as pinned in
// contracts/preinstalls/*.json. Duplicated here on purpose: the JSON carrying
// its own codeHash only proves the file is self-consistent, this proves the
// file is the one we meant to ship.
var want = map[string]struct{ addr, codeHash string }{
	"EntryPointV07":   {"0x0000000071727De22E5E9d8BAf0edAc6f37da032", "0x8db5ff695839d655407cc8490bb7a5d82337a86a6b39c3f0258aa6c3b582fc58"},
	"EntryPointV08":   {"0x4337084D9E255Ff0702461CF8895CE9E3b5Ff108", "0x44e632a24c6f2600cbd5b5b8b4c2d372359112c8b5774297f5fd0a9e64f11f86"},
	"Create2Deployer": {"0x13b0D85CcB8bf860b6b79AF3029fCA081AE9beF2", "0x2a300e3fee0eee59e0a1b184d1531c4bea54b843b28426f227d12145e8918663"},
}

func TestLoad(t *testing.T) {
	ps, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != len(Files) || len(ps) != len(want) {
		t.Fatalf("got %d preinstalls, Files=%d, want=%d", len(ps), len(Files), len(want))
	}
	for _, p := range ps {
		w, ok := want[p.Name]
		if !ok {
			t.Fatalf("unexpected preinstall %q", p.Name)
		}
		if got := common.HexToAddress(p.Address); got != common.HexToAddress(w.addr) {
			t.Errorf("%s: address %s, want %s", p.Name, got, w.addr)
		}
		if got := crypto.Keccak256Hash(common.FromHex(p.Code)); got != common.HexToHash(w.codeHash) {
			t.Errorf("%s: codeHash %s, want %s", p.Name, got, w.codeHash)
		}
	}
}

// The three must not overlap cosmos/evm's own list: a collision would be
// rejected by x/vm genesis validation, and would mean we are shipping two
// different bytecodes for one address.
func TestMergeWithDefaults(t *testing.T) {
	ours := MustLoad()
	all, err := Merge(evmtypes.DefaultPreinstalls, ours)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != len(evmtypes.DefaultPreinstalls)+len(ours) {
		t.Fatalf("merged %d, want %d", len(all), len(evmtypes.DefaultPreinstalls)+len(ours))
	}
	gs := evmtypes.DefaultGenesisState()
	gs.Preinstalls = all
	if err := gs.Validate(); err != nil {
		t.Fatalf("x/vm genesis with merged preinstalls does not validate: %v", err)
	}
}

func TestMergeRejectsCollision(t *testing.T) {
	ours := MustLoad()
	dup := evmtypes.Preinstall{Name: "dup", Address: ours[0].Address, Code: "0x00"}
	if _, err := Merge(ours, []evmtypes.Preinstall{dup}); err == nil {
		t.Fatal("expected collision error")
	}
}
