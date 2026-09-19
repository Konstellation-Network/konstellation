package types

// Every list mutation and every scheduling action emits one of these, so the
// full history of who froze whom, when, and why is reconstructible from
// events alone (ENGINEERING.md §10: "all actions logged on-chain").
const (
	EventTypeUpdateScheduled = "compliance_update_scheduled"
	EventTypeUpdateCancelled = "compliance_update_cancelled"
	EventTypeUpdateExecuted  = "compliance_update_executed"
	EventTypeEntryAdded      = "compliance_entry_added"
	EventTypeEntryRemoved    = "compliance_entry_removed"
	EventTypeEntryExpired    = "compliance_entry_expired"
	EventTypeEmergencyFreeze = "compliance_emergency_freeze"
	EventTypeEmergencyLifted = "compliance_emergency_lifted"
	EventTypeParamsUpdated   = "compliance_params_updated"
	// EventTypeDelegationReset: a block-list add found an EIP-7702 delegation
	// on the address and removed it (keeper/delegation.go).
	EventTypeDelegationReset = "compliance_delegation_reset"

	AttributeKeyID        = "id"
	AttributeKeyAddress   = "address"
	AttributeKeyList      = "list"
	AttributeKeyAction    = "action"
	AttributeKeyReason    = "reason"
	AttributeKeyExecuteAt = "execute_at"
	AttributeKeyExpiresAt = "expires_at"
	AttributeKeyBy        = "by"
	AttributeKeyAuthority = "authority"
	AttributeKeyDelegate  = "delegate"
)
