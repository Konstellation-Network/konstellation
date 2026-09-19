// Package e2e runs konstellationd as a real node — the Docker image built
// from ./Dockerfile — under interchaintest, and drives it the way operators
// and users do: the CLI over docker exec, CometBFT RPC/gRPC, and the EVM
// JSON-RPC. Single-node semantics with the app in-process live in
// tests/integration; this layer is for what only a real process shows:
// restarts, the JSON-RPC submission path, config written by `init`.
//
//	docker build -t konstellation:e2e .
//	make test-e2e
package e2e

import (
	"context"
	"crypto/ecdsa"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/cosmos/interchaintest/v10"
	"github.com/cosmos/interchaintest/v10/chain/cosmos"
	"github.com/cosmos/interchaintest/v10/ibc"
	"github.com/cosmos/interchaintest/v10/testutil"

	sdkmath "cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
)

// These mirror app/config; the e2e module deliberately does not import the
// app, so they are restated and checked against the running node in
// TestChainIdentity.
const (
	chainID      = "konstellation-local-1"
	evmChainID   = 56670
	bech32Prefix = "kons"
	denom        = "esp"
	chainName    = "konstellation"
	imageRepo    = chainName
	imageTag     = "e2e"

	// authorityKey is the x/compliance list authority, created on validator
	// 0 before genesis so its address can be written into the params.
	authorityKey = "authority"
	faucetKey    = "faucet"
)

var oneKASH = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)

type konsChain struct {
	t     *testing.T
	ctx   context.Context
	chain *cosmos.CosmosChain
	// authority is the bech32 address of authorityKey.
	authority string
	eth       *ethclient.Client
}

// startChain boots one validator from the image and returns it running.
func startChain(t *testing.T) *konsChain {
	t.Helper()
	if testing.Short() {
		t.Skip("e2e needs Docker; skipped in -short")
	}
	ctx := context.Background()
	one, zero := 1, 0
	decimals := int64(18)

	var authority string
	cfg := ibc.ChainConfig{
		Type:             "cosmos",
		Name:             chainName,
		ChainID:          chainID,
		Images:           []ibc.DockerImage{{Repository: imageRepo, Version: imageTag, UIDGID: "1025:1025"}},
		Bin:              "konstellationd",
		Bech32Prefix:     bech32Prefix,
		Denom:            denom,
		CoinType:         "60",
		SigningAlgorithm: "eth_secp256k1",
		CoinDecimals:     &decimals,
		// 1 gwei: the feemarket's initial base fee, and it only decays from
		// there on an idle chain (MinGasPrice is 0).
		GasPrices:      "1000000000" + denom,
		GasAdjustment:  1.5,
		TrustingPeriod: "336h",
		ConfigFileOverrides: map[string]any{
			"config/app.toml": testutil.Toml{"json-rpc": testutil.Toml{
				"enable":     true,
				"address":    "0.0.0.0:8545",
				"ws-address": "0.0.0.0:8546",
			}},
		},
		ExposeAdditionalPorts: []string{"8545/tcp"},
		PreGenesis: func(c ibc.Chain) error {
			val := c.(*cosmos.CosmosChain).Validators[0]
			if err := val.CreateKey(ctx, authorityKey); err != nil {
				return err
			}
			addr, err := val.AccountKeyBech32(ctx, authorityKey)
			if err != nil {
				return err
			}
			authority = addr
			return val.AddGenesisAccount(ctx, addr, []sdk.Coin{sdk.NewCoin(denom, sdkmath.NewInt(1_000_000).Mul(sdkmath.NewIntFromBigInt(oneKASH)))})
		},
		ModifyGenesis: func(cfg ibc.ChainConfig, genbz []byte) ([]byte, error) {
			return cosmos.ModifyGenesis([]cosmos.GenesisKV{
				cosmos.NewGenesisKV("app_state.compliance.params.authority", authority),
				// Short so an emergency freeze lapses inside a test if one
				// needs it to; x/compliance's MinTimelock is a minute.
				cosmos.NewGenesisKV("app_state.compliance.params.timelock", "120s"),
			})(cfg, genbz)
		},
	}

	cf := interchaintest.NewBuiltinChainFactory(zaptest.NewLogger(t), []*interchaintest.ChainSpec{{
		Name: chainName, ChainName: chainName, Version: imageTag,
		NumValidators: &one, NumFullNodes: &zero, ChainConfig: cfg,
	}})
	chains, err := cf.Chains(t.Name())
	require.NoError(t, err)
	chain := chains[0].(*cosmos.CosmosChain)

	ic := interchaintest.NewInterchain().AddChain(chain)
	client, network := interchaintest.DockerSetup(t)
	require.NoError(t, ic.Build(ctx, nil, interchaintest.InterchainBuildOptions{
		TestName: t.Name(), Client: client, NetworkID: network, SkipPathCreation: true,
	}))
	t.Cleanup(func() { _ = ic.Close() })

	k := &konsChain{t: t, ctx: ctx, chain: chain, authority: authority}
	k.dialEVM()
	return k
}

