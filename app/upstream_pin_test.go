package app

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// pinnedEVM and pinnedSDK are the cosmos/evm and cosmos-sdk releases the
// checks below were verified against. Bumping them is deliberate
// (ENGINEERING.md §4.2: diff every upstream release); each entry names the
// thing to re-verify on that diff.
const (
	pinnedEVM = "v0.7.3"
	pinnedSDK = "v0.54.3"
)

// TestUpstreamCouplingPins fails on any cosmos/evm or cosmos-sdk version
// change so the upstream review cannot skip the app-level couplings that
// upstream is free to break silently.
func TestUpstreamCouplingPins(t *testing.T) {
	// go.mod is read directly: debug.ReadBuildInfo's Deps are not populated
	// in test binaries on every toolchain. go test runs with the package dir
	// as cwd, which also holds under -trimpath (runtime.Caller would not).
	// A replace of either module is not considered: make verify-deps rejects
	// one before the build (ENGINEERING.md §2.1).
	gomod, err := os.ReadFile("../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	var got, gotSDK string
	for _, line := range strings.Split(string(gomod), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "github.com/cosmos/evm" {
			got = f[1]
		}
		if len(f) >= 2 && f[0] == "github.com/cosmos/cosmos-sdk" {
			gotSDK = f[1]
		}
	}
	if got == "" || gotSDK == "" {
		t.Fatal("cosmos/evm or cosmos-sdk not in go.mod")
	}
	if gotSDK != pinnedSDK {
		t.Fatalf(`cosmos-sdk is %s, pinned checks were done against %s. Re-verify and update pinnedSDK:

  - contrib/x/circuit is deprecated and unmaintained upstream (ENGINEERING.md
    §7.1, D14). Confirm the package is still shipped; read
    "git log <old>..<new> -- contrib/x/circuit" for fixes; confirm baseapp
    still calls the circuit breaker from the message router
    (SetCircuitBreaker in app.go) and that IsAllowed's semantics are
    unchanged, since app/circuit.go depends on both. If the package is
    gone, vendor it into x/ per §7.1.
`, gotSDK, pinnedSDK)
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
  - app/genesis.go InertUpstreamPrecompiles: x/vm/types.AvailableStaticPrecompiles
    lists addresses upstream does not implement (v0.7.3: vesting, 0x…0803 —
    precompiles/ has no such package and DefaultStaticPrecompiles registers
    nothing there), and marking one active makes every call to it fail. Diff
    AvailableStaticPrecompiles against precompiles/types.DefaultStaticPrecompiles
    on the new tag: drop what is now implemented from InertUpstreamPrecompiles
    (activating it is a genesis/param change for running networks, not just
    code), add anything newly listed-but-unimplemented, and update
    chain-config and docs/contracts.md to match. tests/integration
    TestActivePrecompilesAreServed must keep passing.
`, got, pinnedEVM, pinnedEVM)
	}
}

// TestWorkflowsPinGoModToolchain: the CI workflows pin setup-go to an exact
// Go version because setup-go reads only go.mod's `go` line, then `go`
// downloads the `toolchain` version into the module cache a second time and
// the cache restore collides with it (2026-09-20, disk-full link failures).
// The pin must follow go.mod's `toolchain` line, in every job.
func TestWorkflowsPinGoModToolchain(t *testing.T) {
	gomod, err := os.ReadFile("../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	toolchain := regexp.MustCompile(`(?m)^toolchain go(\S+)`).FindSubmatch(gomod)
	if toolchain == nil {
		t.Fatal("go.mod has no toolchain line")
	}
	want := string(toolchain[1])
	for _, wf := range []string{"../.github/workflows/ci.yml", "../.github/workflows/vuln.yml"} {
		bz, err := os.ReadFile(wf)
		if err != nil {
			t.Fatal(err)
		}
		pins := regexp.MustCompile(`go-version:\s*"([^"]+)"`).FindAllSubmatch(bz, -1)
		if len(pins) == 0 {
			t.Errorf("%s: no setup-go go-version pin", wf)
		}
		for _, p := range pins {
			if got := string(p[1]); got != want {
				t.Errorf("%s pins go-version %q; go.mod toolchain is go%s", wf, got, want)
			}
		}
		if regexp.MustCompile(`go-version-file:`).Match(bz) {
			t.Errorf("%s: uses go-version-file, which ignores go.mod's toolchain line", wf)
		}
	}
}
