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
)

var (
	frozen = common.HexToAddress("0x1111111111111111111111111111111111111111")
	clean  = common.HexToAddress("0x2222222222222222222222222222222222222222")
)

type fakeChecker struct {
	frozen  map[string]bool
	enforce bool
}

func (c fakeChecker) IsFrozen(_ context.Context, addr []byte) bool { return c.frozen[string(addr)] }
func (c fakeChecker) Enforce(context.Context) bool                 { return c.enforce }

type recordingApp struct {
	ibcmock.IBCModule
	recv int
}

func (a *recordingApp) OnRecvPacket(sdk.Context, string, channeltypes.Packet, sdk.AccAddress) exported.Acknowledgement {
	a.recv++
	return channeltypes.NewResultAcknowledgement([]byte{1})
}

func (a *recordingApp) SetICS4Wrapper(porttypes.ICS4Wrapper) {}

type recordingAppV2 struct{ recv int }

func (a *recordingAppV2) OnSendPacket(sdk.Context, string, string, uint64, channeltypesv2.Payload, sdk.AccAddress) error {
	return nil
}

func (a *recordingAppV2) OnRecvPacket(sdk.Context, string, string, uint64, channeltypesv2.Payload, sdk.AccAddress) channeltypesv2.RecvPacketResult {
	a.recv++
	return channeltypesv2.RecvPacketResult{Status: channeltypesv2.PacketStatus_Success}
}

func (a *recordingAppV2) OnTimeoutPacket(sdk.Context, string, string, uint64, channeltypesv2.Payload, sdk.AccAddress) error {
	return nil
}

func (a *recordingAppV2) OnAcknowledgementPacket(sdk.Context, string, string, uint64, []byte, channeltypesv2.Payload, sdk.AccAddress) error {
	return nil
}

func packetTo(receiver string) channeltypes.Packet {
	data := transfertypes.NewFungibleTokenPacketData("uatom", "1", "sender", receiver, "").GetBytes()
	return channeltypes.NewPacket(data, 1, "transfer", "channel-9", "transfer", "channel-0", clienttypes.Height{}, 0)
}

func TestRecvGate(t *testing.T) {
	ctx := sdk.Context{}
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
			SourcePort: "transfer", DestinationPort: "transfer", Version: transfertypes.V1, Encoding: transfertypes.EncodingJSON,
			Value: transfertypes.NewFungibleTokenPacketData("uatom", "1", "sender", receiver, "").GetBytes(),
		}
	}
	require.Equal(t, channeltypesv2.PacketStatus_Failure, mwV2.OnRecvPacket(ctx, "c9", "c0", 1, payload(frozen.Hex()), nil).Status)
	require.Zero(t, appV2.recv)
	require.Equal(t, channeltypesv2.PacketStatus_Success, mwV2.OnRecvPacket(ctx, "c9", "c0", 2, payload(clean.Hex()), nil).Status)
	require.Equal(t, 1, appV2.recv)
}
