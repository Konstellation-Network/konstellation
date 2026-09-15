// Package preinstalls holds the genesis preinstall contracts Konstellation adds
// on top of cosmos/evm's DefaultPreinstalls.
//
// The bytecode is NOT authored here. `contracts/preinstalls/*.json` (org repo
// `contracts`) is the source of truth: each file is deployed bytecode pinned
// from a live mainnet `eth_getCode`, with a `codeHash` guard, and `contracts`
// CI verifies it against the live chain. The files under this directory are
// verbatim copies; keep them byte-identical with `contracts` when re-pinning.
//
// Multicall3 and Permit2 are deliberately absent — cosmos/evm ships them in
// DefaultPreinstalls at the same addresses with identical bytecode (verified in
// `contracts`). WKASH is absent by decision: it is a post-genesis deploy
// (ENGINEERING.md §6.3), not a preinstall.
package preinstalls

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	evmtypes "github.com/cosmos/evm/x/vm/types"
)

// Files are the pinned preinstalls, one JSON per contract. The list is the
// set of names, so a file dropped into the directory without being listed here
// is not silently shipped.
var Files = []string{
	"EntryPointV07.json",
	"EntryPointV08.json",
	"Create2Deployer.json",
}

//go:embed *.json
var fs embed.FS

// pinned mirrors the schema of contracts/preinstalls/*.json.
type pinned struct {
	Name     string `json:"name"`
	Address  string `json:"address"`
	Code     string `json:"code"`
	CodeHash string `json:"codeHash"`
	Source   string `json:"source"`
	Notes    string `json:"notes"`
}

// Load parses every file in Files, checks each blob against its own pinned
// codeHash and against cosmos/evm's Preinstall validation, and returns the
// entries in Files order. Any mismatch is a programming/pinning error, not a
// runtime condition, so it is returned rather than silently skipped.
func Load() ([]evmtypes.Preinstall, error) {
	out := make([]evmtypes.Preinstall, 0, len(Files))
	seen := make(map[string]string, len(Files))
	for _, name := range Files {
		raw, err := fs.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("preinstall %s: %w", name, err)
		}
		var p pinned
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("preinstall %s: %w", name, err)
		}
		if p.Name == "" || !common.IsHexAddress(p.Address) {
			return nil, fmt.Errorf("preinstall %s: missing name or invalid address %q", name, p.Address)
		}
		if !strings.HasPrefix(p.Code, "0x") {
			return nil, fmt.Errorf("preinstall %s: code must be 0x-prefixed", name)
		}
		code := common.FromHex(p.Code)
		if len(code) == 0 {
			return nil, fmt.Errorf("preinstall %s: code is empty or not hex", name)
		}
		want := common.HexToHash(p.CodeHash)
		if got := crypto.Keccak256Hash(code); !bytes.Equal(got.Bytes(), want.Bytes()) {
			return nil, fmt.Errorf("preinstall %s: codeHash mismatch: pinned %s, keccak256(code) %s", name, want, got)
		}
		addr := common.HexToAddress(p.Address)
		if prev, dup := seen[addr.Hex()]; dup {
			return nil, fmt.Errorf("preinstall %s: address %s already used by %s", name, addr, prev)
		}
		seen[addr.Hex()] = p.Name

		pi := evmtypes.Preinstall{Name: p.Name, Address: p.Address, Code: p.Code}
		if err := pi.Validate(); err != nil {
			return nil, fmt.Errorf("preinstall %s: %w", name, err)
		}
		out = append(out, pi)
	}
	return out, nil
}

// MustLoad is Load for genesis construction, where a bad pin must abort.
func MustLoad() []evmtypes.Preinstall {
	ps, err := Load()
	if err != nil {
		panic(err)
	}
	return ps
}

// Merge appends extra to base, refusing any address that base already holds.
// cosmos/evm's genesis validation would reject the duplicate too, but only at
// InitChain; failing here surfaces it at `konstellationd init`.
func Merge(base, extra []evmtypes.Preinstall) ([]evmtypes.Preinstall, error) {
	have := make(map[common.Address]string, len(base))
	for _, p := range base {
		have[common.HexToAddress(p.Address)] = p.Name
	}
	out := make([]evmtypes.Preinstall, 0, len(base)+len(extra))
	out = append(out, base...)
	for _, p := range extra {
		addr := common.HexToAddress(p.Address)
		if prev, dup := have[addr]; dup {
			return nil, fmt.Errorf("preinstall %s at %s collides with %s in the base list", p.Name, addr, prev)
		}
		have[addr] = p.Name
		out = append(out, p)
	}
	return out, nil
}

// Addresses returns the checksummed addresses of ps, sorted, for logging and
// tests.
func Addresses(ps []evmtypes.Preinstall) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, common.HexToAddress(p.Address).Hex())
	}
	sort.Strings(out)
	return out
}
