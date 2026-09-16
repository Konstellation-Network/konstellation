package ante_test

import (
	"bytes"
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	protov2 "google.golang.org/protobuf/proto"

	evmtypes "github.com/cosmos/evm/x/vm/types"

	sdkmath "cosmossdk.io/math"

	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	"github.com/cosmos/cosmos-sdk/x/auth"
	"github.com/cosmos/cosmos-sdk/x/authz"
	authzmodule "github.com/cosmos/cosmos-sdk/x/authz/module"
	"github.com/cosmos/cosmos-sdk/x/bank"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"

	"github.com/Konstellation-Network/konstellation/x/compliance/ante"
	comptypes "github.com/Konstellation-Network/konstellation/x/compliance/types"
)

var (
	alice   = common.HexToAddress("0x1111111111111111111111111111111111111111")
	bob     = common.HexToAddress("0x2222222222222222222222222222222222222222")
	carol   = common.HexToAddress("0x3333333333333333333333333333333333333333")
	granter = common.HexToAddress("0x4444444444444444444444444444444444444444")
)

func bech(a common.Address) string { return sdk.AccAddress(a.Bytes()).String() }

type fakeLists struct {
	frozen  map[common.Address]bool
	enforce bool
}

func (f fakeLists) IsFrozen(_ context.Context, addr []byte) bool {
	return f.frozen[common.BytesToAddress(addr)]
}
func (f fakeLists) Enforce(context.Context) bool { return f.enforce }

// fakeTx is the minimal sdk.Tx + FeeTx the extractor reads.
type fakeTx struct {
	msgs    []sdk.Msg
	payer   sdk.AccAddress
	granter sdk.AccAddress
}

func (t fakeTx) GetMsgs() []sdk.Msg                    { return t.msgs }
func (t fakeTx) GetMsgsV2() ([]protov2.Message, error) { return nil, nil }
func (t fakeTx) GetGas() uint64                        { return 0 }
func (t fakeTx) GetFee() sdk.Coins                     { return nil }
func (t fakeTx) FeePayer() []byte                      { return t.payer }
func (t fakeTx) FeeGranter() []byte                    { return t.granter }

func testCodec(t *testing.T) codec.Codec {
	t.Helper()
	cfg := moduletestutil.MakeTestEncodingConfig(auth.AppModuleBasic{}, bank.AppModuleBasic{}, authzmodule.AppModuleBasic{})
	evmtypes.RegisterInterfaces(cfg.InterfaceRegistry)
	return cfg.Codec
}

func has(addrs [][]byte, a common.Address) bool {
	for _, x := range addrs {
		if bytes.Equal(x, a.Bytes()) {
			return true
		}
	}
	return false
}

func TestInvolvedAddresses_CosmosSendAndFees(t *testing.T) {
	cdc := testCodec(t)
	tx := fakeTx{
		msgs:    []sdk.Msg{&banktypes.MsgSend{FromAddress: bech(alice), ToAddress: bech(bob), Amount: sdk.NewCoins(sdk.NewCoin("esp", sdkmath.NewInt(1)))}},
		payer:   alice.Bytes(),
		granter: granter.Bytes(),
	}
	got, err := ante.InvolvedAddresses(cdc, tx)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []common.Address{alice, bob, granter} {
		if !has(got, want) {
			t.Errorf("missing %s", want)
		}
	}
	if has(got, carol) {
		t.Error("carol should not be involved")
	}
}

func TestInvolvedAddresses_MultiSendOutputs(t *testing.T) {
	cdc := testCodec(t)
	coins := sdk.NewCoins(sdk.NewCoin("esp", sdkmath.NewInt(1)))
	tx := fakeTx{msgs: []sdk.Msg{&banktypes.MsgMultiSend{
		Inputs:  []banktypes.Input{{Address: bech(alice), Coins: coins}},
		Outputs: []banktypes.Output{{Address: bech(bob), Coins: coins}, {Address: bech(carol), Coins: coins}},
	}}}
	got, err := ante.InvolvedAddresses(cdc, tx)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []common.Address{alice, bob, carol} {
		if !has(got, want) {
			t.Errorf("missing %s", want)
		}
	}
}

