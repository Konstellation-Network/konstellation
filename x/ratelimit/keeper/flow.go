package keeper

import (
	"context"
	"fmt"
	"time"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	sdkmath "cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/Konstellation-Network/konstellation/x/ratelimit/types"
)

// freshFlow starts a window now: zero flows, channel value = the denom's
// current total supply on this chain.
func (k Keeper) freshFlow(ctx context.Context, denom string) types.Flow {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	return types.Flow{
		Inflow:       sdkmath.ZeroInt(),
		Outflow:      sdkmath.ZeroInt(),
		ChannelValue: k.bank.GetSupply(ctx, denom).Amount,
		WindowStart:  sdkCtx.BlockTime(),
	}
}

// nextFlow starts a new window on an existing limit. The channel value is
// re-snapshotted from supply — except when supply has gone to zero, which
// for a voucher or ERC20-origin denom just means everything is currently
// on the other side. A zero channel value would make the threshold zero
// and refuse every inbound packet until governance removed the limit
// (update and reset would snapshot zero again), so the previous non-zero
// value is carried forward instead; the next window after supply returns
// re-snapshots as usual.
func (k Keeper) nextFlow(ctx context.Context, rl types.RateLimit) types.Flow {
	flow := k.freshFlow(ctx, rl.Path.Denom)
	if flow.ChannelValue.IsZero() {
		flow.ChannelValue = rl.Flow.ChannelValue
	}
	return flow
}

// addRateLimit creates a limit on a path that has none.
func (k Keeper) addRateLimit(ctx context.Context, path types.Path, quota types.Quota, by string) error {
	if _, exists, err := k.GetRateLimit(ctx, path.Denom, path.ChannelId); err != nil {
		return err
	} else if exists {
		return errorsmod.Wrapf(types.ErrRateLimitExists, "%s on %s", path.Denom, path.ChannelId)
	}
	flow := k.freshFlow(ctx, path.Denom)
	if flow.ChannelValue.IsZero() && !quota.Bootstraps() {
		// A limit on a denom with no supply would be a limit of zero.
		// Refuse so a typo in the denom is caught by the proposal, not by
		// every transfer afterwards. The exception is a foreign token
		// limited before its first packet: its voucher has no supply yet,
		// and a positive absolute receive cap is what the limit means until
		// it does (§15 phase 9 sets quotas before a channel carries value).
		return errorsmod.Wrapf(types.ErrZeroSupply,
			"%s (to limit a foreign token before it arrives, set max_percent_recv and max_absolute_recv)", path.Denom)
	}
	if err := k.SetRateLimit(ctx, types.RateLimit{Path: path, Quota: quota, Flow: flow}); err != nil {
		return err
	}
	emitLimitEvent(ctx, types.EventTypeRateLimitAdded, path, quota, by)
	return nil
}

// updateRateLimit replaces the quota and starts a fresh window.
func (k Keeper) updateRateLimit(ctx context.Context, path types.Path, quota types.Quota, by string) error {
	rl, exists, err := k.GetRateLimit(ctx, path.Denom, path.ChannelId)
	if err != nil {
		return err
	}
	if !exists {
		return errorsmod.Wrapf(types.ErrRateLimitNotFound, "%s on %s", path.Denom, path.ChannelId)
	}
	if err := k.clearPending(ctx, path); err != nil {
		return err
	}
	if err := k.SetRateLimit(ctx, types.RateLimit{Path: path, Quota: quota, Flow: k.nextFlow(ctx, rl)}); err != nil {
		return err
	}
	emitLimitEvent(ctx, types.EventTypeRateLimitUpdated, path, quota, by)
	return nil
}

// removeRateLimit deletes a limit.
func (k Keeper) removeRateLimit(ctx context.Context, path types.Path, by string) error {
	if _, exists, err := k.GetRateLimit(ctx, path.Denom, path.ChannelId); err != nil {
		return err
	} else if !exists {
		return errorsmod.Wrapf(types.ErrRateLimitNotFound, "%s on %s", path.Denom, path.ChannelId)
	}
	if err := k.clearPending(ctx, path); err != nil {
		return err
	}
	if err := k.RateLimits.Remove(ctx, pathKey(path)); err != nil {
		return err
	}
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeRateLimitRemoved,
		sdk.NewAttribute(types.AttributeKeyDenom, path.Denom),
		sdk.NewAttribute(types.AttributeKeyChannel, path.ChannelId),
		sdk.NewAttribute(types.AttributeKeyBy, by),
	))
	return nil
}

