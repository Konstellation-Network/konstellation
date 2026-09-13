package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/spf13/cobra"

	"github.com/cosmos/cosmos-sdk/client"
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
func initCmd(mm module.BasicManager, defaultNodeHome string, defaultGenesis func() map[string]json.RawMessage) *cobra.Command {
	gen := defaultGenesis()
	overridden := make(module.BasicManager, len(mm))
	for name, b := range mm {
		if raw, ok := gen[name]; ok {
			overridden[name] = genesisOverride{AppModuleBasic: b, raw: raw}
		} else {
			overridden[name] = b
		}
	}

	cmd := genutilcli.InitCmd(overridden, defaultNodeHome)
	cmd.PreRunE = func(cmd *cobra.Command, _ []string) error {
		denom, _ := cmd.Flags().GetString(genutilcli.FlagDefaultBondDenom)
		if denom != "" && denom != config.BaseDenom {
			return fmt.Errorf("--%s must be %q: the base denom is fixed at genesis", genutilcli.FlagDefaultBondDenom, config.BaseDenom)
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
		appToml := filepath.Join(cfg.RootDir, "config", "app.toml")
		return setEVMChainID(appToml, config.EVMChainIDFor(chainID))
	}
	return cmd
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

// genesisOverride is an AppModuleBasic whose DefaultGenesis is a fixed blob.
type genesisOverride struct {
	module.AppModuleBasic
	raw json.RawMessage
}

var _ module.HasGenesisBasics = genesisOverride{}

func (g genesisOverride) DefaultGenesis(codec.JSONCodec) json.RawMessage { return g.raw }

func (g genesisOverride) ValidateGenesis(cdc codec.JSONCodec, txCfg client.TxEncodingConfig, data json.RawMessage) error {
	if v, ok := g.AppModuleBasic.(module.HasGenesisBasics); ok {
		return v.ValidateGenesis(cdc, txCfg, data)
	}
	return nil
}
