package keeper

import (
	"context"

	"cosmossdk.io/collections"

	"github.com/Konstellation-Network/konstellation/x/ratelimit/types"
)

// InitGenesis writes the genesis state as given.
func (k Keeper) InitGenesis(ctx context.Context, gs types.GenesisState) error {
	if err := gs.Validate(); err != nil {
		return err
	}
	for _, rl := range gs.RateLimits {
		rl.Quota = rl.Quota.Normalized()
		if err := k.SetRateLimit(ctx, rl); err != nil {
			return err
		}
	}
	for _, p := range gs.PendingPackets {
		if err := k.PendingPackets.Set(ctx, collections.Join3(p.ChannelId, p.Denom, p.Sequence)); err != nil {
			return err
		}
	}
	return nil
}

// ExportGenesis reads the state back.
func (k Keeper) ExportGenesis(ctx context.Context) (*types.GenesisState, error) {
	all, err := k.AllRateLimits(ctx)
	if err != nil {
		return nil, err
	}
	gs := &types.GenesisState{RateLimits: all}
	err = k.PendingPackets.Walk(ctx, nil, func(key collections.Triple[string, string, uint64]) (bool, error) {
		gs.PendingPackets = append(gs.PendingPackets, types.PendingPacket{ChannelId: key.K1(), Denom: key.K2(), Sequence: key.K3()})
		return false, nil
	})
	return gs, err
}
