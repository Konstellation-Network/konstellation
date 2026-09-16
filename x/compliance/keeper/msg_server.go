package keeper

import (
	"context"
	"time"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/Konstellation-Network/konstellation/x/compliance/types"
)

var _ types.MsgServer = msgServer{}

type msgServer struct{ k Keeper }

// NewMsgServerImpl returns the Msg service implementation.
func NewMsgServerImpl(k Keeper) types.MsgServer { return msgServer{k} }

// ScheduleUpdate: list authority or governance; timelocked.
func (m msgServer) ScheduleUpdate(ctx context.Context, msg *types.MsgScheduleUpdate) (*types.MsgScheduleUpdateResponse, error) {
	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}
	if err := m.k.requireAuthorityOrGov(ctx, msg.Authority); err != nil {
		return nil, err
	}
	p, err := m.k.schedule(ctx, msg.Changes, msg.Authority)
	if err != nil {
		return nil, err
	}
	return &types.MsgScheduleUpdateResponse{Id: p.Id, ExecuteAt: p.ExecuteAt}, nil
}

// CancelUpdate: list authority or governance.
func (m msgServer) CancelUpdate(ctx context.Context, msg *types.MsgCancelUpdate) (*types.MsgCancelUpdateResponse, error) {
	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}
	if err := m.k.requireAuthorityOrGov(ctx, msg.Authority); err != nil {
		return nil, err
	}
	if err := m.k.cancel(ctx, msg.Id, msg.Authority); err != nil {
		return nil, err
	}
	return &types.MsgCancelUpdateResponse{}, nil
}

// EmergencyFreeze: list authority or governance; immediate, expires after
// one timelock unless a permanent entry lands first.
func (m msgServer) EmergencyFreeze(ctx context.Context, msg *types.MsgEmergencyFreeze) (*types.MsgEmergencyFreezeResponse, error) {
	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}
	if err := m.k.requireAuthorityOrGov(ctx, msg.Authority); err != nil {
		return nil, err
	}
	expiresAt := sdk.UnwrapSDKContext(ctx).BlockTime().Add(m.k.GetParams(ctx).Timelock).Truncate(time.Second)
	for _, a := range msg.Addresses {
		addr, err := types.ParseAddress(a)
		if err != nil {
			return nil, errorsmod.Wrap(types.ErrInvalidChange, err.Error())
		}
		if err := m.k.emergencyFreeze(ctx, addr, msg.Reason, msg.Authority, expiresAt); err != nil {
			return nil, err
		}
	}
	return &types.MsgEmergencyFreezeResponse{ExpiresAt: expiresAt}, nil
}

// LiftEmergencyFreeze: list authority or governance; only temporary entries.
func (m msgServer) LiftEmergencyFreeze(ctx context.Context, msg *types.MsgLiftEmergencyFreeze) (*types.MsgLiftEmergencyFreezeResponse, error) {
	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}
	if err := m.k.requireAuthorityOrGov(ctx, msg.Authority); err != nil {
		return nil, err
	}
	for _, a := range msg.Addresses {
		addr, err := types.ParseAddress(a)
		if err != nil {
			return nil, errorsmod.Wrap(types.ErrInvalidChange, err.Error())
		}
		if err := m.k.liftEmergencyFreeze(ctx, addr, msg.Authority); err != nil {
			return nil, err
		}
	}
	return &types.MsgLiftEmergencyFreezeResponse{}, nil
}

// GovUpdate: governance only; immediate. The override.
func (m msgServer) GovUpdate(ctx context.Context, msg *types.MsgGovUpdate) (*types.MsgGovUpdateResponse, error) {
	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}
	if !m.k.isGov(msg.Authority) {
		return nil, errorsmod.Wrapf(types.ErrUnauthorized, "expected %s, got %s", m.k.govAuthority, msg.Authority)
	}
	for _, c := range msg.Changes {
		if err := m.k.applyChange(ctx, c, msg.Authority); err != nil {
			return nil, err
		}
	}
	return &types.MsgGovUpdateResponse{}, nil
}

// UpdateParams: governance only.
func (m msgServer) UpdateParams(ctx context.Context, msg *types.MsgUpdateParams) (*types.MsgUpdateParamsResponse, error) {
	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}
	if !m.k.isGov(msg.Authority) {
		return nil, errorsmod.Wrapf(types.ErrUnauthorized, "expected %s, got %s", m.k.govAuthority, msg.Authority)
	}
	// An authority that is itself frozen could not act (its txs would be
	// rejected by the ante check). Refuse rather than install a dead one.
	if msg.Params.Authority != "" {
		addr, _ := types.ParseAddress(msg.Params.Authority)
		if m.k.IsFrozen(ctx, addr) {
			return nil, errorsmod.Wrapf(types.ErrAddressFrozen, "new authority %s is on the block list", msg.Params.Authority)
		}
	}
	if err := m.k.Params.Set(ctx, msg.Params); err != nil {
		return nil, err
	}
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(types.EventTypeParamsUpdated,
		sdk.NewAttribute(types.AttributeKeyAuthority, msg.Params.Authority),
		sdk.NewAttribute("timelock", msg.Params.Timelock.String()),
		sdk.NewAttribute("allowlist_add_timelock", msg.Params.AllowlistAddTimelock.String()),
		sdk.NewAttribute("enforce", boolStr(msg.Params.Enforce)),
	))
	return &types.MsgUpdateParamsResponse{}, nil
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
