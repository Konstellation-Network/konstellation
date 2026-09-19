package keeper_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	transfertypes "github.com/cosmos/ibc-go/v11/modules/apps/transfer/types"
	clienttypes "github.com/cosmos/ibc-go/v11/modules/core/02-client/types"
	channeltypes "github.com/cosmos/ibc-go/v11/modules/core/04-channel/types"

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
)

var (
	gov      = authtypes.NewModuleAddress(govtypes.ModuleName).String()
	stranger = sdk.AccAddress([]byte("stranger____________")).String()
	t0       = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	denom    = "esp"
	channel  = "channel-0"
	million  = sdkmath.NewInt(1_000_000)
)

// fakeBank answers GetSupply from a map.
type fakeBank struct{ supply map[string]sdkmath.Int }

func (b fakeBank) GetSupply(_ context.Context, d string) sdk.Coin {
	if s, ok := b.supply[d]; ok {
		return sdk.NewCoin(d, s)
	}
	return sdk.NewCoin(d, sdkmath.ZeroInt())
}

type fixture struct {
	t    *testing.T
	ctx  sdk.Context
	k    keeper.Keeper
	ms   types.MsgServer
	qs   types.QueryServer
	bank *fakeBank
}

func setup(t *testing.T) *fixture {
	t.Helper()
	key := storetypes.NewKVStoreKey(types.StoreKey)
	tc := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("t_"+types.StoreKey))
	cdc := moduletestutil.MakeTestEncodingConfig(ratelimit.AppModuleBasic{}).Codec
	bank := &fakeBank{supply: map[string]sdkmath.Int{denom: million}}
	k := keeper.NewKeeper(cdc, runtime.NewKVStoreService(key), gov, bank)
	ctx := tc.Ctx.WithBlockTime(t0).WithBlockHeight(1)
	require.NoError(t, k.InitGenesis(ctx, *types.DefaultGenesisState()))
	return &fixture{t: t, ctx: ctx, k: k, ms: keeper.NewMsgServerImpl(k), qs: keeper.NewQueryServerImpl(k), bank: bank}
}

func (f *fixture) advance(d time.Duration) {
	f.ctx = f.ctx.WithBlockTime(f.ctx.BlockTime().Add(d)).WithBlockHeight(f.ctx.BlockHeight() + 1)
	require.NoError(f.t, f.k.BeginBlock(f.ctx))
}

func (f *fixture) add(sendPct, recvPct int64, hours uint64) {
	f.t.Helper()
	_, err := f.ms.AddRateLimit(f.ctx, &types.MsgAddRateLimit{
		Authority: gov, Denom: denom, ChannelId: channel,
		MaxPercentSend: sdkmath.NewInt(sendPct), MaxPercentRecv: sdkmath.NewInt(recvPct), DurationHours: hours,
	})
	require.NoError(f.t, err)
}

func (f *fixture) flow() types.Flow {
	rl, ok := f.k.GetRateLimit(f.ctx, denom, channel)
	require.True(f.t, ok)
	return rl.Flow
}

func (f *fixture) send(amount int64, seq uint64) error {
	return f.k.OnSend(f.ctx, keeper.PacketInfo{Denom: denom, ChannelID: channel, Amount: sdkmath.NewInt(amount)}, seq)
}

func (f *fixture) recv(amount int64) error {
	return f.k.OnRecv(f.ctx, keeper.PacketInfo{Denom: denom, ChannelID: channel, Amount: sdkmath.NewInt(amount)})
}

