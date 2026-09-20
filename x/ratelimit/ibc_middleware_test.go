package ratelimit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	transfertypes "github.com/cosmos/ibc-go/v11/modules/apps/transfer/types"
	clienttypes "github.com/cosmos/ibc-go/v11/modules/core/02-client/types"
	channeltypes "github.com/cosmos/ibc-go/v11/modules/core/04-channel/types"
	channeltypesv2 "github.com/cosmos/ibc-go/v11/modules/core/04-channel/v2/types"
	porttypes "github.com/cosmos/ibc-go/v11/modules/core/05-port/types"
	"github.com/cosmos/ibc-go/v11/modules/core/exported"
	ibcmock "github.com/cosmos/ibc-go/v11/testing/mock"

	sdkmath "cosmossdk.io/math"

	"github.com/cosmos/cosmos-sdk/runtime"
	storetypes "github.com/cosmos/cosmos-sdk/store/v2/types"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"

	"github.com/Konstellation-Network/konstellation/x/ratelimit"
	"github.com/Konstellation-Network/konstellation/x/ratelimit/keeper"
	"github.com/Konstellation-Network/konstellation/x/ratelimit/types"
	ratelimitv2 "github.com/Konstellation-Network/konstellation/x/ratelimit/v2"
)

const (
	denom   = "esp"
	channel = "channel-0"
	port    = "transfer"
)

var (
	gov     = authtypes.NewModuleAddress(govtypes.ModuleName).String()
	million = sdkmath.NewInt(1_000_000)
)

type fakeBank struct{}

func (fakeBank) GetSupply(_ context.Context, d string) sdk.Coin { return sdk.NewCoin(d, million) }

// recordingApp is the application under the middleware: it records every
// callback and answers OnRecvPacket with a configurable ack.
type recordingApp struct {
	ibcmock.IBCModule
	recv    int
	acks    int
	timeout int
	ack     exported.Acknowledgement
}

func (a *recordingApp) OnRecvPacket(sdk.Context, string, channeltypes.Packet, sdk.AccAddress) exported.Acknowledgement {
	a.recv++
	return a.ack
}

func (a *recordingApp) OnAcknowledgementPacket(sdk.Context, string, channeltypes.Packet, []byte, sdk.AccAddress) error {
	a.acks++
	return nil
}

func (a *recordingApp) OnTimeoutPacket(sdk.Context, string, channeltypes.Packet, sdk.AccAddress) error {
	a.timeout++
	return nil
}

func (a *recordingApp) SetICS4Wrapper(porttypes.ICS4Wrapper) {}

// fakeCore stands in for the channel keeper: hands out sequences.
type fakeCore struct{ seq uint64 }

func (c *fakeCore) SendPacket(sdk.Context, string, string, clienttypes.Height, uint64, []byte) (uint64, error) {
	c.seq++
	return c.seq, nil
}

func (c *fakeCore) WriteAcknowledgement(sdk.Context, exported.PacketI, exported.Acknowledgement) error {
	return nil
}

func (c *fakeCore) GetAppVersion(sdk.Context, string, string) (string, bool) { return "ics20-1", true }

type fixture struct {
	ctx  sdk.Context
	k    keeper.Keeper
	app  *recordingApp
	core *fakeCore
	mw   *ratelimit.IBCMiddleware
}

func setup(t *testing.T) *fixture {
	t.Helper()
	key := storetypes.NewKVStoreKey(types.StoreKey)
	tc := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("t_"+types.StoreKey))
	cdc := moduletestutil.MakeTestEncodingConfig(ratelimit.AppModuleBasic{}).Codec
	k := keeper.NewKeeper(cdc, runtime.NewKVStoreService(key), gov, fakeBank{})
	ctx := tc.Ctx.WithBlockTime(time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC))
	app := &recordingApp{ack: channeltypes.NewResultAcknowledgement([]byte{1})}
	core := &fakeCore{}
	mw := ratelimit.NewIBCMiddleware(k, app)
	mw.SetICS4Wrapper(core)
	// 10% send, 10% recv of 1 000 000 → 100 000 each way, net.
	_, err := keeper.NewMsgServerImpl(k).AddRateLimit(ctx, &types.MsgAddRateLimit{
		Authority: gov, Denom: denom, ChannelId: channel,
		MaxPercentSend: sdkmath.NewInt(10), MaxPercentRecv: sdkmath.NewInt(10), DurationHours: 24,
	})
	require.NoError(t, err)
	return &fixture{ctx: ctx, k: k, app: app, core: core, mw: mw}
}

