// Package ratelimit is safety rail 2 (ENGINEERING.md §13.2): an IBC
// middleware that caps the net value moving over each channel, per denom,
// per time window. Governance sets the quotas (x/ratelimit/keeper); this
// package is the ICS-26 and ICS-4 plumbing that applies them.
//
// It sits outermost in the transfer stack — the first thing core IBC hands
// a packet to and the last thing a send passes through — so it sees packets
// as x/transfer will see them, before the erc20 and callbacks middlewares.
//
// Written here because no rate-limiting module exists for ibc-go v11 (the
// ibc-apps one is v10-only, decided 2026-09-19); modelled on its semantics:
// percentage-of-supply quotas, net flow, pending sends undone on error ack
// or timeout within the same window. Two deliberate simplifications: no
// address whitelist, and no async-ack tracking. The second is safe only
// because nothing in our stack acknowledges asynchronously (a nil ack from
// OnRecvPacket): if an app that does is ever added and it later fails, the
// inflow stays counted, and under net-flow accounting a phantom inflow
// *widens* the send allowance by that amount — it is not merely stricter.
// Adding such an app means adding WriteAcknowledgement tracking here.
package ratelimit

import (
	"errors"

	"github.com/cosmos/evm/ibc"
	clienttypes "github.com/cosmos/ibc-go/v11/modules/core/02-client/types"
	channeltypes "github.com/cosmos/ibc-go/v11/modules/core/04-channel/types"
	porttypes "github.com/cosmos/ibc-go/v11/modules/core/05-port/types"
	"github.com/cosmos/ibc-go/v11/modules/core/exported"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/Konstellation-Network/konstellation/x/ratelimit/keeper"
	"github.com/Konstellation-Network/konstellation/x/ratelimit/types"
)

var (
	_ porttypes.Middleware            = &IBCMiddleware{}
	_ porttypes.PacketDataUnmarshaler = &IBCMiddleware{}
)

// IBCMiddleware is the ICS-20 v1 rate limiter.
type IBCMiddleware struct {
	*ibc.Module
	app         porttypes.IBCModule
	keeper      keeper.Keeper
	ics4Wrapper porttypes.ICS4Wrapper
}

// NewIBCMiddleware wraps app. The ICS4 wrapper (core's channel keeper, or
// the next middleware up) is set with SetICS4Wrapper.
func NewIBCMiddleware(k keeper.Keeper, app porttypes.IBCModule) *IBCMiddleware {
	if app == nil {
		panic(errors.New("underlying application cannot be nil"))
	}
	return &IBCMiddleware{Module: ibc.NewModule(app), app: app, keeper: k}
}

// SetICS4Wrapper sets what SendPacket forwards to.
func (im *IBCMiddleware) SetICS4Wrapper(wrapper porttypes.ICS4Wrapper) {
	if wrapper == nil {
		panic(errors.New("ICS4Wrapper cannot be nil"))
	}
	im.ics4Wrapper = wrapper
}

// SetUnderlyingApplication sets the app below.
func (im *IBCMiddleware) SetUnderlyingApplication(app porttypes.IBCModule) {
	if app == nil {
		panic(errors.New("underlying application cannot be nil"))
	}
	im.app = app
	im.Module = ibc.NewModule(app)
}

// OnRecvPacket refuses an inflow over quota with an error acknowledgement —
// the counterparty refunds its sender — and otherwise counts it. If the
// application then rejects the packet, the count is undone. That undo is
// belt-and-braces: core IBC runs OnRecvPacket in a cache context and
// discards its writes on an unsuccessful ack, so the count would be
// dropped anyway; it is kept so this module's state is right on its own
// terms and does not depend on the caller's context handling.
func (im *IBCMiddleware) OnRecvPacket(ctx sdk.Context, channelVersion string, packet channeltypes.Packet, relayer sdk.AccAddress) exported.Acknowledgement {
	info, err := keeper.ParsePacketV1(packet, types.PacketRecv)
	if err != nil {
		// Not an ICS-20 packet we understand: not ours to judge.
		return im.app.OnRecvPacket(ctx, channelVersion, packet, relayer)
	}
	if err := im.keeper.OnRecv(ctx, info); err != nil {
		return channeltypes.NewErrorAcknowledgement(err)
	}
	ack := im.app.OnRecvPacket(ctx, channelVersion, packet, relayer)
	if ack != nil && !ack.Success() {
		if err := im.keeper.UndoRecv(ctx, info.Denom, info.ChannelID, info.Amount); err != nil {
			return channeltypes.NewErrorAcknowledgement(err)
		}
	}
	return ack
}

// OnAcknowledgementPacket settles the pending send before the application
// sees the ack.
func (im *IBCMiddleware) OnAcknowledgementPacket(ctx sdk.Context, channelVersion string, packet channeltypes.Packet, acknowledgement []byte, relayer sdk.AccAddress) error {
	if info, err := keeper.ParsePacketV1(packet, types.PacketSend); err == nil {
		success, err := keeper.AckSucceeded(acknowledgement)
		if err != nil {
			return err
		}
		if err := im.keeper.OnAck(ctx, info, packet.Sequence, success); err != nil {
			return err
		}
	}
	return im.app.OnAcknowledgementPacket(ctx, channelVersion, packet, acknowledgement, relayer)
}

// OnTimeoutPacket puts the send back before the application refunds it.
func (im *IBCMiddleware) OnTimeoutPacket(ctx sdk.Context, channelVersion string, packet channeltypes.Packet, relayer sdk.AccAddress) error {
	if info, err := keeper.ParsePacketV1(packet, types.PacketSend); err == nil {
		if err := im.keeper.OnTimeout(ctx, info, packet.Sequence); err != nil {
			return err
		}
	}
	return im.app.OnTimeoutPacket(ctx, channelVersion, packet, relayer)
}

// SendPacket lets core assign the sequence, then applies the quota. An
// error fails the transfer tx, which rolls the sequence back.
func (im *IBCMiddleware) SendPacket(ctx sdk.Context, sourcePort, sourceChannel string, timeoutHeight clienttypes.Height, timeoutTimestamp uint64, data []byte) (uint64, error) {
	sequence, err := im.ics4Wrapper.SendPacket(ctx, sourcePort, sourceChannel, timeoutHeight, timeoutTimestamp, data)
	if err != nil {
		return sequence, err
	}
	info, err := keeper.ParsePacketV1(channeltypes.Packet{
		Sequence: sequence, SourcePort: sourcePort, SourceChannel: sourceChannel, Data: data,
	}, types.PacketSend)
	if err != nil {
		// Not ICS-20: nothing to limit.
		return sequence, nil
	}
	if err := im.keeper.OnSend(ctx, info, sequence); err != nil {
		return 0, errorsmod.Wrap(err, "ICS-20 send refused")
	}
	return sequence, nil
}

// WriteAcknowledgement passes through.
func (im *IBCMiddleware) WriteAcknowledgement(ctx sdk.Context, packet exported.PacketI, ack exported.Acknowledgement) error {
	return im.ics4Wrapper.WriteAcknowledgement(ctx, packet, ack)
}

// GetAppVersion passes through.
func (im *IBCMiddleware) GetAppVersion(ctx sdk.Context, portID, channelID string) (string, bool) {
	return im.ics4Wrapper.GetAppVersion(ctx, portID, channelID)
}