// resetRateLimit zeroes the flow and starts a fresh window.
func (k Keeper) resetRateLimit(ctx context.Context, path types.Path, by string) error {
	rl, exists, err := k.GetRateLimit(ctx, path.Denom, path.ChannelId)
	if err != nil {
		return err
	}
	if !exists {
		return errorsmod.Wrapf(types.ErrRateLimitNotFound, "%s on %s", path.Denom, path.ChannelId)
	}
	if err := k.resetWindow(ctx, rl); err != nil {
		return err
	}
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeRateLimitReset,
		sdk.NewAttribute(types.AttributeKeyDenom, path.Denom),
		sdk.NewAttribute(types.AttributeKeyChannel, path.ChannelId),
		sdk.NewAttribute(types.AttributeKeyBy, by),
	))
	return nil
}

// resetWindow starts a fresh window on rl and forgets its pending sends.
func (k Keeper) resetWindow(ctx context.Context, rl types.RateLimit) error {
	if err := k.clearPending(ctx, rl.Path); err != nil {
		return err
	}
	rl.Flow = k.nextFlow(ctx, rl)
	if err := k.SetRateLimit(ctx, rl); err != nil {
		return err
	}
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeWindowReset,
		sdk.NewAttribute(types.AttributeKeyDenom, rl.Path.Denom),
		sdk.NewAttribute(types.AttributeKeyChannel, rl.Path.ChannelId),
		sdk.NewAttribute(types.AttributeKeyChannelValue, rl.Flow.ChannelValue.String()),
	))
	return nil
}

// BeginBlock resets every window whose duration has elapsed.
func (k Keeper) BeginBlock(ctx context.Context) error {
	now := sdk.UnwrapSDKContext(ctx).BlockTime()
	all, err := k.AllRateLimits(ctx)
	if err != nil {
		return err
	}
	for _, rl := range all {
		if !now.Before(rl.Flow.WindowStart.Add(time.Duration(rl.Quota.DurationHours) * time.Hour)) { //nolint:gosec // bounded by MaxDurationHours
			if err := k.resetWindow(ctx, rl); err != nil {
				return err
			}
		}
	}
	return nil
}

// CheckAndRecord is the gate: it refuses amount in direction if it would
// exceed the path's quota, and otherwise adds it to the flow. Paths with
// no limit pass untouched. It reports whether a limit applied so the caller
// knows whether an undo is ever needed.
func (k Keeper) CheckAndRecord(ctx context.Context, direction types.PacketDirection, denom, channelID string, amount sdkmath.Int) (limited bool, err error) {
	rl, exists, err := k.GetRateLimit(ctx, denom, channelID)
	if err != nil {
		return true, err
	}
	if !exists {
		return false, nil
	}
	exceeded, threshold := rl.Quota.Exceeds(rl.Flow, direction, amount)
	if exceeded {
		sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeQuotaExceeded,
			sdk.NewAttribute(types.AttributeKeyDenom, denom),
			sdk.NewAttribute(types.AttributeKeyChannel, channelID),
			sdk.NewAttribute(types.AttributeKeyDirection, direction.String()),
			sdk.NewAttribute(types.AttributeKeyAmount, amount.String()),
			sdk.NewAttribute(types.AttributeKeyThreshold, threshold.String()),
			sdk.NewAttribute(types.AttributeKeyInflow, rl.Flow.Inflow.String()),
			sdk.NewAttribute(types.AttributeKeyOutflow, rl.Flow.Outflow.String()),
		))
		return true, errorsmod.Wrapf(types.ErrQuotaExceeded,
			"%s %s of %s on %s: net %s flow would exceed %s (%s%% of %s, absolute cap %s) in this window",
			direction, amount, denom, channelID, direction, threshold, percentFor(rl.Quota, direction), rl.Flow.ChannelValue, absoluteFor(rl.Quota, direction))
	}
	if direction == types.PacketSend {
		rl.Flow.Outflow = rl.Flow.Outflow.Add(amount)
	} else {
		rl.Flow.Inflow = rl.Flow.Inflow.Add(amount)
	}
	return true, k.SetRateLimit(ctx, rl)
}

