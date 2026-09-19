package keeper

import (
	"context"

	errorsmod "cosmossdk.io/errors"

	"github.com/Konstellation-Network/konstellation/x/ratelimit/types"
)

var _ types.MsgServer = msgServer{}

type msgServer struct{ k Keeper }

// NewMsgServerImpl returns the Msg service implementation.
func NewMsgServerImpl(k Keeper) types.MsgServer { return msgServer{k} }

func (m msgServer) requireGov(signer string) error {
	if signer != m.k.govAuthority {
		return errorsmod.Wrapf(types.ErrUnauthorized, "expected %s, got %s", m.k.govAuthority, signer)
	}
	return nil
}

func (m msgServer) AddRateLimit(ctx context.Context, msg *types.MsgAddRateLimit) (*types.MsgAddRateLimitResponse, error) {
	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}
	if err := m.requireGov(msg.Authority); err != nil {
		return nil, err
	}
	if err := m.k.addRateLimit(ctx, types.Path{Denom: msg.Denom, ChannelId: msg.ChannelId}, msg.Quota(), msg.Authority); err != nil {
		return nil, err
	}
	return &types.MsgAddRateLimitResponse{}, nil
}

func (m msgServer) UpdateRateLimit(ctx context.Context, msg *types.MsgUpdateRateLimit) (*types.MsgUpdateRateLimitResponse, error) {
	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}
	if err := m.requireGov(msg.Authority); err != nil {
		return nil, err
	}
	if err := m.k.updateRateLimit(ctx, types.Path{Denom: msg.Denom, ChannelId: msg.ChannelId}, msg.Quota(), msg.Authority); err != nil {
		return nil, err
	}
	return &types.MsgUpdateRateLimitResponse{}, nil
}

func (m msgServer) RemoveRateLimit(ctx context.Context, msg *types.MsgRemoveRateLimit) (*types.MsgRemoveRateLimitResponse, error) {
	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}
	if err := m.requireGov(msg.Authority); err != nil {
		return nil, err
	}
	if err := m.k.removeRateLimit(ctx, types.Path{Denom: msg.Denom, ChannelId: msg.ChannelId}, msg.Authority); err != nil {
		return nil, err
	}
	return &types.MsgRemoveRateLimitResponse{}, nil
}

func (m msgServer) ResetRateLimit(ctx context.Context, msg *types.MsgResetRateLimit) (*types.MsgResetRateLimitResponse, error) {
	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}
	if err := m.requireGov(msg.Authority); err != nil {
		return nil, err
	}
	if err := m.k.resetRateLimit(ctx, types.Path{Denom: msg.Denom, ChannelId: msg.ChannelId}, msg.Authority); err != nil {
		return nil, err
	}
	return &types.MsgResetRateLimitResponse{}, nil
}
