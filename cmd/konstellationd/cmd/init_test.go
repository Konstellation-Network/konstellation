package cmd

import (
	"encoding/json"
	"testing"
)

// The genesis cache is keyed on chain-id: a second init in the same process
// with a different --chain-id must not be served the first run's profile.
func TestLazyGenesisRebuildsPerChainID(t *testing.T) {
	builds := 0
	l := &lazyGenesis{build: func(chainID string) map[string]json.RawMessage {
		builds++
		return map[string]json.RawMessage{"id": json.RawMessage(`"` + chainID + `"`)}
	}}

	l.chainID = "konstellation-1"
	if got := string(l.get()["id"]); got != `"konstellation-1"` {
		t.Fatalf("first build: got %s", got)
	}
	l.get()
	if builds != 1 {
		t.Fatalf("same chain-id rebuilt: %d builds", builds)
	}

	l.chainID = "testnet-1"
	if got := string(l.get()["id"]); got != `"testnet-1"` {
		t.Fatalf("after chain-id change: got %s (stale profile)", got)
	}
	if builds != 2 {
		t.Fatalf("expected rebuild on chain-id change, got %d builds", builds)
	}
}