func TestAddValidation(t *testing.T) {
	f := setup(t)
	// gov only
	_, err := f.ms.AddRateLimit(f.ctx, &types.MsgAddRateLimit{
		Authority: stranger, Denom: denom, ChannelId: channel,
		MaxPercentSend: sdkmath.NewInt(10), MaxPercentRecv: sdkmath.NewInt(10), DurationHours: 24,
	})
	require.ErrorIs(t, err, types.ErrUnauthorized)
	// percent out of range
	_, err = f.ms.AddRateLimit(f.ctx, &types.MsgAddRateLimit{
		Authority: gov, Denom: denom, ChannelId: channel,
		MaxPercentSend: sdkmath.NewInt(101), MaxPercentRecv: sdkmath.NewInt(10), DurationHours: 24,
	})
	require.ErrorIs(t, err, types.ErrInvalidQuota)
	// window out of range
	_, err = f.ms.AddRateLimit(f.ctx, &types.MsgAddRateLimit{
		Authority: gov, Denom: denom, ChannelId: channel,
		MaxPercentSend: sdkmath.NewInt(10), MaxPercentRecv: sdkmath.NewInt(10), DurationHours: types.MaxDurationHours + 1,
	})
	require.ErrorIs(t, err, types.ErrInvalidQuota)
	// a denom with no supply: a typo caught at proposal time
	_, err = f.ms.AddRateLimit(f.ctx, &types.MsgAddRateLimit{
		Authority: gov, Denom: "typo", ChannelId: channel,
		MaxPercentSend: sdkmath.NewInt(10), MaxPercentRecv: sdkmath.NewInt(10), DurationHours: 24,
	})
	require.ErrorIs(t, err, types.ErrZeroSupply)

	f.add(10, 10, 24)
	fl := f.flow()
	require.True(t, fl.ChannelValue.Equal(million), "channel value is the supply at window start")
	require.True(t, fl.WindowStart.Equal(t0))
	// duplicate
	_, err = f.ms.AddRateLimit(f.ctx, &types.MsgAddRateLimit{
		Authority: gov, Denom: denom, ChannelId: channel,
		MaxPercentSend: sdkmath.NewInt(10), MaxPercentRecv: sdkmath.NewInt(10), DurationHours: 24,
	})
	require.ErrorIs(t, err, types.ErrRateLimitExists)
}

func TestNetFlowQuota(t *testing.T) {
	f := setup(t)
	f.add(10, 5, 24) // send ≤ 100 000, recv ≤ 50 000 net

	require.NoError(t, f.send(60_000, 1))
	require.NoError(t, f.send(40_000, 2))
	err := f.send(1, 3)
	require.ErrorIs(t, err, types.ErrQuotaExceeded)
	require.Contains(t, err.Error(), "10% of 1000000")

	// An inflow offsets: net outflow is what counts.
	require.NoError(t, f.recv(30_000))
	require.NoError(t, f.send(30_000, 3))
	require.ErrorIs(t, f.send(1, 4), types.ErrQuotaExceeded)

	// Recv quota is net too: 30 000 in, 130 000 out → net inflow is negative,
	// so a further 50 000 in is fine, 80 000 more would still be, and the
	// cap bites at net +50 000.
	require.NoError(t, f.recv(150_000))
	require.ErrorIs(t, f.recv(1), types.ErrQuotaExceeded)

	fl := f.flow()
	require.True(t, fl.Outflow.Equal(sdkmath.NewInt(130_000)), fl.Outflow)
	require.True(t, fl.Inflow.Equal(sdkmath.NewInt(180_000)), fl.Inflow)

	// Unlimited paths pass untouched.
	limited, err := f.k.CheckAndRecord(f.ctx, types.PacketSend, "other", channel, million)
	require.NoError(t, err)
	require.False(t, limited)
}

func TestZeroQuotaBlocksDirection(t *testing.T) {
	f := setup(t)
	f.add(0, 100, 24)
	require.ErrorIs(t, f.send(1, 1), types.ErrQuotaExceeded)
	require.NoError(t, f.recv(million.Int64()))
}

