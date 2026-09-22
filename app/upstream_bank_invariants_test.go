package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The D6 bank-level binding (x/compliance/keeper/restriction.go) rests on
// facts about upstream code that an upstream bump could change silently.
// This test reads the pinned modules out of the module cache and checks
// each fact, so a bump that breaks one fails here rather than on a chain.
//
//  1. x/vm writes bank balances through exactly one path, statedb.go's
//     SetBalanceWithLocked → bankWrapper.SetBalance → UncheckedSetBalance
//     (ENGINEERING.md §4.1.1): EVMBankKeeper guards UncheckedSetBalance and
//     nothing else.
//  2. x/bank applies its send restriction in SendCoins, InputOutputCoins,
//     SendCoinsToVirtual and SendCoinsFromVirtual: SendRestriction sees
//     every send, module-to-account ones included (they call SendCoins).
//  3. x/bank's DelegateCoins / UndelegateCoins do not apply it: that is why
//     StakingBankKeeper exists, and why unbonding completion is exempt "by
//     construction".
func TestUpstreamBankWriteInvariants(t *testing.T) {
	evmDir := moduleDir(t, "github.com/cosmos/evm")
	sdkDir := moduleDir(t, "github.com/cosmos/cosmos-sdk")

	// 1. x/vm: one bank write, in the wrapper, via UncheckedSetBalance.
	statedb := read(t, evmDir, "x/vm/keeper/statedb.go")
	if n := strings.Count(statedb, "k.bankWrapper.SetBalance("); n != 1 {
		t.Errorf("x/vm/keeper/statedb.go calls bankWrapper.SetBalance %d times, want 1 (SetBalanceWithLocked)", n)
	}
	if strings.Contains(statedb, "UncheckedSetBalance") {
		t.Error("x/vm/keeper/statedb.go writes balances directly; EVMBankKeeper only guards the wrapper")
	}
	for _, f := range goFiles(t, filepath.Join(evmDir, "x/vm/keeper")) {
		if filepath.Base(f) == "statedb.go" {
			continue
		}
		if src := read(t, f); strings.Contains(src, "bankWrapper.SetBalance(") || strings.Contains(src, "UncheckedSetBalance(") {
			t.Errorf("%s: a second x/vm balance write appeared; extend EVMBankKeeper or the §4.1.1 trace", f)
		}
	}
	wrapper := read(t, evmDir, "x/vm/wrappers/bank.go")
	if !funcBody(t, wrapper, "SetBalance", "UncheckedSetBalance(") {
		t.Error("x/vm/wrappers/bank.go: BankWrapper.SetBalance no longer calls UncheckedSetBalance")
	}
	for _, fn := range []string{"SendCoinsFromAccountToModule", "SendCoinsFromModuleToAccount"} {
		if funcBody(t, wrapper, fn, "UncheckedSetBalance") {
			t.Errorf("x/vm/wrappers/bank.go: %s now writes balances directly, around the send restriction", fn)
		}
	}

	// 2. x/bank: the restriction is applied in every send entry point.
	for file, fns := range map[string][]string{
		"x/bank/keeper/send.go":    {"SendCoins", "InputOutputCoins"},
		"x/bank/keeper/virtual.go": {"SendCoinsToVirtual", "SendCoinsFromVirtual"},
	} {
		src := read(t, sdkDir, file)
		for _, fn := range fns {
			if !funcBody(t, src, fn, "sendRestriction.apply(") {
				t.Errorf("%s: %s no longer applies the send restriction", file, fn)
			}
		}
	}
	// ...and module-to-account sends go through SendCoins.
	keeper := read(t, sdkDir, "x/bank/keeper/keeper.go")
	for _, fn := range []string{"SendCoinsFromModuleToAccount", "SendCoinsFromAccountToModule", "SendCoinsFromModuleToModule"} {
		if !funcBody(t, keeper, fn, "k.SendCoins(") {
			t.Errorf("x/bank/keeper/keeper.go: %s no longer routes through SendCoins", fn)
		}
	}

	// 3. x/bank: delegation moves bypass it (documented, relied on).
	for _, fn := range []string{"DelegateCoins", "UndelegateCoins"} {
		if funcBody(t, keeper, fn, "sendRestriction") {
			t.Errorf("x/bank/keeper/keeper.go: %s now applies the send restriction; StakingBankKeeper and the unbonding exemption note in restriction.go need revisiting", fn)
		}
	}
}

func moduleDir(t *testing.T, module string) string {
	t.Helper()
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", module).Output()
	if err != nil {
		t.Fatalf("go list -m %s: %v", module, err)
	}
	return strings.TrimSpace(string(out))
}

func read(t *testing.T, parts ...string) string {
	t.Helper()
	bz, err := os.ReadFile(filepath.Join(parts...))
	if err != nil {
		t.Fatal(err)
	}
	return string(bz)
}

func goFiles(t *testing.T, dir string) []string {
	t.Helper()
	all, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range all {
		if !strings.HasSuffix(f, "_test.go") {
			out = append(out, f)
		}
	}
	return out
}

// funcBody reports whether the body of the method named fn (any receiver)
// in src contains needle. Bodies are taken up to the next top-level func.
func funcBody(t *testing.T, src, fn, needle string) bool {
	t.Helper()
	re := regexp.MustCompile(`(?s)\nfunc \([^)]*\) ` + regexp.QuoteMeta(fn) + `\(.*?\n}\n`)
	body := re.FindString(src)
	if body == "" {
		t.Fatalf("function %s not found", fn)
	}
	return strings.Contains(body, needle)
}
