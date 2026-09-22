package cmd

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"

	cmtcli "github.com/cometbft/cometbft/libs/cli"

	"github.com/cosmos/evm/rpc"
	cosmosevmserverconfig "github.com/cosmos/evm/server/config"
	srvflags "github.com/cosmos/evm/server/flags"

	"cosmossdk.io/log/v2"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/flags"
	sdkserver "github.com/cosmos/cosmos-sdk/server"
	svrcmd "github.com/cosmos/cosmos-sdk/server/cmd"
)

// The SDK's bindFlags renders a TOML array with fmt's %v before handing it to
// a StringSlice flag; this is the exact call it makes.
func mangleLikeSDK(t *testing.T, cmd *cobra.Command, name string, val []any) {
	t.Helper()
	if err := cmd.Flags().Set(name, fmt.Sprintf("%v", val)); err != nil {
		t.Fatal(err)
	}
}

func TestRepairSliceFlags(t *testing.T) {
	cmd := &cobra.Command{Use: "x"}
	cmd.Flags().StringSlice("origins", []string{"127.0.0.1", "localhost"}, "")
	cmd.Flags().StringSlice("events", []string{}, "")
	cmd.Flags().StringSlice("user", []string{"d"}, "")
	cmd.Flags().String("scalar", "", "")
	if err := cmd.ParseFlags([]string{"--user", "[not,mangled]"}); err != nil {
		t.Fatal(err)
	}
	userSet := sliceFlagsSetByUser(cmd)
	if !userSet["user"] || userSet["origins"] || userSet["events"] {
		t.Fatalf("user-set slice flags: %v", userSet)
	}

	mangleLikeSDK(t, cmd, "origins", []any{"localhost", "app.example.com"})
	mangleLikeSDK(t, cmd, "events", []any{})
	if got, _ := cmd.Flags().GetStringSlice("origins"); len(got) != 1 || got[0] != "[localhost app.example.com]" {
		t.Fatalf("precondition: the SDK's Set should have mangled the slice, got %q", got)
	}

	if err := repairSliceFlags(cmd, userSet); err != nil {
		t.Fatal(err)
	}
	if got, _ := cmd.Flags().GetStringSlice("origins"); strings.Join(got, ",") != "localhost,app.example.com" {
		t.Errorf("origins not restored: %q", got)
	}
	if got, _ := cmd.Flags().GetStringSlice("events"); len(got) != 0 {
		t.Errorf("empty array not restored: %q", got)
	}
	// A value the user passed is theirs, bracketed or not.
	if got, _ := cmd.Flags().GetStringSlice("user"); strings.Join(got, ",") != "[not,mangled]" {
		t.Errorf("user-set flag rewritten: %q", got)
	}
}

