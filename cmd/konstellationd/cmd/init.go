package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/server"
	"github.com/cosmos/cosmos-sdk/types/module"
	genutilcli "github.com/cosmos/cosmos-sdk/x/genutil/client/cli"
	genutiltypes "github.com/cosmos/cosmos-sdk/x/genutil/types"

	"github.com/Konstellation-Network/konstellation/app/config"
)

// initCmd returns the SDK `init` command driven by the chain's own genesis
// defaults. The SDK builds genesis from BasicManager.DefaultGenesis, which
// only knows module defaults ("stake" everywhere); handing it a manager whose
// modules answer with app.DefaultGenesis() means the file it writes, the
// summary it prints, and `validate-genesis` all agree.
//
// --default-denom is rejected unless it names the base denom: the denom is a
// genesis-time decision (ENGINEERING.md D2), not a per-node flag.
//
// The genesis depends on the chain-id (ENGINEERING.md §18: testnet-1 and dev
// nets get short governance timings, konstellation-1 gets D11's), and the
// SDK only resolves the chain-id inside its RunE. So the override is lazy:
// PreRunE resolves the chain-id the same way the SDK will, builds the genesis
// once, and each module's DefaultGenesis hands out its slice of it.
func initCmd(mm module.BasicManager, defaultNodeHome string, defaultGenesis func(chainID string) map[string]json.RawMessage) *cobra.Command {
	lazy := &lazyGenesis{build: defaultGenesis}
	overridden := make(module.BasicManager, len(mm))
	for name, b := range mm {
		// Only modules with genesis methods are wrapped; the wrapper claims
		// HasGenesisBasics, so it must have something to fall through to.
		// (With NewBasicManagerFromManager every module is a core adaptor
		// that satisfies this, so nothing is skipped in practice.)
		hb, ok := b.(module.HasGenesisBasics)
		if !ok {
			overridden[name] = b
			continue
		}
		overridden[name] = genesisOverride{AppModuleBasic: b, basics: hb, name: name, gen: lazy}
	}

	cmd := genutilcli.InitCmd(overridden, defaultNodeHome)
	cmd.PreRunE = func(cmd *cobra.Command, _ []string) error {
		denom, _ := cmd.Flags().GetString(genutilcli.FlagDefaultBondDenom)
		if denom != "" && denom != config.BaseDenom {
			return fmt.Errorf("--%s must be %q: the base denom is fixed at genesis", genutilcli.FlagDefaultBondDenom, config.BaseDenom)
		}
		// Same precedence as the SDK's InitCmd: --chain-id, then client.toml.
		// Empty means the SDK will pick a random test-chain-* id, which
		// ProfileFor maps to the testnet/dev profile like any unknown id.
		chainID, _ := cmd.Flags().GetString(flags.FlagChainID)
		if chainID == "" {
			chainID = client.GetClientContextFromCmd(cmd).ChainID
		}
		lazy.chainID = chainID
		cmd.PrintErrf("genesis profile: %s (chain-id %q)\n", config.ProfileFor(chainID).Name, chainID)
		// The root pre-run has already written config.toml/client.toml/app.toml
		// (from our template if absent). If app.toml pre-existed (infra
		// tooling, plain-SDK template) it must carry a numeric evm-chain-id
		// for the post-init reconcile to read and patch; check now, before
		// the SDK writes genesis.json and the validator keys.
		appToml := appTomlPath(cmd)
		if _, err := readEVMChainID(appToml); err != nil {
			return fmt.Errorf("%w; add `evm-chain-id = <n>` under [evm] in app.toml (or delete app.toml and rerun init to regenerate it)", err)
		}
		return nil
	}

	// After the SDK has written genesis.json, make app.toml's evm-chain-id
	// match the network in that genesis. The root PersistentPreRunE only
	// writes app.toml when it does not exist yet, and `config set client
	// chain-id X` (the standard join recipe) creates it before init runs.
	sdkRunE := cmd.RunE
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if err := sdkRunE(cmd, args); err != nil {
			return err
		}
		cfg := server.GetServerContextFromCmd(cmd).Config
		f, err := os.Open(cfg.GenesisFile())
		if err != nil {
			return fmt.Errorf("read genesis written by init: %w", err)
		}
		defer f.Close()
		chainID, err := genutiltypes.ParseChainIDFromGenesis(f)
		if err != nil {
			return fmt.Errorf("parse chain-id from genesis: %w", err)
		}
		return reconcileEVMChainID(cmd, appTomlPath(cmd), chainID)
	}
	return cmd
}

