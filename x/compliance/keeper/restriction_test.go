package keeper_test

import (
	"context"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	sdkmath "cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	bankkeeper "github.com/cosmos/cosmos-sdk/x/bank/keeper"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/Konstellation-Network/konstellation/x/compliance/keeper"
	"github.com/Konstellation-Network/konstellation/x/compliance/types"
)

var (
	carol = common.HexToAddress("0x3333333333333333333333333333333333333333")
	esp   = sdk.NewCoins(sdk.NewInt64Coin("esp", 1))
)

func (f *fixture) freeze(addr common.Address) {
	f.t.Helper()
	if _, err := f.ms.GovUpdate(f.ctx, &types.MsgGovUpdate{Authority: gov, Changes: []types.Change{change(addr, types.LIST_BLOCK, types.ACTION_ADD)}}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) send(ctx context.Context, from, to common.Address) error {
	f.t.Helper()
	newTo, err := f.k.SendRestriction(ctx, from.Bytes(), to.Bytes(), esp)
	if string(newTo) != string(to.Bytes()) {
		f.t.Fatalf("restriction changed the recipient to %x", newTo)
	}
	return err
}

func TestSendRestrictionRefusesFrozenParties(t *testing.T) {
	f := setup(t)
	if err := f.send(f.ctx, alice, bob); err != nil {
		t.Fatalf("clean send refused: %v", err)
	}
	f.freeze(alice)
	if err := f.send(f.ctx, alice, bob); !types.ErrAddressFrozen.Is(err) {
		t.Fatalf("frozen sender: got %v", err)
	}
	if err := f.send(f.ctx, bob, alice); !types.ErrAddressFrozen.Is(err) {
		t.Fatalf("frozen recipient: got %v", err)
	}
	if err := f.send(f.ctx, bob, carol); err != nil {
		t.Fatalf("unrelated send refused: %v", err)
	}
}

func TestSendRestrictionProtocolFlowExemptsRecipientOnly(t *testing.T) {
	f := setup(t)
	f.freeze(alice)
	marked := types.WithProtocolFlow(f.ctx)
	if !types.IsProtocolFlow(marked) || types.IsProtocolFlow(f.ctx) {
		t.Fatal("mark not carried by the context")
	}
	if err := f.send(marked, bob, alice); err != nil {
		t.Fatalf("protocol deposit into a frozen address refused: %v", err)
	}
	// The mark survives the SDK's own context plumbing: module code
	// unwraps and re-wraps contexts and caches them.
	cached, _ := marked.CacheContext()
	if err := f.send(sdk.UnwrapSDKContext(cached), bob, alice); err != nil {
		t.Fatalf("mark lost through CacheContext/UnwrapSDKContext: %v", err)
	}
	// A frozen sender is refused even under the mark: no protocol flow
	// debits a user account.
	if err := f.send(marked, alice, bob); !types.ErrAddressFrozen.Is(err) {
		t.Fatalf("frozen sender under protocol mark: got %v", err)
	}
}

func TestSendRestrictionGovRefundsReachFrozenDepositor(t *testing.T) {
	f := setup(t)
	f.freeze(alice)
	govAddr := common.BytesToAddress(authtypes.NewModuleAddress(govtypes.ModuleName))
	if err := f.send(f.ctx, govAddr, alice); err != nil {
		t.Fatalf("x/gov deposit refund to a frozen depositor refused (this halts the chain at the proposal's end): %v", err)
	}
	// Only gov: another module paying a frozen address at a party's request
	// is refused.
	distr := common.BytesToAddress(authtypes.NewModuleAddress("distribution"))
	if err := f.send(f.ctx, distr, alice); !types.ErrAddressFrozen.Is(err) {
		t.Fatalf("distribution → frozen: got %v", err)
	}
}

func TestSendRestrictionHonoursKillSwitch(t *testing.T) {
	f := setup(t)
	f.freeze(alice)
	p := f.k.GetParams(f.ctx)
	p.Enforce = false
	if _, err := f.ms.UpdateParams(f.ctx, &types.MsgUpdateParams{Authority: gov, Params: p}); err != nil {
		t.Fatal(err)
	}
	if err := f.send(f.ctx, alice, bob); err != nil {
		t.Fatalf("enforce=false must make the restriction a no-op: %v", err)
	}
}

// fakeBank is the slice of the bank keeper EVMBankKeeper touches.
type fakeBank struct {
	bankkeeper.Keeper
	balance sdk.Coin
	set     *sdk.Coin
}

func (b *fakeBank) GetBalance(context.Context, sdk.AccAddress, string) sdk.Coin { return b.balance }
func (b *fakeBank) UncheckedSetBalance(_ context.Context, _ sdk.AccAddress, c sdk.Coin) error {
	b.set = &c
	return nil
}

func TestEVMBankKeeperRefusesBalanceChangeOnFrozenAddress(t *testing.T) {
	f := setup(t)
	bank := &fakeBank{balance: sdk.NewInt64Coin("esp", 10)}
	bk := keeper.NewEVMBankKeeper(bank, f.k)

	// Clean address: the write goes through.
	if err := bk.UncheckedSetBalance(f.ctx, alice.Bytes(), sdk.NewInt64Coin("esp", 5)); err != nil || bank.set == nil || !bank.set.Amount.Equal(sdkmath.NewInt(5)) {
		t.Fatalf("clean write: err %v, set %v", err, bank.set)
	}
	bank.set = nil

	f.freeze(alice)
	for _, amt := range []int64{5, 11, 0} {
		if err := bk.UncheckedSetBalance(f.ctx, alice.Bytes(), sdk.NewInt64Coin("esp", amt)); !types.ErrAddressFrozen.Is(err) {
			t.Fatalf("balance %d → %d on a frozen address: got %v", 10, amt, err)
		}
	}
	if bank.set != nil {
		t.Fatal("refused write reached the bank")
	}
	// Rewriting the same balance is a touch, not a change: a contract that
	// reads a frozen address's balance or calls it with no value must not
	// fail at commit.
	if err := bk.UncheckedSetBalance(f.ctx, alice.Bytes(), sdk.NewInt64Coin("esp", 10)); err != nil || bank.set == nil {
		t.Fatalf("same-balance rewrite: err %v, set %v", err, bank.set)
	}
	// Kill switch.
	bank.set = nil
	p := f.k.GetParams(f.ctx)
	p.Enforce = false
	if _, err := f.ms.UpdateParams(f.ctx, &types.MsgUpdateParams{Authority: gov, Params: p}); err != nil {
		t.Fatal(err)
	}
	if err := bk.UncheckedSetBalance(f.ctx, alice.Bytes(), sdk.NewInt64Coin("esp", 0)); err != nil || bank.set == nil {
		t.Fatalf("enforce=false: err %v, set %v", err, bank.set)
	}
}

// fakeHooks records whether AfterValidatorRemoved ran under the protocol
// mark and whether the other hooks were left alone.
type fakeHooks struct {
	stakingtypes.StakingHooks
	removedMarked, modifiedMarked bool
}

func (h *fakeHooks) AfterValidatorRemoved(ctx context.Context, _ sdk.ConsAddress, _ sdk.ValAddress) error {
	h.removedMarked = types.IsProtocolFlow(ctx)
	return nil
}

func (h *fakeHooks) BeforeDelegationSharesModified(ctx context.Context, _ sdk.AccAddress, _ sdk.ValAddress) error {
	h.modifiedMarked = types.IsProtocolFlow(ctx)
	return nil
}

func TestMarkValidatorRemovalMarksOnlyThatHook(t *testing.T) {
	f := setup(t)
	inner := &fakeHooks{}
	h := keeper.MarkValidatorRemoval(inner)
	if err := h.AfterValidatorRemoved(f.ctx, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := h.BeforeDelegationSharesModified(f.ctx, nil, nil); err != nil {
		t.Fatal(err)
	}
	if !inner.removedMarked {
		t.Fatal("AfterValidatorRemoved did not run as a protocol flow")
	}
	if inner.modifiedMarked {
		t.Fatal("BeforeDelegationSharesModified (a delegator's own action) must not be exempt")
	}
	if types.IsProtocolFlow(f.ctx) {
		t.Fatal("mark leaked into the caller's context")
	}
}
