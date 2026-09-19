//go:build test

package integration

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	evmtypes "github.com/cosmos/evm/x/vm/types"

	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	distrtypes "github.com/cosmos/cosmos-sdk/x/distribution/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/Konstellation-Network/konstellation/app/config"
)

// TestEVMTransferToModuleAccountRejected covers the gap ENGINEERING.md §4.1.1
// records: x/vm's only bank write, SetBalanceWithLocked, refuses module
// accounts, but upstream has no test for it. A native transfer from the EVM
// to a module account must fail and move nothing.
//
// Since app/blocked_recipient.go the refusal comes from the ante handler —
// at CheckTx with the reason, and at delivery with no gas charged — instead
// of from the stateDB commit inside a block, where cosmos/evm would not
// index the failed tx and eth_getTransactionReceipt would say "not found".
func TestEVMTransferToModuleAccountRejected(t *testing.T) {
	h := newHarness(t)
	user := h.key(1)
	amount := oneKASH
	for _, mod := range []string{
		authtypes.FeeCollectorName,
		distrtypes.ModuleName,
		stakingtypes.BondedPoolName,
		stakingtypes.NotBondedPoolName,
	} {
		t.Run(mod, func(t *testing.T) {
			target := common.BytesToAddress(authtypes.NewModuleAddress(mod))
			targetBefore := h.balance(target)
			senderBefore := h.balance(user.Addr)
			xfer := evmtypes.EvmTxArgs{To: &target, Amount: amount, GasLimit: 21_000}

			// Mempool admission: refused synchronously, naming the module.
			chk := h.checkTxEVM(user, xfer)
			require.NotZero(t, chk.Code)
			require.Contains(t, chk.Log, "is not allowed to receive funds")
			require.Contains(t, chk.Log, mod)

			// Delivery (a proposer that skipped CheckTx): ante rejection, so
			// the tx never executes and nothing — not even gas — is charged.
			res, err := h.transfer(user, target, amount)
			require.Error(t, err, "transfer to module account %s was accepted", mod)
			require.NotZero(t, res.Code)
			require.Contains(t, res.Log, "is not allowed to receive funds")

			if mod == stakingtypes.BondedPoolName || mod == stakingtypes.NotBondedPoolName {
				require.Equal(t, 0, targetBefore.Cmp(h.balance(target)), "pool balance moved")
			}
			require.Equal(t, 0, senderBefore.Cmp(h.balance(user.Addr)), "sender charged for an ante rejection")
		})
	}

	// Precompiles are on the bank's blocked list too.
	t.Run("precompile", func(t *testing.T) {
		target := common.HexToAddress(config.CompliancePrecompileAddress)
		senderBefore := h.balance(user.Addr)
		res, err := h.transfer(user, target, amount)
		require.Error(t, err)
		require.Contains(t, res.Log, "is not allowed to receive funds")
		require.Equal(t, 0, senderBefore.Cmp(h.balance(user.Addr)))
	})

	// A zero-value call to a blocked address is not a transfer and is left
	// to the EVM (the guard only fires on a balance change).
	t.Run("zero value call passes ante", func(t *testing.T) {
		target := common.BytesToAddress(authtypes.NewModuleAddress(authtypes.FeeCollectorName))
		chk := h.checkTxEVM(user, evmtypes.EvmTxArgs{To: &target, Amount: big.NewInt(0), GasLimit: 21_000})
		require.Zero(t, chk.Code, chk.Log)
	})
}
