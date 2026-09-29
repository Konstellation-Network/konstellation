//go:build test

// Package integration runs the real KonstellationApp in-process — genesis,
// ante chain, EVM, x/compliance — and drives it with signed transactions
// through cosmos/evm's integration harness. No Docker, no network; one
// validator, blocks committed on demand.
//
// Build tag: cosmos/evm's EVM chain config is a process global that can be
// set once per process unless the `test` tag is on, and every test here
// builds its own app. `make test-integration` passes the tag; `go test
// ./...` without it does not see these files.
package integration

import (
	"encoding/json"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	abcitypes "github.com/cometbft/cometbft/abci/types"

	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/evm"
	"github.com/cosmos/evm/crypto/ethsecp256k1"
	evmmempool "github.com/cosmos/evm/mempool"
	srvflags "github.com/cosmos/evm/server/flags"
	testapp "github.com/cosmos/evm/testutil/app"
	testconstants "github.com/cosmos/evm/testutil/constants"
	basefactory "github.com/cosmos/evm/testutil/integration/base/factory"
	"github.com/cosmos/evm/testutil/integration/evm/factory"
	"github.com/cosmos/evm/testutil/integration/evm/grpc"
	"github.com/cosmos/evm/testutil/integration/evm/network"
	testkeyring "github.com/cosmos/evm/testutil/keyring"
	testutiltypes "github.com/cosmos/evm/testutil/types"
	evmtypes "github.com/cosmos/evm/x/vm/types"

	"cosmossdk.io/log/v2"
	sdkmath "cosmossdk.io/math"

	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/client/flags"
	circuittypes "github.com/cosmos/cosmos-sdk/contrib/x/circuit/types"
	simutils "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/Konstellation-Network/konstellation/app"
	"github.com/Konstellation-Network/konstellation/app/config"
	compliancetypes "github.com/Konstellation-Network/konstellation/x/compliance/types"
)

const (
	chainID    = config.ChainIDLocal
	evmChainID = config.EVMChainIDLocal
	gasLimit   = uint64(1_000_000)

	// complianceTimelock is short so a scheduled block-list add executes
	// inside a test (types.MinTimelock is a minute). It is also the
	// emergency-freeze lifetime.
	complianceTimelock = 5 * time.Minute
)

var localChain = testconstants.ChainID{ChainID: chainID, EVMChainID: evmChainID}

func init() {
	config.SetBech32Prefixes(sdk.GetConfig())
	// cosmos/evm's harness looks the chain's coin up in these maps, keyed by
	// Cosmos chain-id and by EIP-155 id respectively.
	info := evmtypes.EvmCoinInfo{
		Denom:         config.BaseDenom,
		ExtendedDenom: config.BaseDenom,
		DisplayDenom:  config.DisplayDenom,
		Decimals:      config.Decimals.Uint32(),
	}
	testconstants.ExampleChainCoinInfo[localChain] = info
	testconstants.ChainsCoinInfo[evmChainID] = info
}

// konsApp adapts KonstellationApp to cosmos/evm's TestApp: DefaultGenesis
// takes no chain-id there, and the harness needs a compliance authority and
// a circuit-breaker admin it holds the keys for (a real network sets both
// in genesis: the foundation multisig and the 3-of-5 operations multisig).
type konsApp struct {
	*app.KonstellationApp
	authority string
}

var _ evm.IntegrationNetworkApp = konsApp{}

