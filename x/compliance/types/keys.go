package types

import "cosmossdk.io/collections"

const (
	// ModuleName is the module name.
	ModuleName = "compliance"
	// StoreKey is the KV store key.
	StoreKey = ModuleName
)

// Collections prefixes. Stable: changing one is a store migration.
var (
	ParamsKey       = collections.NewPrefix(0)
	AllowListKey    = collections.NewPrefix(1)
	BlockListKey    = collections.NewPrefix(2)
	PendingKey      = collections.NewPrefix(3)
	PendingSeqKey   = collections.NewPrefix(4)
	ExpiryIndexKey  = collections.NewPrefix(5)
	ExecutionIdxKey = collections.NewPrefix(6)
)