func (f *fixture) flow(t *testing.T) types.Flow {
	t.Helper()
	rl, ok, err := f.k.GetRateLimit(f.ctx, denom, channel)
	require.NoError(t, err)
	require.True(t, ok)
	return rl.Flow
}

func ics20(denomPath string, amount int64) []byte {
	return transfertypes.NewFungibleTokenPacketData(denomPath, sdkmath.NewInt(amount).String(), "sender", "receiver", "").GetBytes()
}

// inbound is a packet from the counterparty carrying our native denom home.
func inbound(amount int64, seq uint64) channeltypes.Packet {
	return channeltypes.NewPacket(ics20("transfer/channel-9/"+denom, amount), seq, port, "channel-9", port, channel, clienttypes.Height{}, 0)
}

// outbound is the packet our SendPacket produced.
func outbound(seq uint64) channeltypes.Packet {
	return channeltypes.NewPacket(ics20(denom, 100_000), seq, port, channel, port, "channel-9", clienttypes.Height{}, 0)
}

func (f *fixture) send(amount int64) (uint64, error) {
	return f.mw.SendPacket(f.ctx, port, channel, clienttypes.Height{}, 0, ics20(denom, amount))
}

func TestSendGate(t *testing.T) {
	f := setup(t)
	seq, err := f.send(100_000)
	require.NoError(t, err)
	require.EqualValues(t, 1, seq)
	require.True(t, f.flow(t).Outflow.Equal(sdkmath.NewInt(100_000)))

	_, err = f.send(1)
	require.ErrorIs(t, err, types.ErrQuotaExceeded)
	require.Contains(t, err.Error(), "ICS-20 send refused")

	// Non-ICS-20 data on the same channel is not ours to limit.
	_, err = f.mw.SendPacket(f.ctx, "other", channel, clienttypes.Height{}, 0, []byte("opaque"))
	require.NoError(t, err)
	require.True(t, f.flow(t).Outflow.Equal(sdkmath.NewInt(100_000)))
}

func TestSendUndoneOnErrorAckAndTimeout(t *testing.T) {
	f := setup(t)
	seq, err := f.send(100_000)
	require.NoError(t, err)

	errAck := channeltypes.NewErrorAcknowledgement(types.ErrQuotaExceeded).Acknowledgement()
	require.NoError(t, f.mw.OnAcknowledgementPacket(f.ctx, "ics20-1", outbound(seq), errAck, nil))
	require.Equal(t, 1, f.app.acks, "ack must reach the application")
	require.True(t, f.flow(t).Outflow.IsZero(), "error ack did not put the send back")

	seq, err = f.send(100_000)
	require.NoError(t, err)
	require.NoError(t, f.mw.OnTimeoutPacket(f.ctx, "ics20-1", outbound(seq), nil))
	require.Equal(t, 1, f.app.timeout)
	require.True(t, f.flow(t).Outflow.IsZero(), "timeout did not put the send back")

	// A success ack settles it: nothing comes back later.
	seq, err = f.send(100_000)
	require.NoError(t, err)
	okAck := channeltypes.NewResultAcknowledgement([]byte{1}).Acknowledgement()
	require.NoError(t, f.mw.OnAcknowledgementPacket(f.ctx, "ics20-1", outbound(seq), okAck, nil))
	require.NoError(t, f.mw.OnTimeoutPacket(f.ctx, "ics20-1", outbound(seq), nil))
	require.True(t, f.flow(t).Outflow.Equal(sdkmath.NewInt(100_000)))
}