func TestInvolvedAddresses_EVMSenderAndTo(t *testing.T) {
	cdc := testCodec(t)
	ethTx := ethtypes.NewTx(&ethtypes.LegacyTx{Nonce: 0, To: &bob, Value: big.NewInt(1), Gas: 21000, GasPrice: big.NewInt(1)})
	msg := &evmtypes.MsgEthereumTx{}
	msg.FromEthereumTx(ethTx)
	msg.From = alice.Bytes() // what the EVM ante has verified by the time Check runs
	got, err := ante.InvolvedAddresses(cdc, fakeTx{msgs: []sdk.Msg{msg}})
	if err != nil {
		t.Fatal(err)
	}
	if !has(got, alice) || !has(got, bob) {
		t.Fatalf("want sender+to, got %x", got)
	}

	// contract creation: no `to`
	create := ethtypes.NewTx(&ethtypes.LegacyTx{Nonce: 0, To: nil, Gas: 53000, GasPrice: big.NewInt(1), Data: []byte{0x60}})
	msg2 := &evmtypes.MsgEthereumTx{}
	msg2.FromEthereumTx(create)
	msg2.From = alice.Bytes()
	got, err = ante.InvolvedAddresses(cdc, fakeTx{msgs: []sdk.Msg{msg2}})
	if err != nil || !has(got, alice) || len(got) != 1 {
		t.Fatalf("contract creation: %x %v", got, err)
	}
}

func TestInvolvedAddresses_AuthzNested(t *testing.T) {
	cdc := testCodec(t)
	inner := &banktypes.MsgSend{FromAddress: bech(alice), ToAddress: bech(bob), Amount: sdk.NewCoins(sdk.NewCoin("esp", sdkmath.NewInt(1)))}
	exec := authz.NewMsgExec(sdk.AccAddress(carol.Bytes()), []sdk.Msg{inner})
	got, err := ante.InvolvedAddresses(cdc, fakeTx{msgs: []sdk.Msg{&exec}})
	if err != nil {
		t.Fatal(err)
	}
	// grantee (carol) signs; alice is the inner signer; bob the recipient
	for _, want := range []common.Address{alice, bob, carol} {
		if !has(got, want) {
			t.Errorf("missing %s", want)
		}
	}

	// nesting beyond the bound is rejected rather than silently truncated
	deep := exec
	for i := 0; i < 5; i++ {
		d := authz.NewMsgExec(sdk.AccAddress(carol.Bytes()), []sdk.Msg{&deep})
		deep = d
	}
	if _, err := ante.InvolvedAddresses(cdc, fakeTx{msgs: []sdk.Msg{&deep}}); err == nil {
		t.Fatal("over-deep authz accepted")
	}
}

func TestCheck(t *testing.T) {
	cdc := testCodec(t)
	send := &banktypes.MsgSend{FromAddress: bech(alice), ToAddress: bech(bob), Amount: sdk.NewCoins(sdk.NewCoin("esp", sdkmath.NewInt(1)))}
	tx := fakeTx{msgs: []sdk.Msg{send}, payer: alice.Bytes()}
	ctx := sdk.Context{}

	// recipient frozen → rejected, even though the signer is clean
	err := ante.Check(ctx, cdc, fakeLists{frozen: map[common.Address]bool{bob: true}, enforce: true}, tx)
	if err == nil || !comptypes.ErrAddressFrozen.Is(err) {
		t.Fatalf("want ErrAddressFrozen, got %v", err)
	}
	// nobody frozen → ok
	if err := ante.Check(ctx, cdc, fakeLists{enforce: true}, tx); err != nil {
		t.Fatal(err)
	}
	// kill switch → ok even with a frozen party
	if err := ante.Check(ctx, cdc, fakeLists{frozen: map[common.Address]bool{alice: true}, enforce: false}, tx); err != nil {
		t.Fatal(err)
	}
}

func TestWrapOrder(t *testing.T) {
	cdc := testCodec(t)
	send := &banktypes.MsgSend{FromAddress: bech(alice), ToAddress: bech(bob), Amount: sdk.NewCoins(sdk.NewCoin("esp", sdkmath.NewInt(1)))}
	tx := fakeTx{msgs: []sdk.Msg{send}}
	innerCalled := false
	inner := func(ctx sdk.Context, _ sdk.Tx, _ bool) (sdk.Context, error) { innerCalled = true; return ctx, nil }
	h := ante.Wrap(inner, cdc, fakeLists{frozen: map[common.Address]bool{alice: true}, enforce: true})
	if _, err := h(sdk.Context{}, tx, false); err == nil || !innerCalled {
		t.Fatalf("inner=%v err=%v", innerCalled, err)
	}
}