// dialEVM (re)connects the JSON-RPC client; the host port changes when the
// container is recreated.
func (k *konsChain) dialEVM() {
	k.t.Helper()
	url, err := k.chain.GetNode().GetHostAddress(k.ctx, "8545/tcp")
	require.NoError(k.t, err)
	var c *ethclient.Client
	require.Eventually(k.t, func() bool {
		c, err = ethclient.DialContext(k.ctx, url)
		if err != nil {
			return false
		}
		_, err = c.ChainID(k.ctx)
		return err == nil
	}, 60*time.Second, time.Second, "JSON-RPC at %s not answering", url)
	k.eth = c
}

func (k *konsChain) waitBlocks() {
	k.t.Helper()
	require.NoError(k.t, testutil.WaitForBlocks(k.ctx, 2, k.chain))
}

// ── accounts ──────────────────────────────────────────────────────────────

// evmAccount is a key the test holds itself and signs Ethereum txs with.
type evmAccount struct {
	priv *ecdsa.PrivateKey
	addr common.Address
}

func (a evmAccount) bech32() string { return bech32Of(a.addr) }

// newFundedEVMAccount makes a fresh key and funds it from the faucet with a
// bank send, i.e. a Cosmos tx through the CLI.
func (k *konsChain) newFundedEVMAccount(kash int64) evmAccount {
	k.t.Helper()
	priv, err := crypto.GenerateKey()
	require.NoError(k.t, err)
	acct := evmAccount{priv: priv, addr: crypto.PubkeyToAddress(priv.PublicKey)}
	k.fund(acct.bech32(), kash)
	return acct
}

func (k *konsChain) fund(bech32 string, kash int64) {
	k.t.Helper()
	require.NoError(k.t, k.chain.SendFunds(k.ctx, faucetKey, ibc.WalletAmount{
		Address: bech32, Denom: denom, Amount: sdkmath.NewInt(kash).Mul(sdkmath.NewIntFromBigInt(oneKASH)),
	}))
}

func (k *konsChain) balance(addr common.Address) *big.Int {
	k.t.Helper()
	b, err := k.eth.BalanceAt(k.ctx, addr, nil)
	require.NoError(k.t, err)
	return b
}

func bech32Of(addr common.Address) string {
	s, err := sdk.Bech32ifyAddressBytes(bech32Prefix, addr.Bytes())
	if err != nil {
		panic(err)
	}
	return s
}

// ── EVM txs over JSON-RPC ─────────────────────────────────────────────────

// signedTransfer builds a signed EIP-1559 value transfer.
func (k *konsChain) signedTransfer(from evmAccount, to common.Address, amount *big.Int) *ethtypes.Transaction {
	k.t.Helper()
	nonce, err := k.eth.PendingNonceAt(k.ctx, from.addr)
	require.NoError(k.t, err)
	tip, err := k.eth.SuggestGasTipCap(k.ctx)
	require.NoError(k.t, err)
	head, err := k.eth.HeaderByNumber(k.ctx, nil)
	require.NoError(k.t, err)
	feeCap := new(big.Int).Add(tip, new(big.Int).Mul(head.BaseFee, big.NewInt(2)))
	tx := ethtypes.NewTx(&ethtypes.DynamicFeeTx{
		ChainID: big.NewInt(evmChainID), Nonce: nonce, GasTipCap: tip, GasFeeCap: feeCap,
		Gas: 21_000, To: &to, Value: amount,
	})
	signed, err := ethtypes.SignTx(tx, ethtypes.LatestSignerForChainID(big.NewInt(evmChainID)), from.priv)
	require.NoError(k.t, err)
	return signed
}

// send submits over eth_sendRawTransaction and returns the node's immediate
// answer — nil when the mempool accepted it.
func (k *konsChain) send(tx *ethtypes.Transaction) error {
	return k.eth.SendTransaction(k.ctx, tx)
}

// receipt waits for the tx to be mined.
func (k *konsChain) receipt(tx *ethtypes.Transaction) *ethtypes.Receipt {
	k.t.Helper()
	var r *ethtypes.Receipt
	require.Eventually(k.t, func() bool {
		var err error
		r, err = k.eth.TransactionReceipt(k.ctx, tx.Hash())
		return err == nil && r != nil
	}, 60*time.Second, 500*time.Millisecond, "tx %s not mined", tx.Hash())
	return r
}

// ── compliance via the CLI ────────────────────────────────────────────────

func (k *konsChain) emergencyFreeze(addrs ...string) {
	k.t.Helper()
	args := append([]string{"compliance", "emergency-freeze"}, addrs...)
	args = append(args, "--reason", "e2e")
	_, err := k.chain.GetNode().ExecTx(k.ctx, authorityKey, args...)
	require.NoError(k.t, err)
}

func (k *konsChain) isFrozen(addr string) bool {
	k.t.Helper()
	out, _, err := k.chain.GetNode().ExecQuery(k.ctx, "compliance", "status", addr)
	require.NoError(k.t, err)
	// {"frozen":true,...}; the CLI prints protoJSON.
	return strings.Contains(string(out), `"frozen":true`) || strings.Contains(string(out), `"frozen": true`)
}

func moduleAddress(name string) common.Address {
	return common.BytesToAddress(authtypes.NewModuleAddress(name))
}

func kash(n int64) *big.Int { return new(big.Int).Mul(big.NewInt(n), oneKASH) }
