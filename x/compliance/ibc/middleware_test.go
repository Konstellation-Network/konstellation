package ibc_test

import (
	"context"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	transfertypes "github.com/cosmos/ibc-go/v11/modules/apps/transfer/types"
	clienttypes "github.com/cosmos/ibc-go/v11/modules/core/02-client/types"
	channeltypes "github.com/cosmos/ibc-go/v11/modules/core/04-channel/types"
	channeltypesv2 "github.com/cosmos/ibc-go/v11/modules/core/04-channel/v2/types"
	porttypes "github.com/cosmos/ibc-go/v11/modules/core/05-port/types"
	"github.com/cosmos/ibc-go/v11/modules/core/exported"
	ibcmock "github.com/cosmos/ibc-go/v11/testing/mock"

	sdk "github.com/cosmos/cosmos-sdk/types"

	complianceibc "github.com/Konstellation-Network/konstellation/x/compliance/ibc"
	comptypes "github.com/Konstellation-Network/konstellation/x/compliance/types"
)

var (
	frozen = common.HexToAddress("0x1111111111111111111111111111111111111111")
	clean  = common.HexToAddress("0x2222222222222222222222222222222222222222")
)

const transferPort = "transfer"

type fakeChecker struct {
	frozen  map[string]bool
	enforce bool
}

func (c fakeChecker) IsFrozen(_ context.Context, addr []byte) bool { return c.frozen[string(addr)] }
func (c fakeChecker) Enforce(context.Context) bool                 { return c.enforce }

type recordingApp struct {
	ibcmock.IBCModule
	recv int
	// marked records whether each callback ran under the protocol mark.
	marked map[string]bool
}

func (a *recordingApp) OnRecvPacket(ctx sdk.Context, _ string, _ channeltypes.Packet, _ sdk.AccAddress) exported.Acknowledgement {
	a.recv++
	a.mark("recv", ctx)
	return channeltypes.NewResultAcknowledgement([]byte{1})
}

func (a *recordingApp) OnAcknowledgementPacket(ctx sdk.Context, _ string, _ channeltypes.Packet, _ []byte, _ sdk.AccAddress) error {
	a.mark("ack", ctx)
	return nil
}

func (a *recordingApp) OnTimeoutPacket(ctx sdk.Context, _ string, _ channeltypes.Packet, _ sdk.AccAddress) error {
	a.mark("timeout", ctx)
	return nil
}

func (a *recordingApp) mark(cb string, ctx sdk.Context) {
	if a.marked == nil {
		a.marked = map[string]bool{}
	}
	a.marked[cb] = comptypes.IsProtocolFlow(ctx)
}

func (a *recordingApp) SetICS4Wrapper(porttypes.ICS4Wrapper) {}

type recordingAppV2 struct {
	recv   int
	marked map[string]bool
}

func (a *recordingAppV2) mark(cb string, ctx sdk.Context) {
	if a.marked == nil {
		a.marked = map[string]bool{}
	}
	a.marked[cb] = comptypes.IsProtocolFlow(ctx)
}

func (a *recordingAppV2) OnSendPacket(sdk.Context, string, string, uint64, channeltypesv2.Payload, sdk.AccAddress) error {
	return nil
}

func (a *recordingAppV2) OnRecvPacket(ctx sdk.Context, _, _ string, _ uint64, _ channeltypesv2.Payload, _ sdk.AccAddress) channeltypesv2.RecvPacketResult {
	a.recv++
	a.mark("recv", ctx)
	return channeltypesv2.RecvPacketResult{Status: channeltypesv2.PacketStatus_Success}
}

func (a *recordingAppV2) OnTimeoutPacket(ctx sdk.Context, _, _ string, _ uint64, _ channeltypesv2.Payload, _ sdk.AccAddress) error {
	a.mark("timeout", ctx)
	return nil
}

func (a *recordingAppV2) OnAcknowledgementPacket(ctx sdk.Context, _, _ string, _ uint64, _ []byte, _ channeltypesv2.Payload, _ sdk.AccAddress) error {
	a.mark("ack", ctx)
	return nil
}

func packetTo(receiver string) channeltypes.Packet {
	data := transfertypes.NewFungibleTokenPacketData("uatom", "1", "sender", receiver, "").GetBytes()
	return channeltypes.NewPacket(data, 1, transferPort, "channel-9", transferPort, "channel-0", clienttypes.Height{}, 0)
}

