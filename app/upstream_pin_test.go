package app

import (
	"runtime/debug"
	"testing"
)

// pinnedEVM is the cosmos/evm release the checks below were verified
// against. Bumping it is deliberate (ENGINEERING.md §4.2: diff every
// upstream release); each entry names the thing to re-verify on that diff.
const pinnedEVM = "v0.7.3"

// TestUpstreamCouplingPins fails on any cosmos/evm version change so the
// upstream review cannot skip the app-level couplings that upstream is free
// to break silently.
func TestUpstreamCouplingPins(t *testing.T) {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		t.Skip("no build info")
	}
	var got string
	for _, d := range bi.Deps {
		if d.Path == "github.com/cosmos/evm" {
			got = d.Version
			if d.Replace != nil {
				got = d.Replace.Version
			}
		}
	}
	if got == "" {
		t.Fatal("cosmos/evm not in build info")
	}
	if got != pinnedEVM {
		t.Fatalf(`cosmos/evm is %s, pinned checks were done against %s. Re-verify and update pinnedEVM:

  - app/mempool.go wraps the mempool in complianceMempool, so server/start.go's
    type assertion to *evmmempool.Mempool fails and SetClientCtx is never
    called. On %s the setter stored a value nothing read. Run
    "grep -rn clientCtx $(go list -m -f '{{.Dir}}' github.com/cosmos/evm)/mempool"
    and, if a reader appeared, forward the call from the wrapper (STATUS.md §3).
  - x/compliance/ante extracts EIP-7702 authorities from SetCodeAuthorizations;
    confirm x/vm/keeper/state_transition.go still applies them the same way.
`, got, pinnedEVM, pinnedEVM)
	}
}
