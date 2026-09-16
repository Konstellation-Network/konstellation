package keeper

import (
	"context"
	"fmt"
	"time"

	"cosmossdk.io/collections"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/Konstellation-Network/konstellation/x/compliance/types"
)

// listOf returns the map backing list.
func (k Keeper) listOf(list types.List) (collections.Map[[]byte, types.ListEntry], error) {
	switch list {
	case types.LIST_ALLOW:
		return k.Allow, nil
	case types.LIST_BLOCK:
		return k.Block, nil
	default:
		return collections.Map[[]byte, types.ListEntry]{}, fmt.Errorf("unknown list %s", list)
	}
}

// applyChange makes one change effective now, replacing any existing entry
// (a permanent block entry replaces an emergency one and drops its expiry).
func (k Keeper) applyChange(ctx context.Context, c types.Change, by string) error {
	addr, err := types.ParseAddress(c.Address)
	if err != nil {
		return err
	}
	m, err := k.listOf(c.List)
	if err != nil {
		return err
	}
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	switch c.Action {
	case types.ACTION_ADD:
		if err := k.dropExpiry(ctx, m, addr); err != nil {
			return err
		}
		if err := m.Set(ctx, addr, types.ListEntry{
			Address: types.Bech32(addr),
			List:    c.List,
			AddedAt: sdkCtx.BlockTime(),
			Reason:  c.Reason,
		}); err != nil {
			return err
		}
		sdkCtx.EventManager().EmitEvent(sdk.NewEvent(types.EventTypeEntryAdded,
			sdk.NewAttribute(types.AttributeKeyAddress, types.Bech32(addr)),
			sdk.NewAttribute(types.AttributeKeyList, c.List.String()),
			sdk.NewAttribute(types.AttributeKeyReason, c.Reason),
			sdk.NewAttribute(types.AttributeKeyBy, by),
		))
	case types.ACTION_REMOVE:
		has, err := m.Has(ctx, addr)
		if err != nil {
			return err
		}
		if !has {
			return nil
		}
		if err := k.dropExpiry(ctx, m, addr); err != nil {
			return err
		}
		if err := m.Remove(ctx, addr); err != nil {
			return err
		}
		sdkCtx.EventManager().EmitEvent(sdk.NewEvent(types.EventTypeEntryRemoved,
			sdk.NewAttribute(types.AttributeKeyAddress, types.Bech32(addr)),
			sdk.NewAttribute(types.AttributeKeyList, c.List.String()),
			sdk.NewAttribute(types.AttributeKeyReason, c.Reason),
			sdk.NewAttribute(types.AttributeKeyBy, by),
		))
	default:
		return fmt.Errorf("unknown action %s", c.Action)
	}
	return nil
}

// dropExpiry removes the expiry-index entry for an existing emergency freeze
// on addr, if there is one. Only the block list carries expiries.
func (k Keeper) dropExpiry(ctx context.Context, m collections.Map[[]byte, types.ListEntry], addr []byte) error {
	e, err := m.Get(ctx, addr)
	if err != nil || e.ExpiresAt == nil {
		return nil
	}
	return k.ExpiryIndex.Remove(ctx, collections.Join(e.ExpiresAt.Unix(), addr))
}

