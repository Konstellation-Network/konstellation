package types

import "fmt"

// DefaultGenesisState: no limits. Limits are added per channel by
// governance once channels exist.
func DefaultGenesisState() *GenesisState {
	return &GenesisState{}
}

// Validate checks the genesis state.
func (gs GenesisState) Validate() error {
	seen := make(map[string]struct{}, len(gs.RateLimits))
	for i, rl := range gs.RateLimits {
		if err := rl.Path.Validate(); err != nil {
			return fmt.Errorf("rate limit %d: %w", i, err)
		}
		if err := rl.Quota.Validate(); err != nil {
			return fmt.Errorf("rate limit %d: %w", i, err)
		}
		for name, v := range map[string]interface{ IsNil() bool }{"inflow": rl.Flow.Inflow, "outflow": rl.Flow.Outflow, "channel_value": rl.Flow.ChannelValue} {
			if v.IsNil() {
				return fmt.Errorf("rate limit %d: flow.%s must be set", i, name)
			}
		}
		if rl.Flow.Inflow.IsNegative() || rl.Flow.Outflow.IsNegative() || rl.Flow.ChannelValue.IsNegative() {
			return fmt.Errorf("rate limit %d: negative flow", i)
		}
		k := rl.Path.ChannelId + "/" + rl.Path.Denom
		if _, dup := seen[k]; dup {
			return fmt.Errorf("rate limit %d: duplicate path %s", i, k)
		}
		seen[k] = struct{}{}
	}
	pending := make(map[string]struct{}, len(gs.PendingPackets))
	for i, p := range gs.PendingPackets {
		if p.ChannelId == "" || p.Denom == "" {
			return fmt.Errorf("pending packet %d: empty channel or denom", i)
		}
		k := fmt.Sprintf("%s/%s/%d", p.ChannelId, p.Denom, p.Sequence)
		if _, dup := pending[k]; dup {
			return fmt.Errorf("pending packet %d: duplicate %s", i, k)
		}
		pending[k] = struct{}{}
	}
	return nil
}
