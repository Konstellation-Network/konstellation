package e2e

import (
	"testing"

	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/require"

	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// TestEVMTransferToModuleAccountRejected is ENGINEERING.md §4.1.1's missing
// upstream test, at the JSON-RPC. The x/vm guard fires at stateDB commit,
// inside a block, and cosmos/evm does not index a tx that fails there: the
// user would be charged gas and eth_getTransactionReceipt would say "not
// found" (this test pinned that on 2026-09-19 before the fix). Now
// app/blocked_recipient.go refuses the transfer at submission, so
// eth_sendRawTransaction returns the reason and nothing is charged.
func TestEVMTransferToModuleAccountRejected(t *testing.T) {
	k := startChain(t)
	user := k.newFundedEVMAccount(10)
	sink := k.newFundedEVMAccount(1)
	require.Equal(t, 0, kash(10).Cmp(k.balance(user.addr)), "funding did not land")

	for _, name := range []string{authtypes.FeeCollectorName, stakingtypes.BondedPoolName} {
		t.Run(name, func(t *testing.T) {
			target := moduleAddress(name)
			targetBefore := k.balance(target)
			userBefore := k.balance(user.addr)

			err := k.send(k.signedTransfer(user, target, oneKASH))
			require.Error(t, err, "eth_sendRawTransaction accepted a transfer to the %s module account", name)
			require.Contains(t, err.Error(), "is not allowed to receive funds")
			require.Contains(t, err.Error(), name)

			if name == stakingtypes.BondedPoolName {
				require.Equal(t, 0, targetBefore.Cmp(k.balance(target)), "bonded pool balance moved")
			}
			require.Equal(t, 0, userBefore.Cmp(k.balance(user.addr)), "sender was charged for a refused tx")
		})
	}

	// The account is not stuck: its nonce was never consumed, so the next
	// ordinary transfer goes straight through.
	tx := k.signedTransfer(user, sink.addr, oneKASH)
	require.NoError(t, k.send(tx))
	require.Equal(t, ethtypes.ReceiptStatusSuccessful, k.receipt(tx).Status)
	require.Equal(t, 0, kash(2).Cmp(k.balance(sink.addr)))
}
