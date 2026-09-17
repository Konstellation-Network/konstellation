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
	// MinTimelock is the floor. A timelock of seconds turns the design into
	// "the authority has immediate unilateral power", which the 24 h default
	// exists to prevent; a minute is enough for dev chains and makes any
	// governance proposal that lowers a real network's timelock a visible
	// act rather than an off-by-one. (ENGINEERING.md §18: dev/testnet use
	// short values, mainnet 24 h.)
	MinTimelock = time.Minute
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
	if p.Timelock < MinTimelock || p.Timelock > MaxTimelock {
		return fmt.Errorf("timelock %s out of range [%s, %s]", p.Timelock, MinTimelock, MaxTimelock)
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
