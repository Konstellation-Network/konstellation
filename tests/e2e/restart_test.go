package e2e

import (
	"testing"

	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/require"
)

// TestRestartThenFirstEVMTx is the STATUS.md §2a regression (konstellation
// PR #8): a node restarted on existing state panicked on the first EVM tx it
// saw, because the SDK's fee-recipient global was only set by the first
// *Cosmos* tx in the process. Only a real process restart reproduces it.
func TestRestartThenFirstEVMTx(t *testing.T) {
	k := startChain(t)
	user := k.newFundedEVMAccount(10)
	sink := k.newFundedEVMAccount(1)
	k.waitBlocks()

	require.NoError(t, k.chain.StopAllNodes(k.ctx))
	require.NoError(t, k.chain.StartAllNodes(k.ctx))
	k.dialEVM()
	k.waitBlocks()

	// First tx of any kind after the restart is an EVM transfer.
	tx := k.signedTransfer(user, sink.addr, oneKASH)
	require.NoError(t, k.send(tx), "restarted node rejected the first EVM tx")
	r := k.receipt(tx)
	require.Equal(t, ethtypes.ReceiptStatusSuccessful, r.Status)
	require.Equal(t, 0, kash(2).Cmp(k.balance(sink.addr)))

	// And the node is still alive afterwards (the original failure was a
	// panic in the mempool recheck).
	k.waitBlocks()
}
