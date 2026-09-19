package types

import (
	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

var (
	_ sdk.Msg = &MsgAddRateLimit{}
	_ sdk.Msg = &MsgUpdateRateLimit{}
	_ sdk.Msg = &MsgRemoveRateLimit{}
	_ sdk.Msg = &MsgResetRateLimit{}
)

func validateAuthority(a string) error {
	if _, err := sdk.AccAddressFromBech32(a); err != nil {
		return errorsmod.Wrapf(ErrUnauthorized, "authority: %v", err)
	}
	return nil
}

func validatePathAndQuota(denom, channel string, q Quota) error {
	if err := (Path{Denom: denom, ChannelId: channel}).Validate(); err != nil {
		return errorsmod.Wrap(ErrInvalidPath, err.Error())
	}
	if err := q.Validate(); err != nil {
		return errorsmod.Wrap(ErrInvalidQuota, err.Error())
	}
	return nil
}

// Quota is the quota the message asks for.
func (m *MsgAddRateLimit) Quota() Quota {
	return Quota{MaxPercentSend: m.MaxPercentSend, MaxPercentRecv: m.MaxPercentRecv, DurationHours: m.DurationHours}
}

// ValidateBasic checks the message.
func (m *MsgAddRateLimit) ValidateBasic() error {
	if err := validateAuthority(m.Authority); err != nil {
		return err
	}
	return validatePathAndQuota(m.Denom, m.ChannelId, m.Quota())
}

// Quota is the quota the message asks for.
func (m *MsgUpdateRateLimit) Quota() Quota {
	return Quota{MaxPercentSend: m.MaxPercentSend, MaxPercentRecv: m.MaxPercentRecv, DurationHours: m.DurationHours}
}

// ValidateBasic checks the message.
func (m *MsgUpdateRateLimit) ValidateBasic() error {
	if err := validateAuthority(m.Authority); err != nil {
		return err
	}
	return validatePathAndQuota(m.Denom, m.ChannelId, m.Quota())
}

// ValidateBasic checks the message.
func (m *MsgRemoveRateLimit) ValidateBasic() error {
	if err := validateAuthority(m.Authority); err != nil {
		return err
	}
	if err := (Path{Denom: m.Denom, ChannelId: m.ChannelId}).Validate(); err != nil {
		return errorsmod.Wrap(ErrInvalidPath, err.Error())
	}
	return nil
}

// ValidateBasic checks the message.
func (m *MsgResetRateLimit) ValidateBasic() error {
	if err := validateAuthority(m.Authority); err != nil {
		return err
	}
	if err := (Path{Denom: m.Denom, ChannelId: m.ChannelId}).Validate(); err != nil {
		return errorsmod.Wrap(ErrInvalidPath, err.Error())
	}
	return nil
}