// TestStartHonoursAppTomlWSOrigins runs the real root pre-run — the SDK's
// config interception plus our repair — on a fresh home, exactly as
// `konstellationd start` does, and checks that (1) the config cosmos/evm
// builds from it carries app.toml's `ws-origins` array intact, and (2)
// cosmos/evm's own WebSocket server, given that config, accepts a
// browser-style Origin for each listed host and still refuses others.
// Without the repair, (1) is `["[127.0.0.1 localhost]"]` and every
// Origin gets a 403 (STATUS.md §5a P27).
func TestStartHonoursAppTomlWSOrigins(t *testing.T) {
	home := t.TempDir()
	var start *cobra.Command

	// One fresh command tree per run, as one process has: a parsed flag
	// stays "changed" on the cobra object, which would mask later runs.
	preRun := func(t *testing.T, args ...string) cosmosevmserverconfig.Config {
		t.Helper()
		root := NewRootCmd()
		root.PersistentFlags().String(flags.FlagHome, home, "")
		root.PersistentFlags().Bool(cmtcli.TraceFlag, false, "")
		var err error
		start, _, err = root.Find([]string{"start"})
		if err != nil {
			t.Fatal(err)
		}
		start.SetContext(svrcmd.CreateExecuteContext(context.Background()))
		if err := start.ParseFlags(append([]string{"--home", home}, args...)); err != nil {
			t.Fatal(err)
		}
		if err := root.PersistentPreRunE(start, nil); err != nil {
			t.Fatal(err)
		}
		cfg, err := cosmosevmserverconfig.GetConfig(sdkserver.GetServerContextFromCmd(start).Viper)
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}

	// 1. The app.toml `init` writes: cosmos/evm's default origins.
	cfg := preRun(t)
	if got := strings.Join(cfg.JSONRPC.WSOrigins, ","); got != strings.Join(cosmosevmserverconfig.GetDefaultWSOrigins(), ",") {
		t.Fatalf("ws-origins from a fresh app.toml: %q", cfg.JSONRPC.WSOrigins)
	}
	assertOriginCheck(t, cfg, map[string]bool{
		"http://localhost": true, "http://localhost:3000": true, "http://127.0.0.1:5173": true,
		"https://dapp.example.com": false,
	})

	// 2. An operator's edit: an extra host in the array.
	appToml := filepath.Join(home, "config", "app.toml")
	bz, err := os.ReadFile(appToml)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(bz), `ws-origins = ["127.0.0.1", "localhost"]`, `ws-origins = ["localhost", "dapp.example.com"]`, 1)
	if edited == string(bz) {
		t.Fatal("app.toml does not carry the default ws-origins array this test edits")
	}
	if err := os.WriteFile(appToml, []byte(edited), 0o600); err != nil { //nolint:gosec // G703: path is <t.TempDir()>/config/app.toml
		t.Fatal(err)
	}
	cfg = preRun(t)
	if got := strings.Join(cfg.JSONRPC.WSOrigins, ","); got != "localhost,dapp.example.com" {
		t.Fatalf("ws-origins from an edited app.toml: %q", cfg.JSONRPC.WSOrigins)
	}
	assertOriginCheck(t, cfg, map[string]bool{
		"https://dapp.example.com": true, "http://localhost:8080": true, "http://127.0.0.1": false,
	})

	// 3. The command line still wins over the file.
	cfg = preRun(t, "--"+srvflags.JSONRPCWSOrigins, "cli.example.com,localhost")
	if got := strings.Join(cfg.JSONRPC.WSOrigins, ","); got != "cli.example.com,localhost" {
		t.Fatalf("ws-origins from the flag: %q", cfg.JSONRPC.WSOrigins)
	}

	// The only slice value the same app.toml writes as an array besides
	// ws-origins is the SDK's index-events; it must come back empty, not as
	// the one bogus key "[]".
	if got := sdkserver.GetServerContextFromCmd(start).Viper.GetStringSlice(sdkserver.FlagIndexEvents); len(got) != 0 {
		t.Errorf("index-events: %q", got)
	}
}

// assertOriginCheck drives cosmos/evm's real WebSocket upgrade handler
// (rpc.NewWebsocketsServer with cfg) with browser-style Origin headers.
func assertOriginCheck(t *testing.T, cfg cosmosevmserverconfig.Config, want map[string]bool) {
	t.Helper()
	handler, ok := rpc.NewWebsocketsServer(client.Context{}, log.NewNopLogger(), nil, &cfg).(http.Handler)
	if !ok {
		t.Fatal("cosmos/evm's websockets server no longer serves HTTP directly; re-check this test on the bump")
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	var mu sync.Mutex
	for origin, accepted := range want {
		mu.Lock()
		conn, resp, err := websocket.DefaultDialer.Dial(url, http.Header{"Origin": {origin}})
		mu.Unlock()
		if accepted {
			if err != nil {
				status := 0
				if resp != nil {
					status = resp.StatusCode
				}
				t.Errorf("Origin %s: upgrade refused (HTTP %d, %v) with ws-origins %v", origin, status, err, cfg.JSONRPC.WSOrigins)
				continue
			}
			_ = conn.Close()
			continue
		}
		if err == nil {
			_ = conn.Close()
			t.Errorf("Origin %s: accepted with ws-origins %v", origin, cfg.JSONRPC.WSOrigins)
		} else if resp == nil || resp.StatusCode != http.StatusForbidden {
			t.Errorf("Origin %s: want 403, got %v", origin, err)
		}
	}
}
