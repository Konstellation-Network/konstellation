package types

import errorsmod "cosmossdk.io/errors"

var (
	ErrUnauthorized       = errorsmod.Register(ModuleName, 2, "signer is neither the list authority nor governance")
	ErrNoAuthority        = errorsmod.Register(ModuleName, 3, "no list authority is set; only governance can act")
	ErrInvalidChange      = errorsmod.Register(ModuleName, 4, "invalid change")
	ErrPendingNotFound    = errorsmod.Register(ModuleName, 5, "pending update not found")
	ErrAddressFrozen      = errorsmod.Register(ModuleName, 6, "address is frozen")
	ErrNotEmergencyFrozen = errorsmod.Register(ModuleName, 7, "address is not under an emergency freeze")
	ErrTooMany            = errorsmod.Register(ModuleName, 8, "too many items in one message")
	ErrReasonTooLong      = errorsmod.Register(ModuleName, 9, "reason too long")
	ErrEmergencyCooldown  = errorsmod.Register(ModuleName, 10, "address is under an emergency freeze or its cooldown; ratify through a scheduled update or governance")
	ErrProtectedAddress   = errorsmod.Register(ModuleName, 11, "address cannot be frozen: module account, precompile, IBC escrow, governance or the list authority")
	ErrGovScheduled       = errorsmod.Register(ModuleName, 12, "update was scheduled by governance; only governance can cancel it")
)
