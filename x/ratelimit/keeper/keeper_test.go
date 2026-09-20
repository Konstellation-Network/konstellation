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
	rl, ok, err := f.k.GetRateLimit(f.ctx, denom, channel)
	require.NoError(f.t, err)
	require.True(f.t, ok)
	return rl.Flow
}

func (f *fixture) mustLimit(d string) types.RateLimit {
	f.t.Helper()
	rl, ok, err := f.k.GetRateLimit(f.ctx, d, channel)
	require.NoError(f.t, err)
	require.True(f.t, ok)
	return rl
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

// An absolute cap binds when it is below the percentage. Matters for the
// native denom, whose supply is the whole chain, so a percentage alone is
// a weak ceiling.
func TestAbsoluteCap(t *testing.T) {
	f := setup(t)
	_, err := f.ms.AddRateLimit(f.ctx, &types.MsgAddRateLimit{
		Authority: gov, Denom: denom, ChannelId: channel,
		MaxPercentSend: sdkmath.NewInt(10), MaxPercentRecv: sdkmath.NewInt(10), DurationHours: 24, // 100 000 by percentage
		MaxAbsoluteSend: sdkmath.NewInt(25_000), // the lower one wins
	})
	require.NoError(t, err)

	require.NoError(t, f.send(25_000, 1))
	err = f.send(1, 2)
	require.ErrorIs(t, err, types.ErrQuotaExceeded)
	require.Contains(t, err.Error(), "exceed 25000 (10% of 1000000, absolute cap 25000)")
	// Recv side has no absolute cap: the percentage applies unchanged.
	require.NoError(t, f.recv(125_000)) // net inflow 100 000
	require.ErrorIs(t, f.recv(1), types.ErrQuotaExceeded)

	// A cap above the percentage does not loosen it.
	_, err = f.ms.UpdateRateLimit(f.ctx, &types.MsgUpdateRateLimit{
		Authority: gov, Denom: denom, ChannelId: channel,
		MaxPercentSend: sdkmath.NewInt(10), MaxPercentRecv: sdkmath.NewInt(10), DurationHours: 24,
		MaxAbsoluteSend: sdkmath.NewInt(5_000_000),
	})
	require.NoError(t, err)
	require.NoError(t, f.send(100_000, 3))
	require.ErrorIs(t, f.send(1, 4), types.ErrQuotaExceeded)

	// Unset (a proposal's JSON omitting the fields) is "no cap" and is
	// stored as zero, not nil.
	rl, ok, err := f.k.GetRateLimit(f.ctx, denom, channel)
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, rl.Quota.MaxAbsoluteRecv.IsNil())
	require.True(t, rl.Quota.MaxAbsoluteRecv.IsZero())

	// Negative is refused.
	_, err = f.ms.UpdateRateLimit(f.ctx, &types.MsgUpdateRateLimit{
		Authority: gov, Denom: denom, ChannelId: channel,
		MaxPercentSend: sdkmath.NewInt(10), MaxPercentRecv: sdkmath.NewInt(10), DurationHours: 24,
		MaxAbsoluteRecv: sdkmath.NewInt(-1),
	})
	require.ErrorIs(t, err, types.ErrInvalidQuota)
}

// A foreign token's path is limited before its first packet: the voucher
// has no supply, so the absolute receive cap stands alone until it does
// (ENGINEERING.md §15 phase 9: quotas before a channel carries value).
func TestBootstrapForeignVoucher(t *testing.T) {
	f := setup(t)
	const voucher = "ibc/NEW" // absent from the fake bank: supply 0
	add := func(pct, abs int64) error {
		_, err := f.ms.AddRateLimit(f.ctx, &types.MsgAddRateLimit{
			Authority: gov, Denom: voucher, ChannelId: channel,
			MaxPercentSend: sdkmath.NewInt(pct), MaxPercentRecv: sdkmath.NewInt(pct), DurationHours: 24,
			MaxAbsoluteRecv: sdkmath.NewInt(abs),
		})
		return err
	}
	recv := func(amount int64) error {
		return f.k.OnRecv(f.ctx, keeper.PacketInfo{Denom: voucher, ChannelID: channel, Amount: sdkmath.NewInt(amount)})
	}

	// Without an absolute cap the typo guard still applies …
	require.ErrorIs(t, add(10, 0), types.ErrZeroSupply)
	// … and a zero percentage does not bootstrap either (it allows nothing).
	require.ErrorIs(t, add(0, 50_000), types.ErrZeroSupply)

	require.NoError(t, add(10, 50_000))
	require.True(t, f.mustLimit(voucher).Flow.ChannelValue.IsZero())
	require.NoError(t, recv(50_000))
	require.ErrorIs(t, recv(1), types.ErrQuotaExceeded)

	// Once the voucher has been minted, the next window snapshots it and
	// the lower of the percentage and the cap applies.
	f.bank.supply[voucher] = sdkmath.NewInt(200_000)
	f.advance(24 * time.Hour)
	rl := f.mustLimit(voucher)
	require.True(t, rl.Flow.ChannelValue.Equal(sdkmath.NewInt(200_000)))
	require.True(t, rl.Quota.Threshold(types.PacketRecv, rl.Flow.ChannelValue).Equal(sdkmath.NewInt(20_000)), "10% of 200 000 < 50 000")
	require.NoError(t, recv(20_000))
	require.ErrorIs(t, recv(1), types.ErrQuotaExceeded)
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

// A voucher's supply drops to zero once everything has gone home. A window
// reset must not snapshot that zero — the threshold would be zero and the
// path deadlocked until governance removed the limit.
func TestWindowResetKeepsChannelValueOnZeroSupply(t *testing.T) {
	f := setup(t)
	f.add(10, 10, 1)
	f.bank.supply[denom] = sdkmath.ZeroInt()

	f.advance(time.Hour) // BeginBlock reset
	require.True(t, f.flow().ChannelValue.Equal(million), "zero supply must carry the previous channel value")
	require.NoError(t, f.recv(100_000), "inbound still allowed at the old threshold")

	_, err := f.ms.ResetRateLimit(f.ctx, &types.MsgResetRateLimit{Authority: gov, Denom: denom, ChannelId: channel})
	require.NoError(t, err)
	require.True(t, f.flow().ChannelValue.Equal(million))

	_, err = f.ms.UpdateRateLimit(f.ctx, &types.MsgUpdateRateLimit{
		Authority: gov, Denom: denom, ChannelId: channel,
		MaxPercentSend: sdkmath.NewInt(5), MaxPercentRecv: sdkmath.NewInt(5), DurationHours: 1,
	})
	require.NoError(t, err)
	require.True(t, f.flow().ChannelValue.Equal(million))

	// Once supply is back, the next window snapshots it as usual.
	f.bank.supply[denom] = sdkmath.NewInt(3_000_000)
	f.advance(2 * time.Hour)
	require.True(t, f.flow().ChannelValue.Equal(sdkmath.NewInt(3_000_000)))
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
	_, ok, err := f.k.GetRateLimit(f.ctx, denom, channel)
	require.NoError(t, err)
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
	in := *gs
	in.RateLimits = append([]types.RateLimit(nil), gs.RateLimits...)
	in.RateLimits[0].Quota.MaxAbsoluteSend = sdkmath.Int{} // omitted in a hand-written genesis
	require.NoError(t, g.k.InitGenesis(g.ctx, in))
	gs2, err := g.k.ExportGenesis(g.ctx)
	require.NoError(t, err)
	require.Equal(t, gs, gs2, "unset absolute caps normalise to zero")

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
