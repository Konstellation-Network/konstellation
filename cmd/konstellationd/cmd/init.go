package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/types/module"
	genutilcli "github.com/cosmos/cosmos-sdk/x/genutil/client/cli"

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
	return cmd
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
