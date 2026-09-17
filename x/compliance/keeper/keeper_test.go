package keeper_test

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/cosmos/cosmos-sdk/runtime"
	storetypes "github.com/cosmos/cosmos-sdk/store/v2/types"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"

	"github.com/Konstellation-Network/konstellation/x/compliance"
	"github.com/Konstellation-Network/konstellation/x/compliance/keeper"
	"github.com/Konstellation-Network/konstellation/x/compliance/types"
)

var (
	gov          = authtypes.NewModuleAddress(govtypes.ModuleName).String()
	feeCollector = authtypes.NewModuleAddress(authtypes.FeeCollectorName).String()
	authority    = sdk.AccAddress(common.HexToAddress("0x00000000000000000000000000000000000000A1").Bytes()).String()
	stranger     = sdk.AccAddress(common.HexToAddress("0x00000000000000000000000000000000000000A2").Bytes()).String()
	alice        = common.HexToAddress("0x1111111111111111111111111111111111111111")
	bob          = common.HexToAddress("0x2222222222222222222222222222222222222222")
	t0           = time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
)

type fixture struct {
	t   *testing.T
	ctx sdk.Context
	k   keeper.Keeper
	ms  types.MsgServer
	qs  types.QueryServer
}

func setup(t *testing.T) *fixture {
	t.Helper()
	key := storetypes.NewKVStoreKey(types.StoreKey)
	tc := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("t_"+types.StoreKey))
	cdc := moduletestutil.MakeTestEncodingConfig(compliance.AppModuleBasic{}).Codec
	k := keeper.NewKeeper(cdc, runtime.NewKVStoreService(key), gov, []string{feeCollector})
	ctx := tc.Ctx.WithBlockTime(t0).WithBlockHeight(1)
	gs := types.DefaultGenesisState()
	gs.Params.Authority = authority
	if err := k.InitGenesis(ctx, *gs); err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, ctx: ctx, k: k, ms: keeper.NewMsgServerImpl(k), qs: keeper.NewQueryServerImpl(k)}
}

func (f *fixture) advance(d time.Duration) {
	f.ctx = f.ctx.WithBlockTime(f.ctx.BlockTime().Add(d)).WithBlockHeight(f.ctx.BlockHeight() + 1)
	if err := f.k.EndBlock(f.ctx); err != nil {
		f.t.Fatal(err)
	}
}

func change(addr common.Address, list types.List, action types.Action) types.Change {
	return types.Change{Address: addr.Hex(), List: list, Action: action, Reason: "test"}
}