func (a konsApp) DefaultGenesis() map[string]json.RawMessage {
	gen := a.KonstellationApp.DefaultGenesis(chainID)
	gs := compliancetypes.DefaultGenesisState()
	gs.Params.Authority = a.authority
	gs.Params.Timelock = complianceTimelock
	gs.Params.AllowlistAddTimelock = complianceTimelock
	gen[compliancetypes.ModuleName] = a.AppCodec().MustMarshalJSON(gs)
	// The same key is the circuit breaker's super admin, the way a network
	// genesis grants the operations multisig (ENGINEERING.md §13.1). The
	// rest of the circuit genesis — D16's disable list — is the app's own,
	// so these tests run against what `konstellationd init` writes.
	var cg circuittypes.GenesisState
	a.AppCodec().MustUnmarshalJSON(gen[circuittypes.ModuleName], &cg)
	cg.AccountPermissions = []*circuittypes.GenesisAccountPermissions{{
		Address:     a.authority,
		Permissions: &circuittypes.Permissions{Level: circuittypes.Permissions_LEVEL_SUPER_ADMIN},
	}}
	gen[circuittypes.ModuleName] = a.AppCodec().MustMarshalJSON(&cg)
	return gen
}

// harness is one chain: the app, the tx factory, and the keys it can sign
// with. keys[0] is the compliance authority; the rest are plain users.
type harness struct {
	t         *testing.T
	app       *app.KonstellationApp
	nw        *network.UnitTestNetwork
	factory   factory.TxFactory
	grpc      grpc.Handler
	keyring   testkeyring.Keyring
	authority testkeyring.Key
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	kr := testkeyring.New(4)
	authority := kr.GetKey(0)

	var built *app.KonstellationApp
	create := func(cid string, evmID uint64, baseOpts ...func(*baseapp.BaseApp)) evm.EvmApp {
		home, err := os.MkdirTemp("", "konstellation-integration")
		require.NoError(t, err)
		t.Cleanup(func() { _ = os.RemoveAll(home) })
		opts := simutils.AppOptionsMap{
			flags.FlagHome:                              home,
			srvflags.EVMChainID:                         evmID,
			srvflags.EVMMempoolInsertQueueSize:          5000,
			srvflags.EVMMempoolPendingTxProposalTimeout: "250ms",
		}
		built = app.New(log.NewNopLogger(), dbm.NewMemDB(), true, opts, append(baseOpts, baseapp.SetChainID(cid))...)
		return testapp.NewEvmAppAdapter(konsApp{built, authority.AccAddr.String()})
	}

	nw := network.NewUnitTestNetwork(create,
		network.WithChainID(localChain),
		network.WithAmountOfValidators(1),
		network.WithPreFundedAccounts(kr.GetAllAccAddrs()...),
	)
	t.Cleanup(func() { _ = built.Close() })
	gh := grpc.NewIntegrationHandler(nw)
	return &harness{
		t:         t,
		app:       built,
		nw:        nw,
		factory:   factory.New(nw, gh),
		grpc:      gh,
		keyring:   kr,
		authority: authority,
	}
}

func (h *harness) key(i int) testkeyring.Key { return h.keyring.GetKey(i) }

func (h *harness) ctx() sdk.Context { return h.nw.GetContext() }

// nextBlock commits the pending block. The EVM mempool's tx pool reads
// state from a background goroutine; cosmos/evm's harness serialises that
// against Commit only when the app's mempool is its own type, and ours is
// wrapped by the compliance pre-check, so take the lock here.
func (h *harness) nextBlock() {
	h.t.Helper()
	h.nextBlockAfter(time.Second)
}

func (h *harness) nextBlockAfter(d time.Duration) {
	h.t.Helper()
	if mp, ok := h.app.GetMempool().(interface{ GetBlockchain() *evmmempool.Blockchain }); ok {
		if bc := mp.GetBlockchain(); bc != nil {
			bc.BeginCommit()
			defer bc.EndCommit()
		}
	}
	require.NoError(h.t, h.nw.NextBlockAfter(d))
}

// ── balances ──────────────────────────────────────────────────────────────

func (h *harness) balance(addr common.Address) *big.Int {
	h.t.Helper()
	res, err := h.grpc.GetBalanceFromBank(sdk.AccAddress(addr.Bytes()), config.BaseDenom)
	require.NoError(h.t, err)
	return res.Balance.Amount.BigInt()
}

// ── EVM txs ───────────────────────────────────────────────────────────────

