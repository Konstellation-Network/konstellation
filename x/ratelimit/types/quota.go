package types

import (
	"fmt"

	sdkmath "cosmossdk.io/math"
)

// MaxDurationHours bounds a window so a typo cannot lock a channel for
// years. 30 days.
const MaxDurationHours = 30 * 24

// Validate checks a quota: percentages in [0, 100], a positive window.
func (q Quota) Validate() error {
	for name, p := range map[string]sdkmath.Int{"max_percent_send": q.MaxPercentSend, "max_percent_recv": q.MaxPercentRecv} {
		if p.IsNil() || p.IsNegative() || p.GT(sdkmath.NewInt(100)) {
			return fmt.Errorf("%s must be in [0, 100], got %s", name, p)
		}
	}
	if q.DurationHours == 0 || q.DurationHours > MaxDurationHours {
		return fmt.Errorf("duration_hours must be in [1, %d], got %d", MaxDurationHours, q.DurationHours)
	}
	return nil
}

// Threshold is the absolute amount a percentage of channelValue allows.
func Threshold(percent, channelValue sdkmath.Int) sdkmath.Int {
	return channelValue.Mul(percent).Quo(sdkmath.NewInt(100))
}

// Exceeds reports whether adding amount in direction would push the *net*
// flow past the quota. Net, not gross: an inflow of X and an outflow of X
// in the same window is a round trip, not a drain, and must not eat the
// quota (the ibc-apps / Osmosis semantics).
func (q Quota) Exceeds(f Flow, direction PacketDirection, amount sdkmath.Int) (exceeded bool, threshold sdkmath.Int) {
	switch direction {
	case PacketSend:
		threshold = Threshold(q.MaxPercentSend, f.ChannelValue)
		net := f.Outflow.Sub(f.Inflow).Add(amount)
		return net.GT(threshold), threshold
	default:
		threshold = Threshold(q.MaxPercentRecv, f.ChannelValue)
		net := f.Inflow.Sub(f.Outflow).Add(amount)
		return net.GT(threshold), threshold
	}
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