// reconcileEVMChainID makes app.toml's evm-chain-id consistent with the
// network in genesis:
//   - known network: force the required id;
//   - unknown network: keep a deliberately set id unless it belongs to a real
//     network (that would put a dev net in mainnet/testnet's replay domain),
//     in which case use the local id.
//
// Any change is printed so nothing is rewritten silently.
func reconcileEVMChainID(cmd *cobra.Command, appToml, chainID string) error {
	current, err := readEVMChainID(appToml)
	if err != nil {
		return err
	}
	want := current
	switch req, known := config.RequiredEVMChainID[chainID]; {
	case known:
		want = req
	case config.IsRealNetworkEVMChainID(current):
		want = config.EVMChainIDLocal
	}
	if want == current {
		return nil
	}
	if err := setEVMChainID(appToml, want); err != nil {
		return err
	}
	cmd.PrintErrf("app.toml: evm-chain-id %d -> %d for chain-id %q\n", current, want, chainID)
	return nil
}

var evmChainIDValue = regexp.MustCompile(`(?m)^evm-chain-id\s*=\s*"?(\d+)"?`)

func readEVMChainID(appToml string) (uint64, error) {
	b, err := os.ReadFile(appToml)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", appToml, err)
	}
	m := evmChainIDValue.FindSubmatch(b)
	if m == nil {
		return 0, fmt.Errorf("%s has no parseable evm-chain-id line", appToml)
	}
	return strconv.ParseUint(string(m[1]), 10, 64)
}

func appTomlPath(cmd *cobra.Command) string {
	root := server.GetServerContextFromCmd(cmd).Config.RootDir
	return filepath.Clean(filepath.Join(root, "config", "app.toml"))
}

var evmChainIDLine = regexp.MustCompile(`(?m)^evm-chain-id\s*=.*$`)

// setEVMChainID rewrites the [evm] evm-chain-id line of app.toml in place,
// leaving every other setting untouched.
func setEVMChainID(appToml string, id uint64) error {
	b, err := os.ReadFile(appToml)
	if err != nil {
		return fmt.Errorf("read %s: %w", appToml, err)
	}
	if !evmChainIDLine.Match(b) {
		return fmt.Errorf("%s has no evm-chain-id line to update", appToml)
	}
	out := evmChainIDLine.ReplaceAll(b, []byte(fmt.Sprintf("evm-chain-id = %d", id)))
	// appToml is derived from the node's own --home, not user-supplied content.
	if err := os.WriteFile(appToml, out, 0o600); err != nil { //nolint:gosec // G703: path is <home>/config/app.toml
		return fmt.Errorf("write %s: %w", appToml, err)
	}
	return nil
}

// lazyGenesis builds app.DefaultGenesis for the chain-id PreRunE resolved,
// the first time any module asks for its default. The build is cached per
// chain-id, not per process: the command tree outlives a single run (tests,
// in-process reuse of the root command), and a second `init` with another
// --chain-id must get its own profile rather than the first run's.
type lazyGenesis struct {
	build    func(chainID string) map[string]json.RawMessage
	chainID  string
	builtFor string
	gen      map[string]json.RawMessage
}

func (l *lazyGenesis) get() map[string]json.RawMessage {
	if l.gen == nil || l.builtFor != l.chainID {
		l.gen = l.build(l.chainID)
		l.builtFor = l.chainID
	}
	return l.gen
}

// genesisOverride is an AppModuleBasic whose DefaultGenesis is the module's
// slice of the app-level genesis; modules the app does not customise fall
// through to their own default.
type genesisOverride struct {
	module.AppModuleBasic
	basics module.HasGenesisBasics
	name   string
	gen    *lazyGenesis
}

var _ module.HasGenesisBasics = genesisOverride{}

func (g genesisOverride) DefaultGenesis(cdc codec.JSONCodec) json.RawMessage {
	if raw, ok := g.gen.get()[g.name]; ok {
		return raw
	}
	return g.basics.DefaultGenesis(cdc)
}

func (g genesisOverride) ValidateGenesis(cdc codec.JSONCodec, txCfg client.TxEncodingConfig, data json.RawMessage) error {
	return g.basics.ValidateGenesis(cdc, txCfg, data)
}
