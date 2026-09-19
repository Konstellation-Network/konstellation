package keeper

import (
	"bytes"
	"context"

	transfertypes "github.com/cosmos/ibc-go/v11/modules/apps/transfer/types"
	channeltypes "github.com/cosmos/ibc-go/v11/modules/core/04-channel/types"
	channeltypesv2 "github.com/cosmos/ibc-go/v11/modules/core/04-channel/v2/types"

	errorsmod "cosmossdk.io/errors"
	sdkmath "cosmossdk.io/math"

	"github.com/Konstellation-Network/konstellation/x/ratelimit/types"
)

// PacketInfo is what the limiter needs from an ICS-20 packet: the denom as
// it exists on this chain, the local channel (or client) id, and the amount.
type PacketInfo struct {
	Denom     string
	ChannelID string
	Amount    sdkmath.Int
	Sender    string
	Receiver  string
}

// ParsePacketV1 extracts PacketInfo from an ICS-20 v1 packet.
//
// The local denom follows x/transfer's own rules exactly: on send the packet
// carries the denom as it exists here, so its IBCDenom() is the local form
// (base denom for native, ibc/<hash> for vouchers). On receive, a denom
// prefixed with our source port/channel is a token coming home — strip the
// hop and use what is left; anything else is a foreign token and gets our
// destination port/channel prepended, which is the voucher x/transfer will
// mint. Diverging from x/transfer here would count against the wrong path.
func ParsePacketV1(packet channeltypes.Packet, direction types.PacketDirection) (PacketInfo, error) {
	data, err := transfertypes.UnmarshalPacketData(packet.GetData(), transfertypes.V1, "")
	if err != nil {
		return PacketInfo{}, errorsmod.Wrap(types.ErrInvalidPacket, err.Error())
	}
	return packetInfo(data, direction,
		packet.GetSourcePort(), packet.GetSourceChannel(),
		packet.GetDestPort(), packet.GetDestChannel())
}

// ParsePacketV2 extracts PacketInfo from an ICS-20 packet carried as an
// IBC v2 payload; the client ids play the channel's role.
func ParsePacketV2(payload channeltypesv2.Payload, sourceClient, destinationClient string, direction types.PacketDirection) (PacketInfo, error) {
	data, err := transfertypes.UnmarshalPacketData(payload.Value, payload.Version, payload.Encoding)
	if err != nil {
		return PacketInfo{}, errorsmod.Wrap(types.ErrInvalidPacket, err.Error())
	}
	return packetInfo(data, direction,
		payload.SourcePort, sourceClient,
		payload.DestinationPort, destinationClient)
}

func packetInfo(data transfertypes.InternalTransferRepresentation, direction types.PacketDirection, srcPort, srcChannel, dstPort, dstChannel string) (PacketInfo, error) {
	amount, ok := sdkmath.NewIntFromString(data.Token.Amount)
	if !ok {
		return PacketInfo{}, errorsmod.Wrapf(types.ErrInvalidPacket, "amount %q", data.Token.Amount)
	}
	denom := data.Token.Denom
	info := PacketInfo{Amount: amount, Sender: data.Sender, Receiver: data.Receiver}
	switch direction {
	case types.PacketSend:
		info.ChannelID = srcChannel
		info.Denom = denom.IBCDenom()
	default:
		info.ChannelID = dstChannel
		if denom.HasPrefix(srcPort, srcChannel) {
			denom.Trace = denom.Trace[1:]
		} else {
			denom.Trace = append([]transfertypes.Hop{transfertypes.NewHop(dstPort, dstChannel)}, denom.Trace...)
		}
		info.Denom = denom.IBCDenom()
	}
	return info, nil
}

// OnSend gates an outgoing transfer and records it as pending. Called after
// core has assigned the sequence; an error here fails the whole tx, so the
// sequence and the escrow are rolled back with it.
func (k Keeper) OnSend(ctx context.Context, info PacketInfo, sequence uint64) error {
	limited, err := k.CheckAndRecord(ctx, types.PacketSend, info.Denom, info.ChannelID, info.Amount)
	if err != nil || !limited {
		return err
	}
	return k.MarkPending(ctx, info.Denom, info.ChannelID, sequence)
}

// OnRecv gates an incoming transfer.
func (k Keeper) OnRecv(ctx context.Context, info PacketInfo) error {
	_, err := k.CheckAndRecord(ctx, types.PacketRecv, info.Denom, info.ChannelID, info.Amount)
	return err
}

// OnAck settles a pending send: success clears it, failure puts the amount
// back.
func (k Keeper) OnAck(ctx context.Context, info PacketInfo, sequence uint64, success bool) error {
	if success {
		return k.ClearPending(ctx, info.Denom, info.ChannelID, sequence)
	}
	return k.UndoSend(ctx, info.Denom, info.ChannelID, sequence, info.Amount)
}

// OnTimeout puts a timed-out send back.
func (k Keeper) OnTimeout(ctx context.Context, info PacketInfo, sequence uint64) error {
	return k.UndoSend(ctx, info.Denom, info.ChannelID, sequence, info.Amount)
}

// AckSucceeded reports whether an ICS-20 acknowledgement is a success. The
// IBC v2 universal error acknowledgement counts as failure, as x/transfer
// treats it.
func AckSucceeded(ackBz []byte) (bool, error) {
	if bytes.Equal(ackBz, channeltypesv2.ErrorAcknowledgement[:]) {
		return false, nil
	}
	var ack channeltypes.Acknowledgement
	if err := transfertypes.ModuleCdc.UnmarshalJSON(ackBz, &ack); err != nil {
		return false, errorsmod.Wrapf(types.ErrInvalidPacket, "acknowledgement: %v", err)
	}
	return ack.Success(), nil
}
