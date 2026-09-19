package types

import errorsmod "cosmossdk.io/errors"

var (
	ErrUnauthorized      = errorsmod.Register(ModuleName, 2, "signer is not governance")
	ErrRateLimitExists   = errorsmod.Register(ModuleName, 3, "a rate limit already exists for this path")
	ErrRateLimitNotFound = errorsmod.Register(ModuleName, 4, "no rate limit for this path")
	ErrInvalidQuota      = errorsmod.Register(ModuleName, 5, "invalid quota")
	ErrQuotaExceeded     = errorsmod.Register(ModuleName, 6, "transfer would exceed the rate limit for this path")
	ErrInvalidPacket     = errorsmod.Register(ModuleName, 7, "cannot parse ICS-20 packet")
	ErrInvalidPath       = errorsmod.Register(ModuleName, 8, "invalid path")
	ErrZeroSupply        = errorsmod.Register(ModuleName, 9, "denom has no supply on this chain")
)
