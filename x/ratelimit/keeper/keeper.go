// Package keeper holds the rate limits and their running flows
// (ENGINEERING.md §13.2). The middleware in the parent package asks it two
// questions per packet — may this go, and record that it did — and tells it
// when a send failed so the amount is put back.
package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/collections"
	"cosmossdk.io/core/store"
	"cosmossdk.io/log/v2"

	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/Konstellation-Network/konstellation/x/ratelimit/types"
)

// Keeper owns the rate limits.
//
// State:
//
//	RateLimits     (channel_id, denom) → RateLimit
//	PendingPackets (channel_id, denom, sequence): sends counted in the
//	               current window and not yet acknowledged
type Keeper struct {
	cdc          codec.BinaryCodec
	govAuthority string
	bank         types.BankKeeper

	Schema         collections.Schema
	RateLimits     collections.Map[collections.Pair[string, string], types.RateLimit]
	PendingPackets collections.KeySet[collections.Triple[string, string, uint64]]
}

// NewKeeper builds the keeper. govAuthority (x/gov's module address) is the
// only signer of the module's messages.
func NewKeeper(cdc codec.BinaryCodec, storeService store.KVStoreService, govAuthority string, bank types.BankKeeper) Keeper {
	if _, err := sdk.AccAddressFromBech32(govAuthority); err != nil {
		panic(fmt.Errorf("ratelimit: invalid gov authority %q: %w", govAuthority, err))
	}
	sb := collections.NewSchemaBuilder(storeService)
	k := Keeper{
		cdc:          cdc,
		govAuthority: govAuthority,
		bank:         bank,
		RateLimits: collections.NewMap(sb, types.RateLimitsKey, "rate_limits",
			collections.PairKeyCodec(collections.StringKey, collections.StringKey), codec.CollValue[types.RateLimit](cdc)),
		PendingPackets: collections.NewKeySet(sb, types.PendingPacketsKey, "pending_packets",
			collections.TripleKeyCodec(collections.StringKey, collections.StringKey, collections.Uint64Key)),
	}
	schema, err := sb.Build()
	if err != nil {
		panic(err)
	}
	k.Schema = schema
	return k
}

// GovAuthority is the governance module address.
func (k Keeper) GovAuthority() string { return k.govAuthority }

// Logger returns a module-tagged logger.
func (k Keeper) Logger(ctx context.Context) log.Logger {
	return sdk.UnwrapSDKContext(ctx).Logger().With("module", "x/"+types.ModuleName)
}

func pathKey(p types.Path) collections.Pair[string, string] {
	return collections.Join(p.ChannelId, p.Denom)
}

// GetRateLimit returns the limit on a path, if any.
func (k Keeper) GetRateLimit(ctx context.Context, denom, channelID string) (types.RateLimit, bool) {
	rl, err := k.RateLimits.Get(ctx, collections.Join(channelID, denom))
	if err != nil {
		return types.RateLimit{}, false
	}
	return rl, true
}

// SetRateLimit stores a limit.
func (k Keeper) SetRateLimit(ctx context.Context, rl types.RateLimit) error {
	return k.RateLimits.Set(ctx, pathKey(rl.Path), rl)
}

// AllRateLimits returns every limit, ordered by (channel, denom).
func (k Keeper) AllRateLimits(ctx context.Context) ([]types.RateLimit, error) {
	var out []types.RateLimit
	err := k.RateLimits.Walk(ctx, nil, func(_ collections.Pair[string, string], rl types.RateLimit) (bool, error) {
		out = append(out, rl)
		return false, nil
	})
	return out, err
}

// RateLimitsByChannel returns the limits on one channel.
func (k Keeper) RateLimitsByChannel(ctx context.Context, channelID string) ([]types.RateLimit, error) {
	var out []types.RateLimit
	rng := collections.NewPrefixedPairRange[string, string](channelID)
	err := k.RateLimits.Walk(ctx, rng, func(_ collections.Pair[string, string], rl types.RateLimit) (bool, error) {
		out = append(out, rl)
		return false, nil
	})
	return out, err
}