func TestRecvGate(t *testing.T) {
	f := setup(t)
	ack := f.mw.OnRecvPacket(f.ctx, "ics20-1", inbound(100_000, 1), nil)
	require.True(t, ack.Success())
	require.Equal(t, 1, f.app.recv)
	require.True(t, f.flow(t).Inflow.Equal(sdkmath.NewInt(100_000)))

	// Over quota: error ack, and the application never sees the packet.
	ack = f.mw.OnRecvPacket(f.ctx, "ics20-1", inbound(1, 2), nil)
	require.False(t, ack.Success())
	// ibc-go error acks carry only the ABCI code, deterministically; the
	// reason is in the events.
	require.Contains(t, string(ack.Acknowledgement()), "ABCI code: 6")
	require.Equal(t, 1, f.app.recv)

	// The application rejecting the packet undoes the count.
	f2 := setup(t)
	f2.app.ack = channeltypes.NewErrorAcknowledgement(transfertypes.ErrReceiveDisabled)
	ack = f2.mw.OnRecvPacket(f2.ctx, "ics20-1", inbound(100_000, 1), nil)
	require.False(t, ack.Success())
	require.True(t, f2.flow(t).Inflow.IsZero(), "app rejection did not undo the inflow")

	// Non-ICS-20 packet: straight through.
	opaque := channeltypes.NewPacket([]byte("opaque"), 3, "other", "channel-9", "other", channel, clienttypes.Height{}, 0)
	require.True(t, f.mw.OnRecvPacket(f.ctx, "v1", opaque, nil).Success())
}

// ── v2 ────────────────────────────────────────────────────────────────────

type recordingAppV2 struct {
	sends, recvs int
	result       channeltypesv2.RecvPacketResult
}

func (a *recordingAppV2) OnSendPacket(sdk.Context, string, string, uint64, channeltypesv2.Payload, sdk.AccAddress) error {
	a.sends++
	return nil
}

func (a *recordingAppV2) OnRecvPacket(sdk.Context, string, string, uint64, channeltypesv2.Payload, sdk.AccAddress) channeltypesv2.RecvPacketResult {
	a.recvs++
	return a.result
}

func (a *recordingAppV2) OnTimeoutPacket(sdk.Context, string, string, uint64, channeltypesv2.Payload, sdk.AccAddress) error {
	return nil
}

func (a *recordingAppV2) OnAcknowledgementPacket(sdk.Context, string, string, uint64, []byte, channeltypesv2.Payload, sdk.AccAddress) error {
	return nil
}

func payload(denomPath string, amount int64) channeltypesv2.Payload {
	return channeltypesv2.Payload{
		SourcePort: port, DestinationPort: port, Version: transfertypes.V1, Encoding: transfertypes.EncodingJSON,
		Value: ics20(denomPath, amount),
	}
}

func TestV2Gates(t *testing.T) {
	f := setup(t)
	app := &recordingAppV2{result: channeltypesv2.RecvPacketResult{Status: channeltypesv2.PacketStatus_Success, Acknowledgement: channeltypes.NewResultAcknowledgement([]byte{1}).Acknowledgement()}}
	mw := ratelimitv2.NewIBCMiddleware(f.k, app)

	// The v2 path uses client ids where v1 uses channel ids; the limit was
	// added on "channel-0", so use that as the client id.
	require.NoError(t, mw.OnSendPacket(f.ctx, channel, "client-9", 1, payload(denom, 100_000), nil))
	require.Equal(t, 1, app.sends)
	require.ErrorIs(t, mw.OnSendPacket(f.ctx, channel, "client-9", 2, payload(denom, 1), nil), types.ErrQuotaExceeded)
	require.Equal(t, 1, app.sends, "refused send must not reach the application")

	// Universal error ack undoes the send.
	require.NoError(t, mw.OnAcknowledgementPacket(f.ctx, channel, "client-9", 1, channeltypesv2.ErrorAcknowledgement[:], payload(denom, 100_000), nil))
	require.True(t, f.flow(t).Outflow.IsZero())

	// Inbound over quota fails the receipt without reaching the app. Our
	// native denom coming home carries the counterparty's view of the path:
	// our port and the *client* id it knows us by.
	res := mw.OnRecvPacket(f.ctx, "client-9", channel, 1, payload("transfer/client-9/"+denom, 100_000), nil)
	require.Equal(t, channeltypesv2.PacketStatus_Success, res.Status)
	require.True(t, f.flow(t).Inflow.Equal(sdkmath.NewInt(100_000)))
	res = mw.OnRecvPacket(f.ctx, "client-9", channel, 2, payload("transfer/client-9/"+denom, 1), nil)
	require.Equal(t, channeltypesv2.PacketStatus_Failure, res.Status)
	require.Equal(t, 1, app.recvs)
}
