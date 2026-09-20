package types

import "cosmossdk.io/collections"

const (
	// ModuleName is the module name.
	ModuleName = "ratelimit"
	// StoreKey is the KV store key.
	StoreKey = ModuleName
)

// Collections prefixes. Stable: changing one is a store migration.
var (
	RateLimitsKey     = collections.NewPrefix(0)
	PendingPacketsKey = collections.NewPrefix(1)
)

// PacketDirection is which way a packet moves value relative to this chain.
type PacketDirection int

const (
	PacketSend PacketDirection = iota
	PacketRecv
)

func (d PacketDirection) String() string {
	if d == PacketSend {
		return "send"
	}
	return "recv"
}