func TestUndoSendOnlyWithinWindow(t *testing.T) {
	f := setup(t)
	f.add(10, 10, 1)
	require.NoError(t, f.send(100_000, 7))
	require.ErrorIs(t, f.send(1, 8), types.ErrQuotaExceeded)

	// Error ack for seq 7 puts it back.
	info := keeper.PacketInfo{Denom: denom, ChannelID: channel, Amount: sdkmath.NewInt(100_000)}
	require.NoError(t, f.k.OnAck(f.ctx, info, 7, false))
	require.True(t, f.flow().Outflow.IsZero())
	// Twice does nothing: pending was cleared.
	require.NoError(t, f.k.OnAck(f.ctx, info, 7, false))
	require.True(t, f.flow().Outflow.IsZero())

	// Success ack clears pending; a later "failure" for the same seq does
	// not credit anything.
	require.NoError(t, f.send(100_000, 9))
	require.NoError(t, f.k.OnAck(f.ctx, info, 9, true))
	require.NoError(t, f.k.OnAck(f.ctx, info, 9, false))
	require.True(t, f.flow().Outflow.Equal(sdkmath.NewInt(100_000)))

	// Timeout after the window reset: the new window never counted it, so
	// nothing is credited.
	require.NoError(t, f.send(0, 10)) // zero-amount pending marker is fine
	f.advance(time.Hour)
	require.True(t, f.flow().Outflow.IsZero(), "window did not reset")
	require.NoError(t, f.k.OnTimeout(f.ctx, info, 9))
	require.True(t, f.flow().Outflow.IsZero())
	gs, err := f.k.ExportGenesis(f.ctx)
	require.NoError(t, err)
	require.Empty(t, gs.PendingPackets, "reset must forget pending sends")
}

func TestWindowResetSnapshotsSupply(t *testing.T) {
	f := setup(t)
	f.add(10, 10, 2)
	require.NoError(t, f.send(100_000, 1))
	f.bank.supply[denom] = sdkmath.NewInt(2_000_000)

	f.advance(time.Hour) // not yet
	require.True(t, f.flow().Outflow.Equal(sdkmath.NewInt(100_000)))
	require.True(t, f.flow().ChannelValue.Equal(million), "channel value must not move mid-window")

	f.advance(time.Hour) // exactly at the boundary resets
	fl := f.flow()
	require.True(t, fl.Outflow.IsZero())
	require.True(t, fl.ChannelValue.Equal(sdkmath.NewInt(2_000_000)))
	require.True(t, fl.WindowStart.Equal(t0.Add(2*time.Hour)))
	require.NoError(t, f.send(200_000, 2), "quota follows the new supply")
}

func TestUpdateRemoveReset(t *testing.T) {
	f := setup(t)
	f.add(10, 10, 24)
	require.NoError(t, f.send(50_000, 1))

	_, err := f.ms.UpdateRateLimit(f.ctx, &types.MsgUpdateRateLimit{
		Authority: gov, Denom: denom, ChannelId: channel,
		MaxPercentSend: sdkmath.NewInt(1), MaxPercentRecv: sdkmath.NewInt(1), DurationHours: 1,
	})
	require.NoError(t, err)
	require.True(t, f.flow().Outflow.IsZero(), "update starts a fresh window")
	require.ErrorIs(t, f.send(10_001, 2), types.ErrQuotaExceeded)
	require.NoError(t, f.send(10_000, 2))

	_, err = f.ms.ResetRateLimit(f.ctx, &types.MsgResetRateLimit{Authority: gov, Denom: denom, ChannelId: channel})
	require.NoError(t, err)
	require.True(t, f.flow().Outflow.IsZero())

	_, err = f.ms.RemoveRateLimit(f.ctx, &types.MsgRemoveRateLimit{Authority: stranger, Denom: denom, ChannelId: channel})
	require.ErrorIs(t, err, types.ErrUnauthorized)
	_, err = f.ms.RemoveRateLimit(f.ctx, &types.MsgRemoveRateLimit{Authority: gov, Denom: denom, ChannelId: channel})
	require.NoError(t, err)
	_, ok := f.k.GetRateLimit(f.ctx, denom, channel)
	require.False(t, ok)
	require.NoError(t, f.send(million.Int64(), 3), "no limit, no gate")
	_, err = f.ms.ResetRateLimit(f.ctx, &types.MsgResetRateLimit{Authority: gov, Denom: denom, ChannelId: channel})
	require.ErrorIs(t, err, types.ErrRateLimitNotFound)
}

