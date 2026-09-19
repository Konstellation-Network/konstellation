package types

// Events. Every quota refusal and every window reset is on chain so the
// history of what was blocked, and why, is reconstructible.
const (
	EventTypeRateLimitAdded   = "ratelimit_added"
	EventTypeRateLimitUpdated = "ratelimit_updated"
	EventTypeRateLimitRemoved = "ratelimit_removed"
	EventTypeRateLimitReset   = "ratelimit_reset"
	EventTypeWindowReset      = "ratelimit_window_reset"
	EventTypeQuotaExceeded    = "ratelimit_quota_exceeded"

	AttributeKeyDenom        = "denom"
	AttributeKeyChannel      = "channel_id"
	AttributeKeyDirection    = "direction"
	AttributeKeyAmount       = "amount"
	AttributeKeyChannelValue = "channel_value"
	AttributeKeyInflow       = "inflow"
	AttributeKeyOutflow      = "outflow"
	AttributeKeyThreshold    = "threshold"
	AttributeKeyBy           = "by"
)
