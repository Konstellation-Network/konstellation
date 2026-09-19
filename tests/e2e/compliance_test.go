package e2e

import (
	"testing"

	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/require"
)

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
