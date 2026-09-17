package app

import (
	"os"
	"strings"
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
	// go.mod is read directly: debug.ReadBuildInfo's Deps are not populated
	// in test binaries on every toolchain. go test runs with the package dir
	// as cwd, which also holds under -trimpath (runtime.Caller would not).
	// A replace of cosmos/evm is not considered: make verify-deps rejects
	// one before the build (ENGINEERING.md §2.1).
	gomod, err := os.ReadFile("../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	var got string
	for _, line := range strings.Split(string(gomod), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "github.com/cosmos/evm" {
			got = f[1]
		}
	}
	if got == "" {
		t.Fatal("cosmos/evm not in go.mod")
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
