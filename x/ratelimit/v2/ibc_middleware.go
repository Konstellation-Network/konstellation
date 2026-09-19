// Package v2 is the IBC v2 (client-routed) form of the rate limiter; the
// keeper logic is shared with the v1 middleware.
package v2

import (
	"errors"

	channeltypesv2 "github.com/cosmos/ibc-go/v11/modules/core/04-channel/v2/types"
	ibcapi "github.com/cosmos/ibc-go/v11/modules/core/api"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/Konstellation-Network/konstellation/x/ratelimit/keeper"
	"github.com/Konstellation-Network/konstellation/x/ratelimit/types"
)

var _ ibcapi.IBCModule = &IBCMiddleware{}

// IBCMiddleware is the ICS-20 over IBC v2 rate limiter.
type IBCMiddleware struct {
	app    ibcapi.IBCModule
	keeper keeper.Keeper
}

// NewIBCMiddleware wraps app.
func NewIBCMiddleware(k keeper.Keeper, app ibcapi.IBCModule) *IBCMiddleware {
	if app == nil {
		panic(errors.New("underlying application cannot be nil"))
	}
	return &IBCMiddleware{app: app, keeper: k}
}

// OnSendPacket applies the outflow quota, then hands to the application.
func (im *IBCMiddleware) OnSendPacket(ctx sdk.Context, sourceClient, destinationClient string, sequence uint64, payload channeltypesv2.Payload, signer sdk.AccAddress) error {
	if info, err := keeper.ParsePacketV2(payload, sourceClient, destinationClient, types.PacketSend); err == nil {
		if err := im.keeper.OnSend(ctx, info, sequence); err != nil {
			return err
		}
	}
	return im.app.OnSendPacket(ctx, sourceClient, destinationClient, sequence, payload, signer)
}

// OnRecvPacket applies the inflow quota; over quota is a failed receipt.
func (im *IBCMiddleware) OnRecvPacket(ctx sdk.Context, sourceClient, destinationClient string, sequence uint64, payload channeltypesv2.Payload, relayer sdk.AccAddress) channeltypesv2.RecvPacketResult {
	info, err := keeper.ParsePacketV2(payload, sourceClient, destinationClient, types.PacketRecv)
	if err != nil {
		return im.app.OnRecvPacket(ctx, sourceClient, destinationClient, sequence, payload, relayer)
	}
	if err := im.keeper.OnRecv(ctx, info); err != nil {
		return channeltypesv2.RecvPacketResult{Status: channeltypesv2.PacketStatus_Failure}
	}
	res := im.app.OnRecvPacket(ctx, sourceClient, destinationClient, sequence, payload, relayer)
	if res.Status == channeltypesv2.PacketStatus_Failure {
		if err := im.keeper.UndoRecv(ctx, info.Denom, info.ChannelID, info.Amount); err != nil {
			return channeltypesv2.RecvPacketResult{Status: channeltypesv2.PacketStatus_Failure}
		}
	}
	return res
}

// OnAcknowledgementPacket settles the pending send.
func (im *IBCMiddleware) OnAcknowledgementPacket(ctx sdk.Context, sourceClient, destinationClient string, sequence uint64, acknowledgement []byte, payload channeltypesv2.Payload, relayer sdk.AccAddress) error {
	if info, err := keeper.ParsePacketV2(payload, sourceClient, destinationClient, types.PacketSend); err == nil {
		success, err := keeper.AckSucceeded(acknowledgement)
		if err != nil {
			return err
		}
		if err := im.keeper.OnAck(ctx, info, sequence, success); err != nil {
			return err
		}
	}
	return im.app.OnAcknowledgementPacket(ctx, sourceClient, destinationClient, sequence, acknowledgement, payload, relayer)
}

// OnTimeoutPacket puts the send back.
func (im *IBCMiddleware) OnTimeoutPacket(ctx sdk.Context, sourceClient, destinationClient string, sequence uint64, payload channeltypesv2.Payload, relayer sdk.AccAddress) error {
	if info, err := keeper.ParsePacketV2(payload, sourceClient, destinationClient, types.PacketSend); err == nil {
		if err := im.keeper.OnTimeout(ctx, info, sequence); err != nil {
			return err
		}
	}
	return im.app.OnTimeoutPacket(ctx, sourceClient, destinationClient, sequence, payload, relayer)
}
