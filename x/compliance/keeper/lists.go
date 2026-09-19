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
		if c.List == types.LIST_BLOCK {
			if err := k.checkFreezable(ctx, addr); err != nil {
				return err
			}
		}
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
		if c.List == types.LIST_BLOCK {
			k.resetDelegation(ctx, addr, by)
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
	if err := k.checkFreezable(ctx, addr); err != nil {
		return err
	}
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
	k.resetDelegation(ctx, addr, by)
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
// emergency expiry and the scheduled execute_at. Returns the expiry it
// replaced so the caller can record it; nil if nothing was extended.
func (k Keeper) extendEmergencyTo(ctx context.Context, addr []byte, until time.Time) (*types.ExtendedFreeze, error) {
	e, err := k.Block.Get(ctx, addr)
	if err != nil || e.ExpiresAt == nil || !e.ExpiresAt.Before(until) {
		return nil, nil
	}
	rec := &types.ExtendedFreeze{Address: types.Bech32(addr), OriginalExpiresAt: *e.ExpiresAt, FrozenAt: e.AddedAt}
	if err := k.setExpiry(ctx, addr, e, until, "extended to scheduled ratification"); err != nil {
		return nil, err
	}
	return rec, nil
}

// setExpiry moves a temporary block entry's expiry, keeping the index in
// step.
func (k Keeper) setExpiry(ctx context.Context, addr []byte, e types.ListEntry, to time.Time, reason string) error {
	if err := k.ExpiryIndex.Remove(ctx, collections.Join(e.ExpiresAt.Unix(), addr)); err != nil {
		return err
	}
	exp := to.Truncate(time.Second)
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
		sdk.NewAttribute(types.AttributeKeyReason, reason),
	))
	return nil
}

// lapse drops a temporary block entry the way EndBlock does when its expiry
// passes: entry and index row gone, cooldown started, entry_expired emitted.
func (k Keeper) lapse(ctx context.Context, addr []byte, e types.ListEntry) error {
	if err := k.ExpiryIndex.Remove(ctx, collections.Join(e.ExpiresAt.Unix(), addr)); err != nil {
		return err
	}
	if err := k.Block.Remove(ctx, addr); err != nil {
		return err
	}
	if err := k.startCooldown(ctx, addr); err != nil {
		return err
	}
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeEntryExpired,
		sdk.NewAttribute(types.AttributeKeyAddress, types.Bech32(addr)),
		sdk.NewAttribute(types.AttributeKeyExpiresAt, e.ExpiresAt.UTC().Format(time.RFC3339)),
	))
	return nil
}

// restoreExtension undoes what scheduling update `cancelled` did to the
// emergency freeze rec describes. The freeze goes back to its original
// expiry, or further out if another pending block-list add still ratifies
// it (that update inherits the record so its own cancellation restores
// correctly). If the restored expiry has already passed, the freeze lapses
// now with the usual cooldown. A freeze that is not the one extended (the
// original was removed by governance and the address frozen afresh) is
// left alone.
func (k Keeper) restoreExtension(ctx context.Context, rec types.ExtendedFreeze, cancelled types.PendingUpdate) error {
	addr, _ := types.ParseAddress(rec.Address)
	e, err := k.Block.Get(ctx, addr)
	if err != nil || e.ExpiresAt == nil || !e.AddedAt.Equal(rec.FrozenAt) {
		return nil // lifted, ratified by a permanent entry, or a different freeze
	}
	target := rec.OriginalExpiresAt
	var heir *types.PendingUpdate
	if err := k.Pending.Walk(ctx, nil, func(_ uint64, q types.PendingUpdate) (bool, error) {
		for _, c := range q.Changes {
			if c.List != types.LIST_BLOCK || c.Action != types.ACTION_ADD {
				continue
			}
			if a, _ := types.ParseAddress(c.Address); string(a) != string(addr) {
				continue
			}
			if q.ExecuteAt.After(target) {
				target = q.ExecuteAt
				qq := q
				heir = &qq
			}
		}
		return false, nil
	}); err != nil {
		return err
	}
	if heir != nil {
		// The heir may itself hold a record for this freeze, taken when it
		// extended from *our* extension; the earliest original is the true
		// one.
		found := false
		for i, x := range heir.Extended {
			if a, _ := types.ParseAddress(x.Address); string(a) != string(addr) || !x.FrozenAt.Equal(rec.FrozenAt) {
				continue
			}
			found = true
			if rec.OriginalExpiresAt.Before(x.OriginalExpiresAt) {
				heir.Extended[i].OriginalExpiresAt = rec.OriginalExpiresAt
			}
		}
		if !found {
			heir.Extended = append(heir.Extended, rec)
		}
		if err := k.Pending.Set(ctx, heir.Id, *heir); err != nil {
			return err
		}
	}
	if target.Equal(*e.ExpiresAt) {
		return nil
	}
	if !target.After(sdk.UnwrapSDKContext(ctx).BlockTime()) {
		return k.lapse(ctx, addr, e)
	}
	return k.setExpiry(ctx, addr, e, target, fmt.Sprintf("restored after cancelling update %d", cancelled.Id))
}

