package types

import (
	"fmt"

	sdkmath "cosmossdk.io/math"
)

// MaxDurationHours bounds a window so a typo cannot lock a channel for
// years. 30 days.
const MaxDurationHours = 30 * 24

// Validate checks a quota: percentages in [0, 100], absolute caps not
// negative (unset is allowed and means none), a positive window.
func (q Quota) Validate() error {
	for name, p := range map[string]sdkmath.Int{"max_percent_send": q.MaxPercentSend, "max_percent_recv": q.MaxPercentRecv} {
		if p.IsNil() || p.IsNegative() || p.GT(sdkmath.NewInt(100)) {
			return fmt.Errorf("%s must be in [0, 100], got %s", name, p)
		}
	}
	for name, a := range map[string]sdkmath.Int{"max_absolute_send": q.MaxAbsoluteSend, "max_absolute_recv": q.MaxAbsoluteRecv} {
		if !a.IsNil() && a.IsNegative() {
			return fmt.Errorf("%s must not be negative, got %s", name, a)
		}
	}
	if q.DurationHours == 0 || q.DurationHours > MaxDurationHours {
		return fmt.Errorf("duration_hours must be in [1, %d], got %d", MaxDurationHours, q.DurationHours)
	}
	return nil
}

// Normalized returns q with unset absolute caps as zero, so stored quotas
// never carry a nil Int (a proposal's JSON may omit the fields).
func (q Quota) Normalized() Quota {
	if q.MaxAbsoluteSend.IsNil() {
		q.MaxAbsoluteSend = sdkmath.ZeroInt()
	}
	if q.MaxAbsoluteRecv.IsNil() {
		q.MaxAbsoluteRecv = sdkmath.ZeroInt()
	}
	return q
}

// Threshold is the amount a percentage of channelValue allows, capped by
// absolute when that is positive.
func Threshold(percent, channelValue, absolute sdkmath.Int) sdkmath.Int {
	t := channelValue.Mul(percent).Quo(sdkmath.NewInt(100))
	if !absolute.IsNil() && absolute.IsPositive() && absolute.LT(t) {
		return absolute
	}
	return t
}

// Threshold is the amount the quota allows in direction for the window
// whose channel value is channelValue.
func (q Quota) Threshold(direction PacketDirection, channelValue sdkmath.Int) sdkmath.Int {
	if direction == PacketSend {
		return Threshold(q.MaxPercentSend, channelValue, q.MaxAbsoluteSend)
	}
	return Threshold(q.MaxPercentRecv, channelValue, q.MaxAbsoluteRecv)
}

// Exceeds reports whether adding amount in direction would push the *net*
// flow past the quota. Net, not gross: an inflow of X and an outflow of X
// in the same window is a round trip, not a drain, and must not eat the
// quota (the ibc-apps / Osmosis semantics).
func (q Quota) Exceeds(f Flow, direction PacketDirection, amount sdkmath.Int) (exceeded bool, threshold sdkmath.Int) {
	threshold = q.Threshold(direction, f.ChannelValue)
	var net sdkmath.Int
	if direction == PacketSend {
		net = f.Outflow.Sub(f.Inflow).Add(amount)
	} else {
		net = f.Inflow.Sub(f.Outflow).Add(amount)
	}
	return net.GT(threshold), threshold
}

// Validate checks a path.
func (p Path) Validate() error {
	if p.Denom == "" {
		return fmt.Errorf("denom must not be empty")
	}
	if p.ChannelId == "" {
		return fmt.Errorf("channel_id must not be empty")
	}
	return nil
}