func percentFor(q types.Quota, d types.PacketDirection) sdkmath.Int {
	if d == types.PacketSend {
		return q.MaxPercentSend
	}
	return q.MaxPercentRecv
}

func absoluteFor(q types.Quota, d types.PacketDirection) sdkmath.Int {
	if d == types.PacketSend {
		return q.MaxAbsoluteSend
	}
	return q.MaxAbsoluteRecv
}

// UndoSend puts a counted send back after an error ack or a timeout — but
// only if it was counted in the *current* window; a window reset forgets
// pending sends so a late failure cannot credit a window it never debited.
func (k Keeper) UndoSend(ctx context.Context, denom, channelID string, sequence uint64, amount sdkmath.Int) error {
	key := collections.Join3(channelID, denom, sequence)
	pending, err := k.PendingPackets.Has(ctx, key)
	if err != nil || !pending {
		return err
	}
	if err := k.PendingPackets.Remove(ctx, key); err != nil {
		return err
	}
	rl, exists, err := k.GetRateLimit(ctx, denom, channelID)
	if err != nil || !exists {
		return err
	}
	rl.Flow.Outflow = rl.Flow.Outflow.Sub(amount)
	if rl.Flow.Outflow.IsNegative() {
		rl.Flow.Outflow = sdkmath.ZeroInt()
	}
	return k.SetRateLimit(ctx, rl)
}

// UndoRecv puts a counted receive back when the application rejected the
// packet after the quota was debited (same window, same block).
func (k Keeper) UndoRecv(ctx context.Context, denom, channelID string, amount sdkmath.Int) error {
	rl, exists, err := k.GetRateLimit(ctx, denom, channelID)
	if err != nil || !exists {
		return err
	}
	rl.Flow.Inflow = rl.Flow.Inflow.Sub(amount)
	if rl.Flow.Inflow.IsNegative() {
		rl.Flow.Inflow = sdkmath.ZeroInt()
	}
	return k.SetRateLimit(ctx, rl)
}

// MarkPending records a counted send awaiting acknowledgement.
func (k Keeper) MarkPending(ctx context.Context, denom, channelID string, sequence uint64) error {
	return k.PendingPackets.Set(ctx, collections.Join3(channelID, denom, sequence))
}

// ClearPending forgets a send once it is acknowledged successfully.
func (k Keeper) ClearPending(ctx context.Context, denom, channelID string, sequence uint64) error {
	return k.PendingPackets.Remove(ctx, collections.Join3(channelID, denom, sequence))
}

// clearPending forgets every pending send on a path.
func (k Keeper) clearPending(ctx context.Context, path types.Path) error {
	rng := collections.NewSuperPrefixedTripleRange[string, string, uint64](path.ChannelId, path.Denom)
	return k.PendingPackets.Clear(ctx, rng)
}

func emitLimitEvent(ctx context.Context, typ string, path types.Path, q types.Quota, by string) {
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(typ,
		sdk.NewAttribute(types.AttributeKeyDenom, path.Denom),
		sdk.NewAttribute(types.AttributeKeyChannel, path.ChannelId),
		sdk.NewAttribute("max_percent_send", q.MaxPercentSend.String()),
		sdk.NewAttribute("max_percent_recv", q.MaxPercentRecv.String()),
		sdk.NewAttribute("max_absolute_send", q.MaxAbsoluteSend.String()),
		sdk.NewAttribute("max_absolute_recv", q.MaxAbsoluteRecv.String()),
		sdk.NewAttribute("duration_hours", fmt.Sprint(q.DurationHours)),
		sdk.NewAttribute(types.AttributeKeyBy, by),
	))
}
