package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// cosmos-sdk's server.InterceptConfigsPreRunHandler copies every app.toml
// value onto the matching command flag the user did not pass — with
// `cmd.Flags().Set(name, fmt.Sprintf("%v", value))` (server/util.go
// bindFlags). For a TOML array that string is Go's `[a b]` rendering, which a
// StringSlice flag parses as ONE element, and the flag is then marked
// changed, so viper prefers it over the file. Net effect on cosmos/evm
// v0.7.3: `ws-origins = ["127.0.0.1", "localhost"]` reaches the WebSocket
// server as `["[127.0.0.1 localhost]"]` and every browser Origin gets a 403
// (STATUS.md §5a P27). The two cosmos/evm flags `--json-rpc.api` and
// `--json-rpc.ws-origins` are the whole affected set: they are the only
// slice flags `start` has (SDK v0.54.3; `index-events` is read through
// appOpts, not a flag). Values written as a string (`api = "eth,net,web3"`
// is what the template writes) or passed on the command line are
// unaffected, which is why local_node.sh's `--json-rpc.api` always worked.
//
// The two functions below undo that: record which slice flags the user
// actually passed before the handler runs, and afterwards re-parse any
// slice flag the handler mangled. Nothing here reads app.toml a second
// time or changes precedence (flag > env > file > default); it only restores
// the array the file held. Remove once upstream fixes bindFlags.
//
// Known limits of re-parsing the `[a b]` rendering, all irrelevant to
// hostnames and RPC namespaces: an element containing a comma cannot be
// repaired at all (StringSlice.Set CSV-splits before we see it, leaving
// `["[a" "b c]"]`, which is not the one-element form); an element containing
// a space is split in two.

// sliceFlagsSetByUser returns the slice-valued flags present on the command
// line, i.e. the ones the SDK will leave alone.
func sliceFlagsSetByUser(cmd *cobra.Command) map[string]bool {
	set := map[string]bool{}
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if _, ok := f.Value.(pflag.SliceValue); ok && f.Changed {
			set[f.Name] = true
		}
	})
	return set
}

// repairSliceFlags restores every slice flag that the SDK's config
// interception filled from a TOML array. userSet is sliceFlagsSetByUser's
// result from before the interception.
func repairSliceFlags(cmd *cobra.Command, userSet map[string]bool) error {
	var err error
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		// Only a flag the SDK's Set touched is Changed here (the user's
		// own are in userSet); a slice flag whose *default* happens to be a
		// single bracketed value is left alone.
		if err != nil || userSet[f.Name] || !f.Changed {
			return
		}
		sv, ok := f.Value.(pflag.SliceValue)
		if !ok {
			return
		}
		fixed, mangled := unmangleSlice(sv.GetSlice())
		if !mangled {
			return
		}
		if rerr := sv.Replace(fixed); rerr != nil {
			err = fmt.Errorf("flag --%s: restoring %v from app.toml: %w", f.Name, fixed, rerr)
		}
	})
	return err
}

// unmangleSlice recognises the one-element `[a b c]` form fmt produces for a
// []interface{} and splits it back into its elements. Elements of the slice
// flags this applies to (origins, RPC namespaces, event keys) never contain
// whitespace, and a genuine one-element value that both starts with `[` and
// ends with `]` does not occur in any of them.
func unmangleSlice(vals []string) ([]string, bool) {
	if len(vals) != 1 {
		return nil, false
	}
	v := vals[0]
	if !strings.HasPrefix(v, "[") || !strings.HasSuffix(v, "]") {
		return nil, false
	}
	return strings.Fields(v[1 : len(v)-1]), true
}