func TestQueriesAndGenesisRoundTrip(t *testing.T) {
	f := setup(t)
	f.add(10, 10, 24)
	f.bank.supply["ibc/ABC"] = million
	_, err := f.ms.AddRateLimit(f.ctx, &types.MsgAddRateLimit{
		Authority: gov, Denom: "ibc/ABC", ChannelId: "channel-1",
		MaxPercentSend: sdkmath.NewInt(5), MaxPercentRecv: sdkmath.NewInt(5), DurationHours: 6,
	})
	require.NoError(t, err)
	require.NoError(t, f.send(1, 1))

	all, err := f.qs.RateLimits(f.ctx, &types.QueryRateLimitsRequest{})
	require.NoError(t, err)
	require.Len(t, all.RateLimits, 2)
	one, err := f.qs.RateLimit(f.ctx, &types.QueryRateLimitRequest{Denom: denom, ChannelId: channel})
	require.NoError(t, err)
	require.True(t, one.RateLimit.Flow.Outflow.Equal(sdkmath.OneInt()))
	byChan, err := f.qs.RateLimitsByChannel(f.ctx, &types.QueryRateLimitsByChannelRequest{ChannelId: "channel-1"})
	require.NoError(t, err)
	require.Len(t, byChan.RateLimits, 1)
	require.Equal(t, "ibc/ABC", byChan.RateLimits[0].Path.Denom)
	_, err = f.qs.RateLimit(f.ctx, &types.QueryRateLimitRequest{Denom: "nope", ChannelId: channel})
	require.Error(t, err)

	gs, err := f.k.ExportGenesis(f.ctx)
	require.NoError(t, err)
	require.Len(t, gs.RateLimits, 2)
	require.Len(t, gs.PendingPackets, 1)
	require.NoError(t, gs.Validate())

	g := setup(t)
	require.NoError(t, g.k.InitGenesis(g.ctx, *gs))
	gs2, err := g.k.ExportGenesis(g.ctx)
	require.NoError(t, err)
	require.Equal(t, gs, gs2)

	// Validate catches duplicates and bad quotas.
	bad := *gs
	bad.RateLimits = append(bad.RateLimits, bad.RateLimits[0])
	require.Error(t, bad.Validate())
}

// ── packet parsing: must agree with x/transfer's denom rules ──────────────

func packetV1(t *testing.T, denomPath, srcChan, dstChan string) channeltypes.Packet {
	t.Helper()
	data := transfertypes.NewFungibleTokenPacketData(denomPath, "5", "sender", "receiver", "")
	return channeltypes.NewPacket(data.GetBytes(), 1, "transfer", srcChan, "transfer", dstChan, clienttypes.Height{}, 0)
}

func TestParsePacketV1Denoms(t *testing.T) {
	voucher := transfertypes.NewDenom("uatom", transfertypes.NewHop("transfer", "channel-0"))
	cases := []struct {
		name      string
		packet    channeltypes.Packet
		direction types.PacketDirection
		denom     string
		channel   string
	}{
		{"send native", packetV1(t, denom, channel, "channel-9"), types.PacketSend, denom, channel},
		{"send voucher back", packetV1(t, voucher.Path(), channel, "channel-9"), types.PacketSend, voucher.IBCDenom(), channel},
		// Our esp coming home from the counterparty: it carries our port/channel prefix as seen from them.
		{"recv native returning", packetV1(t, "transfer/channel-9/"+denom, "channel-9", channel), types.PacketRecv, denom, channel},
		// A foreign token arriving: we prefix our dest port/channel → voucher.
		{"recv foreign", packetV1(t, "uatom", "channel-9", channel), types.PacketRecv, voucher.IBCDenom(), channel},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			info, err := keeper.ParsePacketV1(c.packet, c.direction)
			require.NoError(t, err)
			require.Equal(t, c.denom, info.Denom)
			require.Equal(t, c.channel, info.ChannelID)
			require.True(t, info.Amount.Equal(sdkmath.NewInt(5)))
		})
	}
	_, err := keeper.ParsePacketV1(channeltypes.NewPacket([]byte("not ics20"), 1, "p", "c", "p", "c", clienttypes.Height{}, 0), types.PacketSend)
	require.ErrorIs(t, err, types.ErrInvalidPacket)
}

func TestAckSucceeded(t *testing.T) {
	ok, err := keeper.AckSucceeded(channeltypes.NewResultAcknowledgement([]byte{1}).Acknowledgement())
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = keeper.AckSucceeded(channeltypes.NewErrorAcknowledgement(types.ErrQuotaExceeded).Acknowledgement())
	require.NoError(t, err)
	require.False(t, ok)
	_, err = keeper.AckSucceeded([]byte("garbage"))
	require.Error(t, err)
}
