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
	gov       = authtypes.NewModuleAddress(govtypes.ModuleName).String()
	authority = sdk.AccAddress(common.HexToAddress("0x00000000000000000000000000000000000000A1").Bytes()).String()
	stranger  = sdk.AccAddress(common.HexToAddress("0x00000000000000000000000000000000000000A2").Bytes()).String()
	alice     = common.HexToAddress("0x1111111111111111111111111111111111111111")
	bob       = common.HexToAddress("0x2222222222222222222222222222222222222222")
	t0        = time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
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
	k := keeper.NewKeeper(cdc, runtime.NewKVStoreService(key), gov)
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