// sendEVM signs and delivers an EVM tx from key and commits the block. It
// returns the delivery result and the error the factory derived from it
// (non-nil when the tx failed at the ante stage or reverted).
func (h *harness) sendEVM(key testkeyring.Key, args evmtypes.EvmTxArgs) (abcitypes.ExecTxResult, error) {
	h.t.Helper()
	res, err := h.factory.ExecuteEthTx(key.Priv, args)
	h.nextBlock()
	return res, err
}

// checkTxEVM runs an EVM tx through ABCI CheckTx only — the mempool
// admission path, with the compliance pre-check in front of the ante
// handler — without including it in a block.
func (h *harness) checkTxEVM(key testkeyring.Key, args evmtypes.EvmTxArgs) *abcitypes.ResponseCheckTx {
	h.t.Helper()
	signed, err := h.factory.GenerateSignedEthTx(key.Priv, args)
	require.NoError(h.t, err)
	bz, err := h.factory.EncodeTx(signed)
	require.NoError(h.t, err)
	res, err := h.nw.CheckTx(bz)
	require.NoError(h.t, err)
	return res
}

// checkTxCosmos runs a Cosmos tx through ABCI CheckTx only.
func (h *harness) checkTxCosmos(key testkeyring.Key, msgs ...sdk.Msg) *abcitypes.ResponseCheckTx {
	h.t.Helper()
	gas := uint64(500_000)
	signed, err := h.factory.BuildCosmosTx(key.Priv, basefactory.CosmosTxArgs{
		Msgs: msgs, Gas: &gas, GasPrice: ptr(sdkmath.NewInt(1_000_000_000)),
	})
	require.NoError(h.t, err)
	bz, err := h.factory.EncodeTx(signed)
	require.NoError(h.t, err)
	res, err := h.nw.CheckTx(bz)
	require.NoError(h.t, err)
	return res
}

func (h *harness) transfer(from testkeyring.Key, to common.Address, amount *big.Int) (abcitypes.ExecTxResult, error) {
	h.t.Helper()
	return h.sendEVM(from, evmtypes.EvmTxArgs{To: &to, Amount: amount, GasLimit: 21_000})
}

func (h *harness) deploy(from testkeyring.Key, c evmtypes.CompiledContract) common.Address {
	h.t.Helper()
	addr, err := h.factory.DeployContract(from.Priv, evmtypes.EvmTxArgs{GasLimit: 3_000_000}, testutiltypes.ContractDeploymentData{
		Contract: c,
	})
	require.NoError(h.t, err)
	h.nextBlock()
	return addr
}

func (h *harness) call(from testkeyring.Key, to common.Address, c evmtypes.CompiledContract, method string, args ...interface{}) (abcitypes.ExecTxResult, error) {
	h.t.Helper()
	res, err := h.factory.ExecuteContractCall(from.Priv, evmtypes.EvmTxArgs{To: &to, GasLimit: gasLimit}, testutiltypes.CallArgs{
		ContractABI: c.ABI, MethodName: method, Args: args,
	})
	h.nextBlock()
	return res, err
}

// query is eth_call: it runs code without a transaction, so it sees what an
// internal call from another contract would see.
func (h *harness) query(to common.Address, c evmtypes.CompiledContract, method string, args ...interface{}) ([]byte, error) {
	h.t.Helper()
	res, err := h.factory.QueryContract(evmtypes.EvmTxArgs{To: &to}, testutiltypes.CallArgs{
		ContractABI: c.ABI, MethodName: method, Args: args,
	}, gasLimit)
	if err != nil {
		return nil, err
	}
	return res.Ret, nil
}

// ── EIP-7702 ──────────────────────────────────────────────────────────────

