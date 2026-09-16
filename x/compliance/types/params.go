package types

import (
	"fmt"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// Defaults (ENGINEERING.md §11 D6, decided 2026-09-15).
const (
	DefaultTimelock             = 24 * time.Hour
	DefaultAllowlistAddTimelock = 24 * time.Hour
	DefaultEnforce              = true

	// MaxTimelock bounds both timelocks so a bad param can't lock the lists
	// for years.
	MaxTimelock = 90 * 24 * time.Hour
)

// DefaultParams has no authority: until governance (or genesis) sets one,
// only governance can touch the lists.
func DefaultParams() Params {
	return Params{
		Authority:            "",
		Timelock:             DefaultTimelock,
		AllowlistAddTimelock: DefaultAllowlistAddTimelock,
		Enforce:              DefaultEnforce,
	}
}

// Validate checks the params.
func (p Params) Validate() error {
	if p.Authority != "" {
		if _, err := sdk.AccAddressFromBech32(p.Authority); err != nil {
			return fmt.Errorf("authority: %w", err)
		}
	}
	// Strictly positive: with 0 an emergency freeze would expire in the block
	// it was created and never bind.
	if p.Timelock <= 0 || p.Timelock > MaxTimelock {
		return fmt.Errorf("timelock %s out of range (0, %s]", p.Timelock, MaxTimelock)
	}
	if p.AllowlistAddTimelock < 0 || p.AllowlistAddTimelock > MaxTimelock {
		return fmt.Errorf("allowlist_add_timelock %s out of range [0, %s]", p.AllowlistAddTimelock, MaxTimelock)
	}
	return nil
}

// TimelockFor returns the delay a change waits before executing.
func (p Params) TimelockFor(c Change) time.Duration {
	if c.List == LIST_ALLOW && c.Action == ACTION_ADD {
		return p.AllowlistAddTimelock
	}
	return p.Timelock
}
