package e2e

import (
	"fmt"
	"math/big"
	"testing"
	"time"

	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/require"

	abcitypes "github.com/cometbft/cometbft/abci/types"

	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// TestEVMTransferToModuleAccountRejected is ENGINEERING.md §4.1.1's missing
// upstream test, at the JSON-RPC: a signed transfer to a module account,
// submitted with eth_sendRawTransaction, must not move the funds. The
// in-process version (tests/integration) shows the guard; this shows what a
// user sees through the RPC — and that is worth knowing: the tx is accepted
// by the mempool (the ante cannot see the recipient's kind), fails in the
// block with the guard's error as an SDK tx result (code 4), and because a
// failed SDK tx is not indexed as an Ethereum tx, eth_getTransactionReceipt
// and eth_getTransactionByHash answer "not found". The reason is only on
// the CometBFT side (tx_search). eth_call / eth_estimateGas do not see the
// guard either: it sits in the stateDB commit, which a simulation never
// reaches.
func TestEVMTransferToModuleAccountRejected(t *testing.T) {
	k := startChain(t)
	user := k.newFundedEVMAccount(10)
	require.Equal(t, 0, kash(10).Cmp(k.balance(user.addr)), "funding did not land")

	for _, name := range []string{authtypes.FeeCollectorName, stakingtypes.BondedPoolName} {
		t.Run(name, func(t *testing.T) {
			target := moduleAddress(name)
			targetBefore := k.balance(target)
			userBefore := k.balance(user.addr)

			tx := k.signedTransfer(user, target, oneKASH)
			require.NoError(t, k.send(tx), "mempool should accept it; the guard is at execution")

			res := k.cosmosResult(tx)
			require.NotZero(t, res.Code)
			require.Contains(t, res.Log, "is not allowed to receive funds")
			_, err := k.eth.TransactionReceipt(k.ctx, tx.Hash())
			require.ErrorContains(t, err, "not found", "a failed SDK tx is not indexed as an Ethereum tx")

			if name == stakingtypes.BondedPoolName {
				require.Equal(t, 0, targetBefore.Cmp(k.balance(target)), "bonded pool balance moved")
			}
			lost := new(big.Int).Sub(userBefore, k.balance(user.addr))
			require.Equal(t, 1, lost.Sign(), "failed tx charged no gas")
			require.Equal(t, -1, lost.Cmp(oneKASH), "sender lost the full amount: transfer went through")
		})
	}
}

// cosmosResult finds the SDK tx result for an Ethereum tx by the hash the
// EVM module indexes it under, and waits for it to be in a block.
func (k *konsChain) cosmosResult(tx *ethtypes.Transaction) abcitypes.ExecTxResult {
	k.t.Helper()
	var out abcitypes.ExecTxResult
	require.Eventually(k.t, func() bool {
		res, err := k.chain.GetNode().Client.TxSearch(k.ctx,
			fmt.Sprintf("ethereum_tx.ethereumTxHash='%s'", tx.Hash().Hex()), false, nil, nil, "")
		if err != nil || len(res.Txs) == 0 {
			return false
		}
		out = res.Txs[0].TxResult
		return true
	}, 60*time.Second, 500*time.Millisecond, "tx %s never made it into a block", tx.Hash())
	return out
}
