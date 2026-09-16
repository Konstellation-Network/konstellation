package keeper

import (
	"context"
	"time"

	"cosmossdk.io/collections"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/Konstellation-Network/konstellation/x/compliance/types"
)

// EndBlock applies every pending update whose timelock has run and drops
// every emergency freeze that has lapsed. Both walks are index-ordered by
// time and stop at the first future entry, so cost is proportional to what
// is due, not to list size.
func (k Keeper) EndBlock(ctx context.Context) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	now := sdkCtx.BlockTime()

	// Due updates, in (execute_at, id) order.
	var due []uint64
	rng := collections.NewPrefixUntilPairRange[int64, uint64](now.Unix())
	if err := k.ExecIndex.Walk(ctx, rng, func(key collections.Pair[int64, uint64]) (bool, error) {
		due = append(due, key.K2())
		return false, nil
	}); err != nil {
		return err
	}
	for _, id := range due {
		p, err := k.Pending.Get(ctx, id)
		if err != nil {
			continue
		}
		if err := k.execute(ctx, p); err != nil {
			return err
		}
	}

	// Lapsed emergency freezes, in (expires_at, address) order.
	type lapsed struct {
		at   int64
		addr []byte
	}
	var gone []lapsed
	erng := collections.NewPrefixUntilPairRange[int64, []byte](now.Unix())
	if err := k.ExpiryIndex.Walk(ctx, erng, func(key collections.Pair[int64, []byte]) (bool, error) {
		gone = append(gone, lapsed{key.K1(), key.K2()})
		return false, nil
	}); err != nil {
		return err
	}
	for _, g := range gone {
		if err := k.ExpiryIndex.Remove(ctx, collections.Join(g.at, g.addr)); err != nil {
			return err
		}
		e, err := k.Block.Get(ctx, g.addr)
		// Only sweep if the entry is still the temporary one this index row
		// describes; a permanent entry may have replaced it meanwhile.
		if err != nil || e.ExpiresAt == nil || e.ExpiresAt.Unix() != g.at {
			continue
		}
		if err := k.Block.Remove(ctx, g.addr); err != nil {
			return err
		}
		sdkCtx.EventManager().EmitEvent(sdk.NewEvent(types.EventTypeEntryExpired,
			sdk.NewAttribute(types.AttributeKeyAddress, types.Bech32(g.addr)),
			sdk.NewAttribute(types.AttributeKeyExpiresAt, time.Unix(g.at, 0).UTC().Format(time.RFC3339)),
		))
	}
	return nil
}