// schedule queues changes to execute once every change's timelock has run.
func (k Keeper) schedule(ctx context.Context, changes []types.Change, by string) (types.PendingUpdate, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	params := k.GetParams(ctx)
	// Reject protected targets now; an EndBlock failure 24 h later would be
	// the wrong place to learn about a typo in a 100-address batch.
	for _, c := range changes {
		if c.List == types.LIST_BLOCK && c.Action == types.ACTION_ADD {
			addr, _ := types.ParseAddress(c.Address)
			if err := k.checkFreezable(ctx, addr); err != nil {
				return types.PendingUpdate{}, err
			}
		}
	}
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
	// A scheduled permanent freeze ratifies a live emergency freeze: keep
	// the address frozen through to execute_at, and remember what the
	// expiry was so cancelling puts it back.
	for _, c := range changes {
		if c.List == types.LIST_BLOCK && c.Action == types.ACTION_ADD {
			addr, _ := types.ParseAddress(c.Address)
			rec, err := k.extendEmergencyTo(ctx, addr, p.ExecuteAt)
			if err != nil {
				return types.PendingUpdate{}, err
			}
			if rec != nil {
				p.Extended = append(p.Extended, *rec)
			}
		}
	}
	if err := k.Pending.Set(ctx, id, p); err != nil {
		return types.PendingUpdate{}, err
	}
	if err := k.ExecIndex.Set(ctx, collections.Join(p.ExecuteAt.Unix(), id)); err != nil {
		return types.PendingUpdate{}, err
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

// cancel drops a pending update and puts back any emergency freeze it had
// extended. Without the restore, schedule-then-cancel every timelock would
// keep an address frozen indefinitely with no ratification and no cooldown.
func (k Keeper) cancel(ctx context.Context, id uint64, by string) error {
	p, err := k.Pending.Get(ctx, id)
	if err != nil {
		return types.ErrPendingNotFound.Wrapf("id %d", id)
	}
	// Governance is a superset of the list authority, not the reverse: the
	// authority must not be able to undo a passed proposal.
	if k.isGov(p.ScheduledBy) && !k.isGov(by) {
		return types.ErrGovScheduled.Wrapf("id %d", id)
	}
	if err := k.ExecIndex.Remove(ctx, collections.Join(p.ExecuteAt.Unix(), id)); err != nil {
		return err
	}
	if err := k.Pending.Remove(ctx, id); err != nil {
		return err
	}
	for _, x := range p.Extended {
		if err := k.restoreExtension(ctx, x, p); err != nil {
			return err
		}
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
			if types.ErrProtectedAddress.Is(err) {
				// The target became protected after scheduling (e.g. it is
				// now the authority). Skip it, loudly; never fail EndBlock.
				sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeUpdateExecuted,
					sdk.NewAttribute(types.AttributeKeyID, fmt.Sprint(p.Id)),
					sdk.NewAttribute(types.AttributeKeyAddress, c.Address),
					sdk.NewAttribute("skipped", err.Error()),
				))
				continue
			}
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
