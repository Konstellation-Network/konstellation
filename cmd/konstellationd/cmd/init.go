package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cosmos/cosmos-sdk/server"
	"github.com/cosmos/cosmos-sdk/x/genutil"
	genutiltypes "github.com/cosmos/cosmos-sdk/x/genutil/types"
)

// withAppDefaultGenesis wraps the SDK `init` command so the genesis it writes
// carries the chain's own defaults (app.DefaultGenesis) rather than the bare
// module defaults, which still say "stake" for every denom. Without this a
// freshly initialised node is not a valid Konstellation genesis until it has
// been patched by hand.
func withAppDefaultGenesis(initCmd *cobra.Command, defaultGenesis func() map[string]json.RawMessage) *cobra.Command {
	sdkRunE := initCmd.RunE
	initCmd.RunE = func(cmd *cobra.Command, args []string) error {
		if err := sdkRunE(cmd, args); err != nil {
			return err
		}

		genFile := server.GetServerContextFromCmd(cmd).Config.GenesisFile()
		appGenesis, err := genutiltypes.AppGenesisFromFile(genFile)
		if err != nil {
			return fmt.Errorf("read genesis written by init: %w", err)
		}

		appState, err := json.MarshalIndent(defaultGenesis(), "", " ")
		if err != nil {
			return fmt.Errorf("marshal app default genesis: %w", err)
		}
		appGenesis.AppState = appState

		if err := genutil.ExportGenesisFile(appGenesis, genFile); err != nil {
			return fmt.Errorf("write genesis: %w", err)
		}
		return nil
	}
	return initCmd
}