// signedAuthorization is key's authorization to install delegate's code on
// its own account. The nonce is the key's *next* nonce when key also sends
// the tx (the sender's nonce is bumped before authorizations are processed),
// and its current nonce when someone else relays it.
func (h *harness) signedAuthorization(key testkeyring.Key, delegate common.Address, selfSent bool) ethtypes.SetCodeAuthorization {
	h.t.Helper()
	acc, err := h.grpc.GetEvmAccount(key.Addr)
	require.NoError(h.t, err)
	nonce := acc.GetNonce()
	if selfSent {
		nonce++
	}
	priv, err := key.Priv.(*ethsecp256k1.PrivKey).ToECDSA()
	require.NoError(h.t, err)
	auth, err := ethtypes.SignSetCode(priv, ethtypes.SetCodeAuthorization{
		ChainID: *uint256.NewInt(evmChainID),
		Address: delegate,
		Nonce:   nonce,
	})
	require.NoError(h.t, err)
	return auth
}

// setCode sends a type-4 tx from sender carrying auth.
func (h *harness) setCode(sender testkeyring.Key, auth ethtypes.SetCodeAuthorization) (abcitypes.ExecTxResult, error) {
	h.t.Helper()
	return h.sendEVM(sender, evmtypes.EvmTxArgs{
		To:                &common.Address{},
		GasLimit:          gasLimit,
		AuthorizationList: []ethtypes.SetCodeAuthorization{auth},
	})
}

// delegationOf returns the delegate installed on addr, if any.
func (h *harness) delegationOf(addr common.Address) (common.Address, bool) {
	h.t.Helper()
	ek := h.app.GetEVMKeeper()
	ctx := h.ctx()
	return ethtypes.ParseDelegation(ek.GetCode(ctx, ek.GetCodeHash(ctx, addr)))
}

// ── compliance ────────────────────────────────────────────────────────────

func (h *harness) sendCosmos(key testkeyring.Key, msgs ...sdk.Msg) (abcitypes.ExecTxResult, error) {
	h.t.Helper()
	gas := uint64(500_000)
	res, err := h.factory.ExecuteCosmosTx(key.Priv, basefactory.CosmosTxArgs{
		Msgs:     msgs,
		Gas:      &gas,
		GasPrice: ptr(sdkmath.NewInt(1_000_000_000)), // 1 gwei, above the initial base fee
	})
	h.nextBlock()
	return res, err
}

func (h *harness) emergencyFreeze(addrs ...common.Address) abcitypes.ExecTxResult {
	h.t.Helper()
	msg := &compliancetypes.MsgEmergencyFreeze{Authority: h.authority.AccAddr.String(), Reason: "integration test"}
	for _, a := range addrs {
		msg.Addresses = append(msg.Addresses, a.Hex())
	}
	res, err := h.sendCosmos(h.authority, msg)
	require.NoError(h.t, err, res.Log)
	require.Zero(h.t, res.Code, res.Log)
	return res
}

func (h *harness) liftEmergencyFreeze(addrs ...common.Address) {
	h.t.Helper()
	msg := &compliancetypes.MsgLiftEmergencyFreeze{Authority: h.authority.AccAddr.String()}
	for _, a := range addrs {
		msg.Addresses = append(msg.Addresses, a.Hex())
	}
	res, err := h.sendCosmos(h.authority, msg)
	require.NoError(h.t, err, res.Log)
	require.Zero(h.t, res.Code, res.Log)
}

// scheduleBlock schedules a permanent block-list add and returns when it
// executes.
func (h *harness) scheduleBlock(addr common.Address) time.Time {
	h.t.Helper()
	res, err := h.sendCosmos(h.authority, &compliancetypes.MsgScheduleUpdate{
		Authority: h.authority.AccAddr.String(),
		Changes: []compliancetypes.Change{{
			Address: addr.Hex(), List: compliancetypes.LIST_BLOCK, Action: compliancetypes.ACTION_ADD, Reason: "integration test",
		}},
	})
	require.NoError(h.t, err, res.Log)
	require.Zero(h.t, res.Code, res.Log)
	return h.ctx().BlockTime().Add(complianceTimelock)
}

func (h *harness) isFrozen(addr common.Address) bool {
	return h.app.ComplianceKeeper.IsFrozen(h.ctx(), addr.Bytes())
}

func ptr[T any](v T) *T { return &v }
