package types

import (
	"fmt"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// Bounds. One message is one governance-visible action; keep it readable.
const (
	MaxChangesPerMsg   = 100
	MaxAddressesPerMsg = 100
	MaxReasonLength    = 256
)

var (
	_ sdk.Msg = &MsgScheduleUpdate{}
	_ sdk.Msg = &MsgCancelUpdate{}
	_ sdk.Msg = &MsgEmergencyFreeze{}
	_ sdk.Msg = &MsgLiftEmergencyFreeze{}
	_ sdk.Msg = &MsgGovUpdate{}
	_ sdk.Msg = &MsgUpdateParams{}
)

// Validate checks one change: a parseable address, a real list, a real
// action, a bounded reason.
func (c Change) Validate() error {
	if _, err := ParseAddress(c.Address); err != nil {
		return errorsmod.Wrap(ErrInvalidChange, err.Error())
	}
	if c.List != LIST_ALLOW && c.List != LIST_BLOCK {
		return errorsmod.Wrapf(ErrInvalidChange, "list %s", c.List)
	}
	if c.Action != ACTION_ADD && c.Action != ACTION_REMOVE {
		return errorsmod.Wrapf(ErrInvalidChange, "action %s", c.Action)
	}
	if len(c.Reason) > MaxReasonLength {
		return errorsmod.Wrapf(ErrReasonTooLong, "%d > %d", len(c.Reason), MaxReasonLength)
	}
	return nil
}

func validateChanges(changes []Change) error {
	if len(changes) == 0 {
		return errorsmod.Wrap(ErrInvalidChange, "no changes")
	}
	if len(changes) > MaxChangesPerMsg {
		return errorsmod.Wrapf(ErrTooMany, "%d changes > %d", len(changes), MaxChangesPerMsg)
	}
	seen := make(map[string]struct{}, len(changes))
	for i, c := range changes {
		if err := c.Validate(); err != nil {
			return fmt.Errorf("change %d: %w", i, err)
		}
		addr, _ := ParseAddress(c.Address)
		k := fmt.Sprintf("%x/%d", addr, c.List)
		if _, dup := seen[k]; dup {
			return errorsmod.Wrapf(ErrInvalidChange, "change %d: duplicate address/list", i)
		}
		seen[k] = struct{}{}
	}
	return nil
}

func validateAddresses(addrs []string) error {
	if len(addrs) == 0 {
		return errorsmod.Wrap(ErrInvalidChange, "no addresses")
	}
	if len(addrs) > MaxAddressesPerMsg {
		return errorsmod.Wrapf(ErrTooMany, "%d addresses > %d", len(addrs), MaxAddressesPerMsg)
	}
	for i, a := range addrs {
		if _, err := ParseAddress(a); err != nil {
			return errorsmod.Wrapf(ErrInvalidChange, "address %d: %s", i, err)
		}
	}
	return nil
}

func validateSigner(s string) error {
	if _, err := sdk.AccAddressFromBech32(s); err != nil {
		return errorsmod.Wrapf(ErrUnauthorized, "invalid authority address: %s", err)
	}
	return nil
}

func (m MsgScheduleUpdate) ValidateBasic() error {
	if err := validateSigner(m.Authority); err != nil {
		return err
	}
	return validateChanges(m.Changes)
}

func (m MsgCancelUpdate) ValidateBasic() error {
	return validateSigner(m.Authority)
}

func (m MsgEmergencyFreeze) ValidateBasic() error {
	if err := validateSigner(m.Authority); err != nil {
		return err
	}
	if len(m.Reason) > MaxReasonLength {
		return errorsmod.Wrapf(ErrReasonTooLong, "%d > %d", len(m.Reason), MaxReasonLength)
	}
	return validateAddresses(m.Addresses)
}

func (m MsgLiftEmergencyFreeze) ValidateBasic() error {
	if err := validateSigner(m.Authority); err != nil {
		return err
	}
	return validateAddresses(m.Addresses)
}

func (m MsgGovUpdate) ValidateBasic() error {
	if err := validateSigner(m.Authority); err != nil {
		return err
	}
	return validateChanges(m.Changes)
}

func (m MsgUpdateParams) ValidateBasic() error {
	if err := validateSigner(m.Authority); err != nil {
		return err
	}
	return m.Params.Validate()
}