func TestRecvGate(t *testing.T) {
	ctx := sdk.Context{}.WithContext(context.Background())
	checker := fakeChecker{frozen: map[string]bool{string(frozen.Bytes()): true}, enforce: true}
	app := &recordingApp{}
	mw := complianceibc.NewMiddleware(checker, app)

	// Frozen receiver, in either address form: error ack, app never sees it.
	for _, r := range []string{sdk.AccAddress(frozen.Bytes()).String(), frozen.Hex()} {
		ack := mw.OnRecvPacket(ctx, "ics20-1", packetTo(r), nil)
		require.False(t, ack.Success(), r)
	}
	require.Zero(t, app.recv)

	// Clean receiver passes.
	require.True(t, mw.OnRecvPacket(ctx, "ics20-1", packetTo(sdk.AccAddress(clean.Bytes()).String()), nil).Success())
	require.Equal(t, 1, app.recv)

	// A receiver that does not parse is x/transfer's problem, not ours.
	require.True(t, mw.OnRecvPacket(ctx, "ics20-1", packetTo("not-an-address"), nil).Success())
	require.Equal(t, 2, app.recv)

	// Enforce off: the gate is inert, like the ante check.
	off := complianceibc.NewMiddleware(fakeChecker{frozen: checker.frozen, enforce: false}, app)
	require.True(t, off.OnRecvPacket(ctx, "ics20-1", packetTo(sdk.AccAddress(frozen.Bytes()).String()), nil).Success())

	// v2
	appV2 := &recordingAppV2{}
	mwV2 := complianceibc.NewMiddlewareV2(checker, appV2)
	payload := func(receiver string) channeltypesv2.Payload {
		return channeltypesv2.Payload{
			SourcePort: transferPort, DestinationPort: transferPort, Version: transfertypes.V1, Encoding: transfertypes.EncodingJSON,
			Value: transfertypes.NewFungibleTokenPacketData("uatom", "1", "sender", receiver, "").GetBytes(),
		}
	}
	require.Equal(t, channeltypesv2.PacketStatus_Failure, mwV2.OnRecvPacket(ctx, "c9", "c0", 1, payload(frozen.Hex()), nil).Status)
	require.Zero(t, appV2.recv)
	require.Equal(t, channeltypesv2.PacketStatus_Success, mwV2.OnRecvPacket(ctx, "c9", "c0", 2, payload(clean.Hex()), nil).Status)
	require.Equal(t, 1, appV2.recv)
}

// TestRefundMarker: the transfer module's ack and timeout callbacks run
// under the protocol mark (so its refund can land on a since-frozen
// sender); receive does not, and neither does anything above the marker —
// the receive gate wrapping it passes an unmarked context down.
func TestRefundMarker(t *testing.T) {
	ctx := sdk.Context{}.WithContext(context.Background())
	app := &recordingApp{}
	mw := complianceibc.NewMiddleware(fakeChecker{enforce: true}, complianceibc.NewRefundMarker(app))
	pkt := packetTo(sdk.AccAddress(clean.Bytes()).String())

	require.True(t, mw.OnRecvPacket(ctx, "ics20-1", pkt, nil).Success())
	require.NoError(t, mw.OnAcknowledgementPacket(ctx, "ics20-1", pkt, []byte{1}, nil))
	require.NoError(t, mw.OnTimeoutPacket(ctx, "ics20-1", pkt, nil))
	require.Equal(t, map[string]bool{"recv": false, "ack": true, "timeout": true}, app.marked)
	require.False(t, comptypes.IsProtocolFlow(ctx), "mark leaked into the caller's context")

	appV2 := &recordingAppV2{}
	mwV2 := complianceibc.NewMiddlewareV2(fakeChecker{enforce: true}, complianceibc.NewRefundMarkerV2(appV2))
	payload := channeltypesv2.Payload{
		SourcePort: transferPort, DestinationPort: transferPort, Version: transfertypes.V1, Encoding: transfertypes.EncodingJSON,
		Value: transfertypes.NewFungibleTokenPacketData("uatom", "1", "sender", clean.Hex(), "").GetBytes(),
	}
	require.Equal(t, channeltypesv2.PacketStatus_Success, mwV2.OnRecvPacket(ctx, "c9", "c0", 1, payload, nil).Status)
	require.NoError(t, mwV2.OnAcknowledgementPacket(ctx, "c9", "c0", 1, []byte{1}, payload, nil))
	require.NoError(t, mwV2.OnTimeoutPacket(ctx, "c9", "c0", 1, payload, nil))
	require.Equal(t, map[string]bool{"recv": false, "ack": true, "timeout": true}, appV2.marked)
}
