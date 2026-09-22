package e2e

import (
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/require"
)

// wkashAddr is cosmos/evm's werc20 precompile for the native token
// (app/config.WKASHPrecompile); erc20ABI the three ERC-20 methods the test
// needs of it.
var (
	wkashAddr = common.HexToAddress("0xD4949664cD82660AaE99bEdc034a0deA8A0bd517")
	erc20ABI  = mustABI(`[
		{"type":"function","name":"approve","inputs":[{"name":"spender","type":"address"},{"name":"amount","type":"uint256"}],"outputs":[{"type":"bool"}]},
		{"type":"function","name":"transfer","inputs":[{"name":"to","type":"address"},{"name":"amount","type":"uint256"}],"outputs":[{"type":"bool"}]},
		{"type":"function","name":"transferFrom","inputs":[{"name":"from","type":"address"},{"name":"to","type":"address"},{"name":"amount","type":"uint256"}],"outputs":[{"type":"bool"}]}
	]`)
)

func mustABI(s string) abi.ABI {
	a, err := abi.JSON(strings.NewReader(s))
	if err != nil {
		panic(err)
	}
	return a
}

// TestFrozenAddressRejectedAtJSONRPC: the list authority freezes an address
// through the CLI; the node then refuses the frozen party's EVM txs at
// eth_sendRawTransaction, synchronously, with the reason. That path —
// rpc/backend → Mempool.Insert, bypassing ABCI CheckTx — is the one
// app/mempool.go's wrapper exists for, and the one tests/integration cannot
// reach.
func TestFrozenAddressRejectedAtJSONRPC(t *testing.T) {
	k := startChain(t)
	frozen := k.newFundedEVMAccount(10)
	clean := k.newFundedEVMAccount(10)
	sink := k.newFundedEVMAccount(1)

	// Before the freeze the account works.
	tx := k.signedTransfer(frozen, sink.addr, oneKASH)
	require.NoError(t, k.send(tx))
	require.Equal(t, ethtypes.ReceiptStatusSuccessful, k.receipt(tx).Status)

	k.emergencyFreeze(frozen.bech32())
	require.True(t, k.isFrozen(frozen.bech32()))
	require.True(t, k.isFrozen(frozen.addr.Hex()), "status query should accept 0x form too")

	// Frozen sender: refused at submission, never enters the mempool.
	err := k.send(k.signedTransfer(frozen, sink.addr, oneKASH))
	require.Error(t, err)
	require.Contains(t, err.Error(), "address is frozen")

	// Frozen recipient: same.
	err = k.send(k.signedTransfer(clean, frozen.addr, oneKASH))
	require.Error(t, err)
	require.Contains(t, err.Error(), "address is frozen")

	// A clean transfer still goes through on the same node.
	tx = k.signedTransfer(clean, sink.addr, oneKASH)
	require.NoError(t, k.send(tx))
	require.Equal(t, ethtypes.ReceiptStatusSuccessful, k.receipt(tx).Status)
	require.Equal(t, 0, kash(3).Cmp(k.balance(sink.addr)))
}

// TestChainIdentity pins what `konstellationd init` derives from the
// genesis chain-id: the EIP-155 id the JSON-RPC reports must be the local
// one, and the node must be producing blocks under it.
func TestChainIdentity(t *testing.T) {
	k := startChain(t)
	id, err := k.eth.ChainID(k.ctx)
	require.NoError(t, err)
	require.EqualValues(t, evmChainID, id.Int64())
	net, err := k.eth.NetworkID(k.ctx)
	require.NoError(t, err)
	require.EqualValues(t, evmChainID, net.Int64())
	k.waitBlocks()
}

// TestFrozenAllowanceRefusedAtJSONRPC is STATUS.md §5a P20 against a real
// node: an allowance on the WKASH precompile granted before a freeze. The
// spender's transferFrom names the frozen address only in calldata — the
// tx's signer and `to` are clean — so before the fix it was accepted and
// mined, and the frozen account lost the funds (dev chain, 2026-09-21 and
// 2026-09-22). Now eth_sendRawTransaction refuses it with the reason, and
// eth_call shows the bank itself refusing inside the precompile, which is
// what a contract making the same call gets.
func TestFrozenAllowanceRefusedAtJSONRPC(t *testing.T) {
	k := startChain(t)
	frozen := k.newFundedEVMAccount(10)
	spender := k.newFundedEVMAccount(10)
	clean := k.newFundedEVMAccount(10)

	pack := func(method string, args ...any) []byte {
		bz, err := erc20ABI.Pack(method, args...)
		require.NoError(t, err)
		return bz
	}
	two := new(big.Int).Mul(oneKASH, big.NewInt(2))

	// The allowance, and proof it is live: one KASH moves before the freeze.
	tx := k.signedCall(frozen, wkashAddr, pack("approve", spender.addr, two), 100_000)
	require.NoError(t, k.send(tx))
	require.Equal(t, ethtypes.ReceiptStatusSuccessful, k.receipt(tx).Status)
	frozenBefore := k.balance(frozen.addr)
	tx = k.signedCall(spender, wkashAddr, pack("transferFrom", frozen.addr, spender.addr, oneKASH), 200_000)
	require.NoError(t, k.send(tx))
	require.Equal(t, ethtypes.ReceiptStatusSuccessful, k.receipt(tx).Status)
	require.Equal(t, 0, new(big.Int).Sub(frozenBefore, oneKASH).Cmp(k.balance(frozen.addr)), "pre-freeze transferFrom did not move funds")

	k.emergencyFreeze(frozen.bech32())
	require.True(t, k.isFrozen(frozen.bech32()))
	frozenBefore = k.balance(frozen.addr)

	// transferFrom on the remaining allowance: refused at submission.
	err := k.send(k.signedCall(spender, wkashAddr, pack("transferFrom", frozen.addr, spender.addr, oneKASH), 200_000))
	require.Error(t, err)
	require.Contains(t, err.Error(), "address is frozen")

	// Funding the frozen address through the precompile: same.
	err = k.send(k.signedCall(clean, wkashAddr, pack("transfer", frozen.addr, oneKASH), 200_000))
	require.Error(t, err)
	require.Contains(t, err.Error(), "address is frozen")

	// What an internal call sees: the bank refuses inside the precompile
	// and the EVM reverts with the reason.
	_, err = k.eth.CallContract(k.ctx, ethereum.CallMsg{From: spender.addr, To: &wkashAddr, Data: pack("transferFrom", frozen.addr, spender.addr, oneKASH)}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "address is frozen")

	require.Equal(t, 0, frozenBefore.Cmp(k.balance(frozen.addr)), "frozen balance moved")

	// A clean transfer between clean parties still works on the same node.
	tx = k.signedCall(clean, wkashAddr, pack("transfer", spender.addr, oneKASH), 200_000)
	require.NoError(t, k.send(tx))
	require.Equal(t, ethtypes.ReceiptStatusSuccessful, k.receipt(tx).Status)
}