// emergencyFreeze writes a temporary block entry expiring at expiresAt.
//
// "Auto-expires unless ratified" (D6) has teeth only if the emergency path
// cannot be chained: an address under a live emergency freeze, or inside the
// cooldown that follows one, is refused — the authority must ratify through
// a scheduled (timelocked, cancellable, public) update or governance. A
// permanent entry is left alone: it is already stronger.
func (k Keeper) emergencyFreeze(ctx context.Context, addr []byte, reason, by string, expiresAt time.Time) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	if e, err := k.Block.Get(ctx, addr); err == nil {
		if e.ExpiresAt == nil {
			return nil // already permanently frozen
		}
		return types.ErrEmergencyCooldown.Wrapf("%s is emergency-frozen until %s", types.Bech32(addr), e.ExpiresAt.UTC().Format(time.RFC3339))
	}
	if until, err := k.Cooldown.Get(ctx, addr); err == nil {
		if sdkCtx.BlockTime().Unix() < until {
			return types.ErrEmergencyCooldown.Wrapf("%s in cooldown until %s", types.Bech32(addr), time.Unix(until, 0).UTC().Format(time.RFC3339))
		}
		if err := k.Cooldown.Remove(ctx, addr); err != nil {
			return err
		}
	}
	exp := expiresAt.Truncate(time.Second) // index keys are whole seconds
	if err := k.Block.Set(ctx, addr, types.ListEntry{
		Address:   types.Bech32(addr),
		List:      types.LIST_BLOCK,
		AddedAt:   sdkCtx.BlockTime(),
		ExpiresAt: &exp,
		Reason:    reason,
	}); err != nil {
		return err
	}
	if err := k.ExpiryIndex.Set(ctx, collections.Join(exp.Unix(), addr)); err != nil {
		return err
	}
	sdkCtx.EventManager().EmitEvent(sdk.NewEvent(types.EventTypeEmergencyFreeze,
		sdk.NewAttribute(types.AttributeKeyAddress, types.Bech32(addr)),
		sdk.NewAttribute(types.AttributeKeyExpiresAt, exp.UTC().Format(time.RFC3339)),
		sdk.NewAttribute(types.AttributeKeyReason, reason),
		sdk.NewAttribute(types.AttributeKeyBy, by),
	))
	return nil
}

// liftEmergencyFreeze removes a temporary block entry. Permanent entries
// need a scheduled or governance removal. Lifting starts the same cooldown
// as lapsing, so lift-and-refreeze is not a way around it.
func (k Keeper) liftEmergencyFreeze(ctx context.Context, addr []byte, by string) error {
	e, err := k.Block.Get(ctx, addr)
	if err != nil || e.ExpiresAt == nil {
		return types.ErrNotEmergencyFrozen.Wrapf("%s", types.Bech32(addr))
	}
	if err := k.ExpiryIndex.Remove(ctx, collections.Join(e.ExpiresAt.Unix(), addr)); err != nil {
		return err
	}
	if err := k.Block.Remove(ctx, addr); err != nil {
		return err
	}
	if err := k.startCooldown(ctx, addr); err != nil {
		return err
	}
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeEmergencyLifted,
		sdk.NewAttribute(types.AttributeKeyAddress, types.Bech32(addr)),
		sdk.NewAttribute(types.AttributeKeyBy, by),
	))
	return nil
}

// startCooldown blocks a fresh emergency freeze on addr for one timelock
// from now. Governance and scheduled updates are unaffected.
func (k Keeper) startCooldown(ctx context.Context, addr []byte) error {
	until := sdk.UnwrapSDKContext(ctx).BlockTime().Add(k.GetParams(ctx).Timelock).Truncate(time.Second).Unix()
	return k.Cooldown.Set(ctx, addr, until)
}

// extendEmergencyTo pushes a live emergency freeze on addr out to at least
// `until`. Called when a permanent block-list add is scheduled for the
// address, so ratification never leaves an unfrozen gap between the
// emergency expiry and the scheduled execute_at.
func (k Keeper) extendEmergencyTo(ctx context.Context, addr []byte, until time.Time) error {
	e, err := k.Block.Get(ctx, addr)
	if err != nil || e.ExpiresAt == nil || !e.ExpiresAt.Before(until) {
		return nil
	}
	if err := k.ExpiryIndex.Remove(ctx, collections.Join(e.ExpiresAt.Unix(), addr)); err != nil {
		return err
	}
	exp := until.Truncate(time.Second)
	e.ExpiresAt = &exp
	if err := k.Block.Set(ctx, addr, e); err != nil {
		return err
	}
	if err := k.ExpiryIndex.Set(ctx, collections.Join(exp.Unix(), addr)); err != nil {
		return err
	}
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeEmergencyFreeze,
		sdk.NewAttribute(types.AttributeKeyAddress, types.Bech32(addr)),
		sdk.NewAttribute(types.AttributeKeyExpiresAt, exp.UTC().Format(time.RFC3339)),
		sdk.NewAttribute(types.AttributeKeyReason, "extended to scheduled ratification"),
	))
	return nil
}

