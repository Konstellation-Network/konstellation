package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/collections"

	"github.com/Konstellation-Network/konstellation/x/compliance/types"
)

// InitGenesis writes the genesis state. Entries and pending updates are
// stored as given; indexes are rebuilt from them.
func (k Keeper) InitGenesis(ctx context.Context, gs types.GenesisState) error {
	if err := gs.Validate(); err != nil {
		return err
	}
	if err := k.Params.Set(ctx, gs.Params); err != nil {
		return err
	}
	for _, e := range gs.Entries {
		addr, _ := types.ParseAddress(e.Address)
		m, err := k.listOf(e.List)
		if err != nil {
			return err
		}
		e.Address = types.Bech32(addr)
		if err := m.Set(ctx, addr, e); err != nil {
			return err
		}
		if e.ExpiresAt != nil {
			if err := k.ExpiryIndex.Set(ctx, collections.Join(e.ExpiresAt.Unix(), addr)); err != nil {
				return err
			}
		}
	}
	for _, p := range gs.Pending {
		if err := k.Pending.Set(ctx, p.Id, p); err != nil {
			return err
		}
		if err := k.ExecIndex.Set(ctx, collections.Join(p.ExecuteAt.Unix(), p.Id)); err != nil {
			return err
		}
	}
	for _, c := range gs.Cooldowns {
		addr, _ := types.ParseAddress(c.Address)
		if err := k.Cooldown.Set(ctx, addr, c.Until); err != nil {
			return err
		}
	}
	// Sequence.Next returns the current value then increments; seed it so the
	// next id is exactly next_pending_id.
	return k.PendingSeq.Set(ctx, gs.NextPendingId)
}

// ExportGenesis reads the state back out.
func (k Keeper) ExportGenesis(ctx context.Context) (*types.GenesisState, error) {
	gs := &types.GenesisState{Params: k.GetParams(ctx)}
	for _, m := range []collections.Map[[]byte, types.ListEntry]{k.Allow, k.Block} {
		if err := m.Walk(ctx, nil, func(_ []byte, e types.ListEntry) (bool, error) {
			gs.Entries = append(gs.Entries, e)
			return false, nil
		}); err != nil {
			return nil, err
		}
	}
	if err := k.Pending.Walk(ctx, nil, func(_ uint64, p types.PendingUpdate) (bool, error) {
		gs.Pending = append(gs.Pending, p)
		return false, nil
	}); err != nil {
		return nil, err
	}
	if err := k.Cooldown.Walk(ctx, nil, func(addr []byte, until int64) (bool, error) {
		gs.Cooldowns = append(gs.Cooldowns, types.Cooldown{Address: types.Bech32(addr), Until: until})
		return false, nil
	}); err != nil {
		return nil, err
	}
	next, err := k.PendingSeq.Peek(ctx)
	if err != nil {
		return nil, fmt.Errorf("pending seq: %w", err)
	}
	gs.NextPendingId = next
	return gs, nil
}