func TestScheduleTimelockThenExecute(t *testing.T) {
	f := setup(t)
	resp, err := f.ms.ScheduleUpdate(f.ctx, &types.MsgScheduleUpdate{
		Authority: authority,
		Changes:   []types.Change{change(alice, types.LIST_BLOCK, types.ACTION_ADD)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Id != 1 || !resp.ExecuteAt.Equal(t0.Add(24*time.Hour)) {
		t.Fatalf("resp %+v", resp)
	}
	if f.k.IsFrozen(f.ctx, alice.Bytes()) {
		t.Fatal("frozen before timelock")
	}
	f.advance(24*time.Hour - time.Second)
	if f.k.IsFrozen(f.ctx, alice.Bytes()) {
		t.Fatal("frozen one second early")
	}
	f.advance(time.Second)
	if !f.k.IsFrozen(f.ctx, alice.Bytes()) {
		t.Fatal("not frozen after timelock")
	}
	if _, err := f.k.Pending.Get(f.ctx, 1); err == nil {
		t.Fatal("pending update not removed after execution")
	}
	// bech32 and hex resolve to the same entry
	st, err := f.qs.Status(f.ctx, &types.QueryStatusRequest{Address: sdk.AccAddress(alice.Bytes()).String()})
	if err != nil || !st.Frozen || st.Verified || len(st.Entries) != 1 {
		t.Fatalf("status via bech32: %+v %v", st, err)
	}
}

func TestAllowlistAddUsesItsOwnTimelock(t *testing.T) {
	f := setup(t)
	p := f.k.GetParams(f.ctx)
	p.AllowlistAddTimelock = time.Hour
	if _, err := f.ms.UpdateParams(f.ctx, &types.MsgUpdateParams{Authority: gov, Params: p}); err != nil {
		t.Fatal(err)
	}
	resp, err := f.ms.ScheduleUpdate(f.ctx, &types.MsgScheduleUpdate{
		Authority: authority,
		Changes:   []types.Change{change(alice, types.LIST_ALLOW, types.ACTION_ADD)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.ExecuteAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("allowlist add should use the 1h timelock, got %s", resp.ExecuteAt)
	}
	// mixing in a block-list change lifts the whole update to the long timelock
	resp2, err := f.ms.ScheduleUpdate(f.ctx, &types.MsgScheduleUpdate{
		Authority: authority,
		Changes: []types.Change{
			change(bob, types.LIST_ALLOW, types.ACTION_ADD),
			change(bob, types.LIST_BLOCK, types.ACTION_ADD),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp2.ExecuteAt.Equal(t0.Add(24 * time.Hour)) {
		t.Fatalf("mixed update should use the max timelock, got %s", resp2.ExecuteAt)
	}
}

func TestCancelWithinTimelock(t *testing.T) {
	f := setup(t)
	resp, _ := f.ms.ScheduleUpdate(f.ctx, &types.MsgScheduleUpdate{
		Authority: authority, Changes: []types.Change{change(alice, types.LIST_BLOCK, types.ACTION_ADD)},
	})
	if _, err := f.ms.CancelUpdate(f.ctx, &types.MsgCancelUpdate{Authority: stranger, Id: resp.Id}); err == nil {
		t.Fatal("stranger cancelled")
	}
	if _, err := f.ms.CancelUpdate(f.ctx, &types.MsgCancelUpdate{Authority: gov, Id: resp.Id}); err != nil {
		t.Fatal(err)
	}
	f.advance(25 * time.Hour)
	if f.k.IsFrozen(f.ctx, alice.Bytes()) {
		t.Fatal("cancelled update executed")
	}
	if _, err := f.ms.CancelUpdate(f.ctx, &types.MsgCancelUpdate{Authority: gov, Id: resp.Id}); err == nil {
		t.Fatal("cancelling twice should fail")
	}
}

func TestEmergencyFreezeExpiresUnlessRatified(t *testing.T) {
	f := setup(t)
	resp, err := f.ms.EmergencyFreeze(f.ctx, &types.MsgEmergencyFreeze{
		Authority: authority, Addresses: []string{alice.Hex(), bob.Hex()}, Reason: "incident 1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.ExpiresAt.Equal(t0.Add(24 * time.Hour)) {
		t.Fatalf("expires %s", resp.ExpiresAt)
	}
	if !f.k.IsFrozen(f.ctx, alice.Bytes()) || !f.k.IsFrozen(f.ctx, bob.Bytes()) {
		t.Fatal("emergency freeze not immediate")
	}
	frozen, until := f.k.FrozenUntilUnix(f.ctx, alice.Bytes())
	if !frozen || until != uint64(t0.Add(24*time.Hour).Unix()) { //nolint:gosec // test time is positive
		t.Fatalf("FrozenUntilUnix = %v %d", frozen, until)
	}

	// Ratify alice through the normal timelocked path; leave bob to lapse.
	if _, err := f.ms.ScheduleUpdate(f.ctx, &types.MsgScheduleUpdate{
		Authority: authority, Changes: []types.Change{change(alice, types.LIST_BLOCK, types.ACTION_ADD)},
	}); err != nil {
		t.Fatal(err)
	}

	f.advance(24*time.Hour - time.Second)
	if !f.k.IsFrozen(f.ctx, bob.Bytes()) {
		t.Fatal("bob lapsed early")
	}
	f.advance(time.Second)
	if f.k.IsFrozen(f.ctx, bob.Bytes()) {
		t.Fatal("bob's emergency freeze did not lapse")
	}
	if _, err := f.k.Block.Get(f.ctx, bob.Bytes()); err == nil {
		t.Fatal("bob's lapsed entry not swept")
	}
	frozen, until = f.k.FrozenUntilUnix(f.ctx, alice.Bytes())
	if !frozen || until != 0 {
		t.Fatalf("alice should now be permanently frozen: %v %d", frozen, until)
	}
	// the permanent entry must not be swept by the stale expiry row
	f.advance(time.Hour)
	if !f.k.IsFrozen(f.ctx, alice.Bytes()) {
		t.Fatal("permanent entry swept")
	}
}

func TestLiftEmergencyOnlyTemporary(t *testing.T) {
	f := setup(t)
	if _, err := f.ms.GovUpdate(f.ctx, &types.MsgGovUpdate{
		Authority: gov, Changes: []types.Change{change(alice, types.LIST_BLOCK, types.ACTION_ADD)},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ms.LiftEmergencyFreeze(f.ctx, &types.MsgLiftEmergencyFreeze{Authority: authority, Addresses: []string{alice.Hex()}}); err == nil {
		t.Fatal("lifted a permanent freeze via the emergency path")
	}
	if _, err := f.ms.EmergencyFreeze(f.ctx, &types.MsgEmergencyFreeze{Authority: authority, Addresses: []string{bob.Hex()}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ms.LiftEmergencyFreeze(f.ctx, &types.MsgLiftEmergencyFreeze{Authority: authority, Addresses: []string{bob.Hex()}}); err != nil {
		t.Fatal(err)
	}
	if f.k.IsFrozen(f.ctx, bob.Bytes()) {
		t.Fatal("still frozen after lift")
	}
	// emergency freeze on an already-permanent entry is a no-op, never a downgrade
	if _, err := f.ms.EmergencyFreeze(f.ctx, &types.MsgEmergencyFreeze{Authority: authority, Addresses: []string{alice.Hex()}}); err != nil {
		t.Fatal(err)
	}
	if frozen, until := f.k.FrozenUntilUnix(f.ctx, alice.Bytes()); !frozen || until != 0 {
		t.Fatal("permanent entry downgraded to temporary")
	}
}

func TestAuthorityGates(t *testing.T) {
	f := setup(t)
	chg := []types.Change{change(alice, types.LIST_BLOCK, types.ACTION_ADD)}

	if _, err := f.ms.ScheduleUpdate(f.ctx, &types.MsgScheduleUpdate{Authority: stranger, Changes: chg}); err == nil {
		t.Fatal("stranger scheduled")
	}
	if _, err := f.ms.GovUpdate(f.ctx, &types.MsgGovUpdate{Authority: authority, Changes: chg}); err == nil {
		t.Fatal("list authority used the gov override")
	}
	if _, err := f.ms.UpdateParams(f.ctx, &types.MsgUpdateParams{Authority: authority, Params: f.k.GetParams(f.ctx)}); err == nil {
		t.Fatal("list authority changed params")
	}
	// gov override is immediate
	if _, err := f.ms.GovUpdate(f.ctx, &types.MsgGovUpdate{Authority: gov, Changes: chg}); err != nil {
		t.Fatal(err)
	}
	if !f.k.IsFrozen(f.ctx, alice.Bytes()) {
		t.Fatal("gov update not immediate")
	}
	// gov replaces the authority; the old one is locked out
	p := f.k.GetParams(f.ctx)
	p.Authority = stranger
	if _, err := f.ms.UpdateParams(f.ctx, &types.MsgUpdateParams{Authority: gov, Params: p}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ms.EmergencyFreeze(f.ctx, &types.MsgEmergencyFreeze{Authority: authority, Addresses: []string{bob.Hex()}}); err == nil {
		t.Fatal("replaced authority still acts")
	}
	if _, err := f.ms.EmergencyFreeze(f.ctx, &types.MsgEmergencyFreeze{Authority: stranger, Addresses: []string{bob.Hex()}}); err != nil {
		t.Fatal(err)
	}
	// no authority at all: only gov
	p.Authority = ""
	if _, err := f.ms.UpdateParams(f.ctx, &types.MsgUpdateParams{Authority: gov, Params: p}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ms.ScheduleUpdate(f.ctx, &types.MsgScheduleUpdate{Authority: stranger, Changes: chg}); err != types.ErrNoAuthority && err == nil {
		t.Fatal("expected ErrNoAuthority")
	}
}

func TestEnforceKillSwitchDoesNotTouchLists(t *testing.T) {
	f := setup(t)
	_, _ = f.ms.GovUpdate(f.ctx, &types.MsgGovUpdate{Authority: gov, Changes: []types.Change{change(alice, types.LIST_BLOCK, types.ACTION_ADD)}})
	p := f.k.GetParams(f.ctx)
	p.Enforce = false
	_, _ = f.ms.UpdateParams(f.ctx, &types.MsgUpdateParams{Authority: gov, Params: p})
	if f.k.Enforce(f.ctx) {
		t.Fatal("enforce still on")
	}
	if !f.k.IsFrozen(f.ctx, alice.Bytes()) {
		t.Fatal("kill switch must not clear the list; the precompile still reports frozen")
	}
}

func TestGenesisRoundTrip(t *testing.T) {
	f := setup(t)
	_, _ = f.ms.GovUpdate(f.ctx, &types.MsgGovUpdate{Authority: gov, Changes: []types.Change{
		change(alice, types.LIST_ALLOW, types.ACTION_ADD), change(bob, types.LIST_BLOCK, types.ACTION_ADD),
	}})
	_, _ = f.ms.EmergencyFreeze(f.ctx, &types.MsgEmergencyFreeze{Authority: authority, Addresses: []string{alice.Hex()}})
	_, _ = f.ms.ScheduleUpdate(f.ctx, &types.MsgScheduleUpdate{Authority: authority, Changes: []types.Change{change(bob, types.LIST_BLOCK, types.ACTION_REMOVE)}})

	gs, err := f.k.ExportGenesis(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := gs.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(gs.Entries) != 3 || len(gs.Pending) != 1 || gs.NextPendingId != 2 {
		t.Fatalf("export: %d entries, %d pending, next %d", len(gs.Entries), len(gs.Pending), gs.NextPendingId)
	}

	// import into a fresh keeper, advance past both the pending update and the
	// emergency expiry, and expect the same behaviour as the original
	g := setup(t)
	if err := g.k.InitGenesis(g.ctx, *gs); err != nil {
		t.Fatal(err)
	}
	if !g.k.IsFrozen(g.ctx, alice.Bytes()) || !g.k.IsVerified(g.ctx, alice.Bytes()) || !g.k.IsFrozen(g.ctx, bob.Bytes()) {
		t.Fatal("imported state differs")
	}
	g.advance(25 * time.Hour)
	if g.k.IsFrozen(g.ctx, alice.Bytes()) {
		t.Fatal("imported emergency freeze did not lapse (expiry index not rebuilt)")
	}
	if g.k.IsFrozen(g.ctx, bob.Bytes()) {
		t.Fatal("imported pending removal did not execute (exec index not rebuilt)")
	}
	if id, _ := g.k.PendingSeq.Peek(g.ctx); id != 2 {
		t.Fatalf("sequence not restored: %d", id)
	}
}

func TestMsgValidation(t *testing.T) {
	bad := []types.Change{{Address: "not-an-address", List: types.LIST_BLOCK, Action: types.ACTION_ADD}}
	if err := (&types.MsgScheduleUpdate{Authority: authority, Changes: bad}).ValidateBasic(); err == nil {
		t.Fatal("bad address accepted")
	}
	dup := []types.Change{change(alice, types.LIST_BLOCK, types.ACTION_ADD), change(alice, types.LIST_BLOCK, types.ACTION_REMOVE)}
	if err := (&types.MsgScheduleUpdate{Authority: authority, Changes: dup}).ValidateBasic(); err == nil {
		t.Fatal("contradictory changes accepted")
	}
	long := change(alice, types.LIST_BLOCK, types.ACTION_ADD)
	long.Reason = string(make([]byte, types.MaxReasonLength+1))
	if err := (&types.MsgScheduleUpdate{Authority: authority, Changes: []types.Change{long}}).ValidateBasic(); err == nil {
		t.Fatal("overlong reason accepted")
	}
	p := types.DefaultParams()
	p.Timelock = types.MaxTimelock + time.Second
	if err := p.Validate(); err == nil {
		t.Fatal("overlong timelock accepted")
	}
}

// PR #10 review #2: scheduling the permanent add extends the emergency
// freeze to execute_at, so ratification leaves no unfrozen gap; and an
// emergency freeze cannot be chained.
func TestRatificationHasNoGap(t *testing.T) {
	f := setup(t)
	_, err := f.ms.EmergencyFreeze(f.ctx, &types.MsgEmergencyFreeze{Authority: authority, Addresses: []string{alice.Hex()}})
	if err != nil {
		t.Fatal(err)
	}
	// ratify 10h later: scheduled execute_at = T+10h+24h, after the T+24h expiry
	f.advance(10 * time.Hour)
	resp, err := f.ms.ScheduleUpdate(f.ctx, &types.MsgScheduleUpdate{
		Authority: authority, Changes: []types.Change{change(alice, types.LIST_BLOCK, types.ACTION_ADD)},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, until := f.k.FrozenUntil(f.ctx, alice.Bytes())
	if until == nil || !until.Equal(resp.ExecuteAt) {
		t.Fatalf("emergency expiry should be extended to execute_at %s, got %v", resp.ExecuteAt, until)
	}
	// every hour up to execution: still frozen
	for i := 0; i < 24; i++ {
		f.advance(time.Hour)
		if !f.k.IsFrozen(f.ctx, alice.Bytes()) {
			t.Fatalf("gap: unfrozen at +%dh after scheduling", i+1)
		}
	}
	if frozen, until := f.k.FrozenUntilUnix(f.ctx, alice.Bytes()); !frozen || until != 0 {
		t.Fatalf("should now be permanent: %v %d", frozen, until)
	}
}

func TestEmergencyFreezeCannotBeChained(t *testing.T) {
	f := setup(t)
	if _, err := f.ms.EmergencyFreeze(f.ctx, &types.MsgEmergencyFreeze{Authority: authority, Addresses: []string{alice.Hex()}}); err != nil {
		t.Fatal(err)
	}
	// re-freeze while live: refused
	if _, err := f.ms.EmergencyFreeze(f.ctx, &types.MsgEmergencyFreeze{Authority: authority, Addresses: []string{alice.Hex()}}); !types.ErrEmergencyCooldown.Is(err) {
		t.Fatalf("re-freeze while live: want ErrEmergencyCooldown, got %v", err)
	}
	// lapse, then immediately re-freeze: refused for one timelock
	f.advance(24 * time.Hour)
	if f.k.IsFrozen(f.ctx, alice.Bytes()) {
		t.Fatal("should have lapsed")
	}
	if _, err := f.ms.EmergencyFreeze(f.ctx, &types.MsgEmergencyFreeze{Authority: authority, Addresses: []string{alice.Hex()}}); !types.ErrEmergencyCooldown.Is(err) {
		t.Fatalf("re-freeze in cooldown: want ErrEmergencyCooldown, got %v", err)
	}
	// governance is not subject to the cooldown
	if _, err := f.ms.GovUpdate(f.ctx, &types.MsgGovUpdate{Authority: gov, Changes: []types.Change{change(alice, types.LIST_BLOCK, types.ACTION_ADD)}}); err != nil {
		t.Fatal(err)
	}
	_, _ = f.ms.GovUpdate(f.ctx, &types.MsgGovUpdate{Authority: gov, Changes: []types.Change{change(alice, types.LIST_BLOCK, types.ACTION_REMOVE)}})
	// after the cooldown a fresh emergency freeze is allowed again
	f.advance(24 * time.Hour)
	if _, err := f.ms.EmergencyFreeze(f.ctx, &types.MsgEmergencyFreeze{Authority: authority, Addresses: []string{alice.Hex()}}); err != nil {
		t.Fatalf("after cooldown: %v", err)
	}
	// lifting early also starts the cooldown
	if _, err := f.ms.LiftEmergencyFreeze(f.ctx, &types.MsgLiftEmergencyFreeze{Authority: authority, Addresses: []string{alice.Hex()}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ms.EmergencyFreeze(f.ctx, &types.MsgEmergencyFreeze{Authority: authority, Addresses: []string{alice.Hex()}}); !types.ErrEmergencyCooldown.Is(err) {
		t.Fatalf("lift-and-refreeze: want ErrEmergencyCooldown, got %v", err)
	}
	// cooldown survives a genesis round-trip
	gs, _ := f.k.ExportGenesis(f.ctx)
	if len(gs.Cooldowns) != 1 {
		t.Fatalf("cooldowns exported: %d", len(gs.Cooldowns))
	}
	g := setup(t)
	if err := g.k.InitGenesis(g.ctx, *gs); err != nil {
		t.Fatal(err)
	}
	if _, err := g.ms.EmergencyFreeze(g.ctx, &types.MsgEmergencyFreeze{Authority: authority, Addresses: []string{alice.Hex()}}); !types.ErrEmergencyCooldown.Is(err) {
		t.Fatalf("cooldown lost across genesis: %v", err)
	}
}

func TestAuthorityCannotCancelGovScheduledUpdate(t *testing.T) {
	f := setup(t)
	res, err := f.ms.ScheduleUpdate(f.ctx, &types.MsgScheduleUpdate{Authority: gov, Changes: []types.Change{change(alice, types.LIST_BLOCK, types.ACTION_ADD)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.ms.CancelUpdate(f.ctx, &types.MsgCancelUpdate{Authority: authority, Id: res.Id}); !types.ErrGovScheduled.Is(err) {
		t.Fatalf("authority cancelling a gov-scheduled update: want ErrGovScheduled, got %v", err)
	}
	if _, err := f.k.Pending.Get(f.ctx, res.Id); err != nil {
		t.Fatal("update was removed despite the refusal")
	}
	if _, err := f.ms.CancelUpdate(f.ctx, &types.MsgCancelUpdate{Authority: gov, Id: res.Id}); err != nil {
		t.Fatalf("gov cancelling its own update: %v", err)
	}
	// and gov can still cancel the authority's updates
	res, _ = f.ms.ScheduleUpdate(f.ctx, &types.MsgScheduleUpdate{Authority: authority, Changes: []types.Change{change(bob, types.LIST_ALLOW, types.ACTION_ADD)}})
	if _, err := f.ms.CancelUpdate(f.ctx, &types.MsgCancelUpdate{Authority: gov, Id: res.Id}); err != nil {
		t.Fatalf("gov cancelling the authority's update: %v", err)
	}
}

func TestScheduleThenCancelCannotChainEmergencyFreeze(t *testing.T) {
	// Scheduling a permanent block extends a live emergency freeze to
	// execute_at (no ratification gap). Cancelling must put the expiry back,
	// otherwise schedule/cancel every timelock keeps the address frozen
	// forever with no ratification and no cooldown ever starting.
	f := setup(t)
	freeze := &types.MsgEmergencyFreeze{Authority: authority, Addresses: []string{alice.Hex()}}
	if _, err := f.ms.EmergencyFreeze(f.ctx, freeze); err != nil {
		t.Fatal(err)
	}
	orig := *mustBlock(t, f, alice).ExpiresAt // t0 + 24h

	// 1. schedule then cancel before the original expiry: expiry restored
	f.advance(23 * time.Hour)
	res, err := f.ms.ScheduleUpdate(f.ctx, &types.MsgScheduleUpdate{Authority: authority, Changes: []types.Change{change(alice, types.LIST_BLOCK, types.ACTION_ADD)}})
	if err != nil {
		t.Fatal(err)
	}
	if got := *mustBlock(t, f, alice).ExpiresAt; !got.Equal(res.ExecuteAt) {
		t.Fatalf("schedule did not extend: %v vs %v", got, res.ExecuteAt)
	}
	if _, err := f.ms.CancelUpdate(f.ctx, &types.MsgCancelUpdate{Authority: authority, Id: res.Id}); err != nil {
		t.Fatal(err)
	}
	if got := *mustBlock(t, f, alice).ExpiresAt; !got.Equal(orig) {
		t.Fatalf("cancel did not restore the expiry: %v, want %v", got, orig)
	}
	f.advance(2 * time.Hour) // past orig
	if f.k.IsFrozen(f.ctx, alice.Bytes()) {
		t.Fatal("emergency freeze survived its original expiry after schedule/cancel")
	}
	if _, err := f.ms.EmergencyFreeze(f.ctx, freeze); !types.ErrEmergencyCooldown.Is(err) {
		t.Fatalf("cooldown must apply after lapse: %v", err)
	}

	// 2. schedule, then cancel *after* the original expiry would have
	// passed: the freeze lapses at cancel time, with cooldown
	f.advance(25 * time.Hour) // cooldown over
	if _, err := f.ms.EmergencyFreeze(f.ctx, freeze); err != nil {
		t.Fatal(err)
	}
	f.advance(23 * time.Hour)
	res, err = f.ms.ScheduleUpdate(f.ctx, &types.MsgScheduleUpdate{Authority: authority, Changes: []types.Change{change(alice, types.LIST_BLOCK, types.ACTION_ADD)}})
	if err != nil {
		t.Fatal(err)
	}
	f.advance(10 * time.Hour) // original expiry is 9 h in the past; extension keeps it frozen
	if !f.k.IsFrozen(f.ctx, alice.Bytes()) {
		t.Fatal("extension did not hold")
	}
	if _, err := f.ms.CancelUpdate(f.ctx, &types.MsgCancelUpdate{Authority: authority, Id: res.Id}); err != nil {
		t.Fatal(err)
	}
	if f.k.IsFrozen(f.ctx, alice.Bytes()) {
		t.Fatal("cancel after the original expiry must lapse the freeze")
	}
	if _, err := f.ms.EmergencyFreeze(f.ctx, freeze); !types.ErrEmergencyCooldown.Is(err) {
		t.Fatalf("cooldown must apply after lapse-by-cancel: %v", err)
	}

	// 3. two ratifying updates: cancelling the first hands the record to
	// the second, so the freeze holds to the second's execute_at and no
	// longer, and cancelling the second restores the true original
	f.advance(25 * time.Hour)
	if _, err := f.ms.EmergencyFreeze(f.ctx, freeze); err != nil {
		t.Fatal(err)
	}
	orig = *mustBlock(t, f, alice).ExpiresAt
	f.advance(time.Hour)
	first, _ := f.ms.ScheduleUpdate(f.ctx, &types.MsgScheduleUpdate{Authority: authority, Changes: []types.Change{change(alice, types.LIST_BLOCK, types.ACTION_ADD)}})
	f.advance(time.Hour)
	second, _ := f.ms.ScheduleUpdate(f.ctx, &types.MsgScheduleUpdate{Authority: authority, Changes: []types.Change{change(alice, types.LIST_BLOCK, types.ACTION_ADD)}})
	if _, err := f.ms.CancelUpdate(f.ctx, &types.MsgCancelUpdate{Authority: authority, Id: first.Id}); err != nil {
		t.Fatal(err)
	}
	if got := *mustBlock(t, f, alice).ExpiresAt; !got.Equal(second.ExecuteAt) {
		t.Fatalf("after cancelling the first, expiry should follow the second: %v vs %v", got, second.ExecuteAt)
	}
	if _, err := f.ms.CancelUpdate(f.ctx, &types.MsgCancelUpdate{Authority: authority, Id: second.Id}); err != nil {
		t.Fatal(err)
	}
	if got := *mustBlock(t, f, alice).ExpiresAt; !got.Equal(orig) {
		t.Fatalf("after cancelling both, expiry should be the true original: %v vs %v", got, orig)
	}
}

func TestCancelDoesNotTouchADifferentFreeze(t *testing.T) {
	// The extension record must identify the freeze it extended. If gov
	// removes that freeze and the authority freezes the address afresh
	// while the update is still pending, cancelling the stale update must
	// leave the new freeze alone — not shorten it to the old original, and
	// not lapse it with a cooldown.
	f := setup(t)
	freeze := &types.MsgEmergencyFreeze{Authority: authority, Addresses: []string{alice.Hex()}}
	if _, err := f.ms.EmergencyFreeze(f.ctx, freeze); err != nil {
		t.Fatal(err)
	}
	f.advance(time.Hour)
	res, err := f.ms.ScheduleUpdate(f.ctx, &types.MsgScheduleUpdate{Authority: authority, Changes: []types.Change{change(alice, types.LIST_BLOCK, types.ACTION_ADD)}})
	if err != nil {
		t.Fatal(err)
	}
	// gov removes the freeze outright (no cooldown), update stays pending
	if _, err := f.ms.GovUpdate(f.ctx, &types.MsgGovUpdate{Authority: gov, Changes: []types.Change{change(alice, types.LIST_BLOCK, types.ACTION_REMOVE)}}); err != nil {
		t.Fatal(err)
	}
	f.advance(20 * time.Hour)
	if _, err := f.ms.EmergencyFreeze(f.ctx, freeze); err != nil {
		t.Fatal(err)
	}
	fresh := *mustBlock(t, f, alice).ExpiresAt // now + 24h

	// cancel before the stale original (t0+24h) has passed: no shortening
	f.advance(2 * time.Hour)
	if _, err := f.ms.CancelUpdate(f.ctx, &types.MsgCancelUpdate{Authority: authority, Id: res.Id}); err != nil {
		t.Fatal(err)
	}
	if got := *mustBlock(t, f, alice).ExpiresAt; !got.Equal(fresh) {
		t.Fatalf("cancelling a stale update changed a different freeze: %v, want %v", got, fresh)
	}

	// and the same after the stale original has passed: no lapse
	g := setup(t)
	if _, err := g.ms.EmergencyFreeze(g.ctx, freeze); err != nil {
		t.Fatal(err)
	}
	g.advance(time.Hour)
	res, _ = g.ms.ScheduleUpdate(g.ctx, &types.MsgScheduleUpdate{Authority: authority, Changes: []types.Change{change(alice, types.LIST_BLOCK, types.ACTION_ADD)}})
	_, _ = g.ms.GovUpdate(g.ctx, &types.MsgGovUpdate{Authority: gov, Changes: []types.Change{change(alice, types.LIST_BLOCK, types.ACTION_REMOVE)}})
	g.advance(20 * time.Hour)
	if _, err := g.ms.EmergencyFreeze(g.ctx, freeze); err != nil {
		t.Fatal(err)
	}
	g.advance(3*time.Hour + 30*time.Minute) // stale original (t0+24h) is 30 min in the past; update executes at t0+25h
	if _, err := g.ms.CancelUpdate(g.ctx, &types.MsgCancelUpdate{Authority: authority, Id: res.Id}); err != nil {
		t.Fatal(err)
	}
	if !g.k.IsFrozen(g.ctx, alice.Bytes()) {
		t.Fatal("cancelling a stale update lapsed a different freeze")
	}
	if _, err := g.k.Cooldown.Get(g.ctx, alice.Bytes()); err == nil {
		t.Fatal("a cooldown was started for a freeze that did not lapse")
	}
}

func mustBlock(t *testing.T, f *fixture, addr common.Address) types.ListEntry {
	t.Helper()
	e, err := f.k.Block.Get(f.ctx, addr.Bytes())
	if err != nil {
		t.Fatalf("%s not on block list: %v", addr.Hex(), err)
	}
	return e
}

// PR #10 review #3/#4: timelock must be positive; timestamps are whole seconds.
func TestTimelockBoundsAndSecondGranularity(t *testing.T) {
	p := types.DefaultParams()
	p.Timelock = 0
	if err := p.Validate(); err == nil {
		t.Fatal("zero timelock accepted")
	}
	f := setup(t)
	f.ctx = f.ctx.WithBlockTime(t0.Add(700 * time.Millisecond))
	resp, _ := f.ms.EmergencyFreeze(f.ctx, &types.MsgEmergencyFreeze{Authority: authority, Addresses: []string{alice.Hex()}})
	if resp.ExpiresAt.Nanosecond() != 0 {
		t.Fatalf("expiry not whole seconds: %s", resp.ExpiresAt)
	}
	sresp, _ := f.ms.ScheduleUpdate(f.ctx, &types.MsgScheduleUpdate{Authority: authority, Changes: []types.Change{change(bob, types.LIST_ALLOW, types.ACTION_ADD)}})
	if sresp.ExecuteAt.Nanosecond() != 0 {
		t.Fatalf("execute_at not whole seconds: %s", sresp.ExecuteAt)
	}
	// one second before the index key: not executed; at it: executed
	f.ctx = f.ctx.WithBlockTime(sresp.ExecuteAt.Add(-time.Second))
	_ = f.k.EndBlock(f.ctx)
	if f.k.IsVerified(f.ctx, bob.Bytes()) {
		t.Fatal("executed early")
	}
	f.ctx = f.ctx.WithBlockTime(sresp.ExecuteAt)
	_ = f.k.EndBlock(f.ctx)
	if !f.k.IsVerified(f.ctx, bob.Bytes()) {
		t.Fatal("not executed at execute_at")
	}
}

// Human review of PR #10: module accounts, governance and the authority
// itself can never be frozen, on any path — and the protected set is
// checked at submission, not 24 h later in EndBlock.
func TestProtectedAddressesCannotBeFrozen(t *testing.T) {
	f := setup(t)
	for _, victim := range []string{gov, feeCollector, authority} {
		if _, err := f.ms.EmergencyFreeze(f.ctx, &types.MsgEmergencyFreeze{Authority: authority, Addresses: []string{victim}}); !types.ErrProtectedAddress.Is(err) {
			t.Errorf("emergency freeze of %s: want ErrProtectedAddress, got %v", victim, err)
		}
		chg := []types.Change{{Address: victim, List: types.LIST_BLOCK, Action: types.ACTION_ADD}}
		if _, err := f.ms.ScheduleUpdate(f.ctx, &types.MsgScheduleUpdate{Authority: authority, Changes: chg}); !types.ErrProtectedAddress.Is(err) {
			t.Errorf("schedule freeze of %s: want ErrProtectedAddress, got %v", victim, err)
		}
		if _, err := f.ms.GovUpdate(f.ctx, &types.MsgGovUpdate{Authority: gov, Changes: chg}); !types.ErrProtectedAddress.Is(err) {
			t.Errorf("gov freeze of %s: want ErrProtectedAddress, got %v", victim, err)
		}
		// allow-listing a protected address is harmless and permitted
		allow := []types.Change{{Address: victim, List: types.LIST_ALLOW, Action: types.ACTION_ADD}}
		if _, err := f.ms.GovUpdate(f.ctx, &types.MsgGovUpdate{Authority: gov, Changes: allow}); err != nil {
			t.Errorf("allow-listing %s: %v", victim, err)
		}
	}
	// a pending freeze whose target becomes the authority is skipped at
	// execution, and EndBlock does not fail
	if _, err := f.ms.ScheduleUpdate(f.ctx, &types.MsgScheduleUpdate{Authority: authority, Changes: []types.Change{change(alice, types.LIST_BLOCK, types.ACTION_ADD)}}); err != nil {
		t.Fatal(err)
	}
	p := f.k.GetParams(f.ctx)
	p.Authority = sdk.AccAddress(alice.Bytes()).String()
	if _, err := f.ms.UpdateParams(f.ctx, &types.MsgUpdateParams{Authority: gov, Params: p}); err != nil {
		t.Fatal(err)
	}
	f.advance(25 * time.Hour)
	if f.k.IsFrozen(f.ctx, alice.Bytes()) {
		t.Fatal("the authority got frozen by a pre-existing pending update")
	}
}

func TestGenesisRefusesProtectedFreeze(t *testing.T) {
	// InitGenesis is a write path Validate() cannot guard: the protected set
	// lives on the keeper. A frozen gov account at genesis is unrecoverable.
	for _, victim := range []string{gov, feeCollector, authority} {
		f := setup(t)
		gs := types.DefaultGenesisState()
		gs.Params.Authority = authority
		gs.Entries = []types.ListEntry{{Address: victim, List: types.LIST_BLOCK, Reason: "oops"}}
		if err := gs.Validate(); err != nil {
			t.Fatalf("Validate is keeper-free and must accept %s: %v", victim, err)
		}
		if err := f.k.InitGenesis(f.ctx, *gs); !types.ErrProtectedAddress.Is(err) {
			t.Errorf("genesis block entry for %s: want ErrProtectedAddress, got %v", victim, err)
		}
		if f.k.IsFrozen(f.ctx, sdk.MustAccAddressFromBech32(victim)) {
			t.Errorf("%s frozen by genesis", victim)
		}

		// a pending block-add is rejected too, not left as dead weight
		gs.Entries = nil
		gs.Pending = []types.PendingUpdate{{
			Id: 1, ExecuteAt: t0.Add(time.Hour), ScheduledBy: authority,
			Changes: []types.Change{{Address: victim, List: types.LIST_BLOCK, Action: types.ACTION_ADD}},
		}}
		gs.NextPendingId = 2
		if err := f.k.InitGenesis(f.ctx, *gs); !types.ErrProtectedAddress.Is(err) {
			t.Errorf("genesis pending freeze of %s: want ErrProtectedAddress, got %v", victim, err)
		}

		// allow-listing at genesis stays permitted
		gs.Pending = nil
		gs.NextPendingId = 1
		gs.Entries = []types.ListEntry{{Address: victim, List: types.LIST_ALLOW}}
		if err := f.k.InitGenesis(f.ctx, *gs); err != nil {
			t.Errorf("genesis allow entry for %s: %v", victim, err)
		}
	}
}

func TestUpdateParamsRefusesFrozenAuthority(t *testing.T) {
	f := setup(t)
	_, _ = f.ms.GovUpdate(f.ctx, &types.MsgGovUpdate{Authority: gov, Changes: []types.Change{change(bob, types.LIST_BLOCK, types.ACTION_ADD)}})
	p := f.k.GetParams(f.ctx)
	p.Authority = sdk.AccAddress(bob.Bytes()).String()
	if _, err := f.ms.UpdateParams(f.ctx, &types.MsgUpdateParams{Authority: gov, Params: p}); !types.ErrAddressFrozen.Is(err) {
		t.Fatalf("want ErrAddressFrozen, got %v", err)
	}
	p.Timelock = 30 * time.Second
	p.Authority = authority
	if _, err := f.ms.UpdateParams(f.ctx, &types.MsgUpdateParams{Authority: gov, Params: p}); err == nil {
		t.Fatal("timelock below MinTimelock accepted")
	}
}
