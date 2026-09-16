package types

import (
	"fmt"
)

// DefaultGenesisState: default params, empty lists, nothing pending.
func DefaultGenesisState() *GenesisState {
	return &GenesisState{
		Params:        DefaultParams(),
		NextPendingId: 1,
	}
}

// Validate checks the genesis state.
func (gs GenesisState) Validate() error {
	if err := gs.Params.Validate(); err != nil {
		return fmt.Errorf("params: %w", err)
	}
	seen := make(map[string]struct{}, len(gs.Entries))
	for i, e := range gs.Entries {
		addr, err := ParseAddress(e.Address)
		if err != nil {
			return fmt.Errorf("entry %d: %w", i, err)
		}
		if e.List != LIST_ALLOW && e.List != LIST_BLOCK {
			return fmt.Errorf("entry %d: list %s", i, e.List)
		}
		if e.ExpiresAt != nil && e.List != LIST_BLOCK {
			return fmt.Errorf("entry %d: only block-list entries expire", i)
		}
		if len(e.Reason) > MaxReasonLength {
			return fmt.Errorf("entry %d: reason too long", i)
		}
		k := fmt.Sprintf("%x/%d", addr, e.List)
		if _, dup := seen[k]; dup {
			return fmt.Errorf("entry %d: duplicate %s on %s", i, e.Address, e.List)
		}
		seen[k] = struct{}{}
	}
	ids := make(map[uint64]struct{}, len(gs.Pending))
	for i, p := range gs.Pending {
		if p.Id == 0 || p.Id >= gs.NextPendingId {
			return fmt.Errorf("pending %d: id %d must be in [1, next_pending_id)", i, p.Id)
		}
		if _, dup := ids[p.Id]; dup {
			return fmt.Errorf("pending %d: duplicate id %d", i, p.Id)
		}
		ids[p.Id] = struct{}{}
		if err := validateChanges(p.Changes); err != nil {
			return fmt.Errorf("pending %d: %w", i, err)
		}
		if p.ScheduledBy != "" {
			if _, err := ParseAddress(p.ScheduledBy); err != nil {
				return fmt.Errorf("pending %d: scheduled_by: %w", i, err)
			}
		}
	}
	if gs.NextPendingId == 0 {
		return fmt.Errorf("next_pending_id must be ≥ 1")
	}
	cd := make(map[string]struct{}, len(gs.Cooldowns))
	for i, c := range gs.Cooldowns {
		addr, err := ParseAddress(c.Address)
		if err != nil {
			return fmt.Errorf("cooldown %d: %w", i, err)
		}
		if c.Until <= 0 {
			return fmt.Errorf("cooldown %d: until must be positive", i)
		}
		k := fmt.Sprintf("%x", addr)
		if _, dup := cd[k]; dup {
			return fmt.Errorf("cooldown %d: duplicate %s", i, c.Address)
		}
		cd[k] = struct{}{}
	}
	return nil
}
