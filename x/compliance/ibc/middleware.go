// Package ibc gates incoming ICS-20 transfers by the block list
// (ENGINEERING.md §18 "IBC transfers to a frozen address"). The ante handler
// sees signers, fee payers and direct on-chain recipients; an incoming IBC
// packet is none of those, so without this a frozen address could still be
// funded from another chain. A packet to a frozen receiver is answered with
// an error acknowledgement and the counterparty refunds its sender. Sends
// need nothing here: the sender signed a MsgTransfer, which the ante checks.
package ibc

import (
	"context"
	"errors"

	evmibc "github.com/cosmos/evm/ibc"
	transfertypes "github.com/cosmos/ibc-go/v11/modules/apps/transfer/types"
	channeltypes "github.com/cosmos/ibc-go/v11/modules/core/04-channel/types"
	channeltypesv2 "github.com/cosmos/ibc-go/v11/modules/core/04-channel/v2/types"
	porttypes "github.com/cosmos/ibc-go/v11/modules/core/05-port/types"
	ibcapi "github.com/cosmos/ibc-go/v11/modules/core/api"
	"github.com/cosmos/ibc-go/v11/modules/core/exported"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"

	comptypes "github.com/Konstellation-Network/konstellation/x/compliance/types"
)

// FreezeChecker is the slice of the compliance keeper the gate needs.
type FreezeChecker interface {
	IsFrozen(ctx context.Context, addr []byte) bool
	Enforce(ctx context.Context) bool
}

// checkReceiver rejects a receiver on the block list. A receiver that does
// not parse is left to x/transfer, which rejects it with its own error.
func checkReceiver(ctx sdk.Context, k FreezeChecker, receiver string) error {
	if !k.Enforce(ctx) {
		return nil
	}
	addr, err := comptypes.ParseAddress(receiver)
	if err != nil {
		return nil
	}
	if k.IsFrozen(ctx, addr) {
		return errorsmod.Wrapf(comptypes.ErrAddressFrozen, "receiver %s", comptypes.Bech32(addr))
	}
	return nil
}

// ── v1 ────────────────────────────────────────────────────────────────────

var _ porttypes.IBCModule = &Middleware{}

// Middleware is the ICS-20 v1 gate.
type Middleware struct {
	*evmibc.Module
	app    porttypes.IBCModule
	keeper FreezeChecker
}

// NewMiddleware wraps app.
func NewMiddleware(k FreezeChecker, app porttypes.IBCModule) *Middleware {
	if app == nil {
		panic(errors.New("underlying application cannot be nil"))
	}
	return &Middleware{Module: evmibc.NewModule(app), app: app, keeper: k}
}

// OnRecvPacket refuses a packet to a frozen receiver.
func (m *Middleware) OnRecvPacket(ctx sdk.Context, channelVersion string, packet channeltypes.Packet, relayer sdk.AccAddress) exported.Acknowledgement {
	if data, err := transfertypes.UnmarshalPacketData(packet.GetData(), transfertypes.V1, ""); err == nil {
		if err := checkReceiver(ctx, m.keeper, data.Receiver); err != nil {
			return channeltypes.NewErrorAcknowledgement(err)
		}
	}
	return m.app.OnRecvPacket(ctx, channelVersion, packet, relayer)
}

// ── v2 ────────────────────────────────────────────────────────────────────

var _ ibcapi.IBCModule = &MiddlewareV2{}

// MiddlewareV2 is the ICS-20 over IBC v2 gate.
type MiddlewareV2 struct {
	app    ibcapi.IBCModule
	keeper FreezeChecker
}

// NewMiddlewareV2 wraps app.
func NewMiddlewareV2(k FreezeChecker, app ibcapi.IBCModule) *MiddlewareV2 {
	if app == nil {
		panic(errors.New("underlying application cannot be nil"))
	}
	return &MiddlewareV2{app: app, keeper: k}
}

func (m *MiddlewareV2) OnSendPacket(ctx sdk.Context, sourceClient, destinationClient string, sequence uint64, payload channeltypesv2.Payload, signer sdk.AccAddress) error {
	return m.app.OnSendPacket(ctx, sourceClient, destinationClient, sequence, payload, signer)
}

// OnRecvPacket refuses a packet to a frozen receiver.
func (m *MiddlewareV2) OnRecvPacket(ctx sdk.Context, sourceClient, destinationClient string, sequence uint64, payload channeltypesv2.Payload, relayer sdk.AccAddress) channeltypesv2.RecvPacketResult {
	if data, err := transfertypes.UnmarshalPacketData(payload.Value, payload.Version, payload.Encoding); err == nil {
		if err := checkReceiver(ctx, m.keeper, data.Receiver); err != nil {
			return channeltypesv2.RecvPacketResult{Status: channeltypesv2.PacketStatus_Failure}
		}
	}
	return m.app.OnRecvPacket(ctx, sourceClient, destinationClient, sequence, payload, relayer)
}

func (m *MiddlewareV2) OnAcknowledgementPacket(ctx sdk.Context, sourceClient, destinationClient string, sequence uint64, acknowledgement []byte, payload channeltypesv2.Payload, relayer sdk.AccAddress) error {
	return m.app.OnAcknowledgementPacket(ctx, sourceClient, destinationClient, sequence, acknowledgement, payload, relayer)
}

func (m *MiddlewareV2) OnTimeoutPacket(ctx sdk.Context, sourceClient, destinationClient string, sequence uint64, payload channeltypesv2.Payload, relayer sdk.AccAddress) error {
	return m.app.OnTimeoutPacket(ctx, sourceClient, destinationClient, sequence, payload, relayer)
}
