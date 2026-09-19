package keeper_test

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	evmtypes "github.com/cosmos/evm/x/vm/types"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/Konstellation-Network/konstellation/x/compliance/types"
)

// fakeEVM is the code-hash store slice of x/vm: address → code hash, code
// hash → code. Storage is not modelled because the reset must not touch it.
type fakeEVM struct {
	codeHash map[common.Address]common.Hash
	code     map[common.Hash][]byte
	deleted  []common.Address
}

func newFakeEVM() *fakeEVM {
	return &fakeEVM{codeHash: map[common.Address]common.Hash{}, code: map[common.Hash][]byte{}}
}

func (e *fakeEVM) setCode(addr common.Address, code []byte) {
	h := crypto.Keccak256Hash(code)
	e.codeHash[addr] = h
	e.code[h] = code
}

func (e *fakeEVM) GetCodeHash(_ sdk.Context, addr common.Address) common.Hash {
	if h, ok := e.codeHash[addr]; ok {
		return h
	}
	return common.BytesToHash(evmtypes.EmptyCodeHash)
}

func (e *fakeEVM) GetCode(_ sdk.Context, h common.Hash) []byte { return e.code[h] }

func (e *fakeEVM) DeleteCodeHash(_ sdk.Context, addr common.Address) {
	delete(e.codeHash, addr)
	e.deleted = append(e.deleted, addr)
}

var (
	delegate     = common.HexToAddress("0x3333333333333333333333333333333333333333")
	contractCode = []byte{0x60, 0x80, 0x60, 0x40, 0x52} // any bytecode that is not a delegation
)

func setupWithEVM(t *testing.T) (*fixture, *fakeEVM) {
	t.Helper()
	f := setup(t)
	evm := newFakeEVM()
	f.k.SetEVMKeeper(evm)
	if !f.k.HasEVMKeeper() {
		t.Fatal("HasEVMKeeper false after SetEVMKeeper")
	}
	return f, evm
}

func resetEvents(ctx sdk.Context) []sdk.Event {
	var out []sdk.Event
	for _, e := range ctx.EventManager().Events() {
		if e.Type == types.EventTypeDelegationReset {
			out = append(out, e)
		}
	}
	return out
}

func TestEmergencyFreezeResetsDelegation(t *testing.T) {
	f, evm := setupWithEVM(t)
	evm.setCode(alice, ethtypes.AddressToDelegation(delegate))
	evm.setCode(bob, contractCode)

	if _, err := f.ms.EmergencyFreeze(f.ctx, &types.MsgEmergencyFreeze{
		Authority: authority, Addresses: []string{alice.Hex(), bob.Hex()}, Reason: testReason,
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := evm.codeHash[alice]; ok {
		t.Fatal("alice's delegation survived the freeze")
	}
	if _, ok := evm.codeHash[bob]; !ok {
		t.Fatal("bob's contract code was deleted: only delegations may be reset")
	}
	if len(evm.deleted) != 1 || evm.deleted[0] != alice {
		t.Fatalf("deleted %v, want [alice]", evm.deleted)
	}
	evs := resetEvents(f.ctx)
	if len(evs) != 1 {
		t.Fatalf("want one %s event, got %d", types.EventTypeDelegationReset, len(evs))
	}
	got := map[string]string{}
	for _, a := range evs[0].Attributes {
		got[a.Key] = a.Value
	}
	if got[types.AttributeKeyAddress] != sdk.AccAddress(alice.Bytes()).String() ||
		got[types.AttributeKeyDelegate] != delegate.Hex() ||
		got[types.AttributeKeyBy] != authority {
		t.Fatalf("event attributes %v", got)
	}
}

func TestScheduledAndGovAddsResetDelegation(t *testing.T) {
	// Scheduled: the reset happens at execution, not at scheduling — the
	// address is not frozen until then, and may legitimately re-delegate
	// in the meantime.
	f, evm := setupWithEVM(t)
	evm.setCode(alice, ethtypes.AddressToDelegation(delegate))
	if _, err := f.ms.ScheduleUpdate(f.ctx, &types.MsgScheduleUpdate{
		Authority: authority,
		Changes:   []types.Change{change(alice, types.LIST_BLOCK, types.ACTION_ADD)},
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := evm.codeHash[alice]; !ok {
		t.Fatal("delegation reset at scheduling time; must wait for execution")
	}
	f.advance(24 * time.Hour)
	if !f.k.IsFrozen(f.ctx, alice.Bytes()) {
		t.Fatal("not frozen after timelock")
	}
	if _, ok := evm.codeHash[alice]; ok {
		t.Fatal("delegation survived the scheduled freeze")
	}

	// Gov override: immediate.
	evm.setCode(bob, ethtypes.AddressToDelegation(delegate))
	if _, err := f.ms.GovUpdate(f.ctx, &types.MsgGovUpdate{
		Authority: gov,
		Changes:   []types.Change{change(bob, types.LIST_BLOCK, types.ACTION_ADD)},
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := evm.codeHash[bob]; ok {
		t.Fatal("delegation survived the gov freeze")
	}
}

func TestOnlyBlockListAddsTouchCode(t *testing.T) {
	f, evm := setupWithEVM(t)
	evm.setCode(alice, ethtypes.AddressToDelegation(delegate))

	// Allow-list add: not a freeze, nothing to reset.
	if _, err := f.ms.GovUpdate(f.ctx, &types.MsgGovUpdate{
		Authority: gov,
		Changes:   []types.Change{change(alice, types.LIST_ALLOW, types.ACTION_ADD)},
	}); err != nil {
		t.Fatal(err)
	}
	// Block-list remove of an address that is not on it: no-op.
	if _, err := f.ms.GovUpdate(f.ctx, &types.MsgGovUpdate{
		Authority: gov,
		Changes:   []types.Change{change(alice, types.LIST_BLOCK, types.ACTION_REMOVE)},
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := evm.codeHash[alice]; !ok {
		t.Fatal("delegation removed by a non-freeze change")
	}
	if len(resetEvents(f.ctx)) != 0 {
		t.Fatal("unexpected reset event")
	}

	// An address with no code at all: no event, no delete.
	if _, err := f.ms.EmergencyFreeze(f.ctx, &types.MsgEmergencyFreeze{
		Authority: authority, Addresses: []string{bob.Hex()}, Reason: testReason,
	}); err != nil {
		t.Fatal(err)
	}
	if len(evm.deleted) != 0 || len(resetEvents(f.ctx)) != 0 {
		t.Fatalf("freeze of a code-less account touched code: deleted=%v", evm.deleted)
	}
}

func TestNoEVMKeeperLeavesDelegationsAlone(t *testing.T) {
	// A keeper without SetEVMKeeper (unit tests, or a harness that wires
	// only the lists) still freezes; it just cannot reset code.
	f := setup(t)
	if f.k.HasEVMKeeper() {
		t.Fatal("HasEVMKeeper true before SetEVMKeeper")
	}
	if _, err := f.ms.EmergencyFreeze(f.ctx, &types.MsgEmergencyFreeze{
		Authority: authority, Addresses: []string{alice.Hex()}, Reason: testReason,
	}); err != nil {
		t.Fatal(err)
	}
	if !f.k.IsFrozen(f.ctx, alice.Bytes()) {
		t.Fatal("not frozen")
	}
}