// schedule queues changes to execute once every change's timelock has run.
func (k Keeper) schedule(ctx context.Context, changes []types.Change, by string) (types.PendingUpdate, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	params := k.GetParams(ctx)
	var delay time.Duration
	for _, c := range changes {
		if d := params.TimelockFor(c); d > delay {
			delay = d
		}
	}
	id, err := k.PendingSeq.Next(ctx)
	if err != nil {
		return types.PendingUpdate{}, err
	}
	p := types.PendingUpdate{
		Id:          id,
		Changes:     changes,
		ExecuteAt:   sdkCtx.BlockTime().Add(delay).Truncate(time.Second), // index keys are whole seconds
		ScheduledBy: by,
	}
	if err := k.Pending.Set(ctx, id, p); err != nil {
		return types.PendingUpdate{}, err
	}
	if err := k.ExecIndex.Set(ctx, collections.Join(p.ExecuteAt.Unix(), id)); err != nil {
		return types.PendingUpdate{}, err
	}
	// A scheduled permanent freeze ratifies a live emergency freeze: keep
	// the address frozen through to execute_at.
	for _, c := range changes {
		if c.List == types.LIST_BLOCK && c.Action == types.ACTION_ADD {
			addr, _ := types.ParseAddress(c.Address)
			if err := k.extendEmergencyTo(ctx, addr, p.ExecuteAt); err != nil {
				return types.PendingUpdate{}, err
			}
		}
	}
	sdkCtx.EventManager().EmitEvent(sdk.NewEvent(types.EventTypeUpdateScheduled,
		sdk.NewAttribute(types.AttributeKeyID, fmt.Sprint(id)),
		sdk.NewAttribute(types.AttributeKeyExecuteAt, p.ExecuteAt.UTC().Format(time.RFC3339)),
		sdk.NewAttribute(types.AttributeKeyBy, by),
	))
	for _, c := range changes {
		sdkCtx.EventManager().EmitEvent(sdk.NewEvent(types.EventTypeUpdateScheduled,
			sdk.NewAttribute(types.AttributeKeyID, fmt.Sprint(id)),
			sdk.NewAttribute(types.AttributeKeyAddress, c.Address),
			sdk.NewAttribute(types.AttributeKeyList, c.List.String()),
			sdk.NewAttribute(types.AttributeKeyAction, c.Action.String()),
			sdk.NewAttribute(types.AttributeKeyReason, c.Reason),
		))
	}
	return p, nil
}

// cancel drops a pending update.
func (k Keeper) cancel(ctx context.Context, id uint64, by string) error {
	p, err := k.Pending.Get(ctx, id)
	if err != nil {
		return types.ErrPendingNotFound.Wrapf("id %d", id)
	}
	if err := k.ExecIndex.Remove(ctx, collections.Join(p.ExecuteAt.Unix(), id)); err != nil {
		return err
	}
	if err := k.Pending.Remove(ctx, id); err != nil {
		return err
	}
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeUpdateCancelled,
		sdk.NewAttribute(types.AttributeKeyID, fmt.Sprint(id)),
		sdk.NewAttribute(types.AttributeKeyBy, by),
	))
	return nil
}

// execute applies a pending update and removes it.
func (k Keeper) execute(ctx context.Context, p types.PendingUpdate) error {
	for _, c := range p.Changes {
		if err := k.applyChange(ctx, c, p.ScheduledBy); err != nil {
			return err
		}
	}
	if err := k.ExecIndex.Remove(ctx, collections.Join(p.ExecuteAt.Unix(), p.Id)); err != nil {
		return err
	}
	if err := k.Pending.Remove(ctx, p.Id); err != nil {
		return err
	}
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeUpdateExecuted,
		sdk.NewAttribute(types.AttributeKeyID, fmt.Sprint(p.Id)),
	))
	return nil
}
