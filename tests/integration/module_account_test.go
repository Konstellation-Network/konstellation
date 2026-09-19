//go:build test

package integration

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	distrtypes "github.com/cosmos/cosmos-sdk/x/distribution/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// TestEVMTransferToModuleAccountRejected covers the gap ENGINEERING.md §4.1.1
// records: x/vm's only bank write, SetBalanceWithLocked, refuses module
// accounts, but upstream has no test for it. A native transfer from the EVM
// to a module account must fail and move nothing but the sender's gas.
func TestEVMTransferToModuleAccountRejected(t *testing.T) {
	h := newHarness(t)
	user := h.key(1)
	amount := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil) // 1 KASH
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

			res, err := h.transfer(user, target, amount)
			require.Error(t, err, "transfer to module account %s was accepted", mod)
			require.NotZero(t, res.Code)
			// The guard is the one §4.1.1 traced: SetBalanceWithLocked in x/vm.
			require.Contains(t, res.Log, "is not allowed to receive funds")

			// The pools only move on (un)delegation, so they must be exactly
			// unchanged. The fee collector and distribution receive issuance
			// and fees every block, so for them the check is on the sender.
			if mod == stakingtypes.BondedPoolName || mod == stakingtypes.NotBondedPoolName {
				require.Equal(t, 0, targetBefore.Cmp(h.balance(target)), "pool balance moved")
			}
			// The sender paid gas for the failed tx (ante state persists) but
			// not the amount.
			lost := new(big.Int).Sub(senderBefore, h.balance(user.Addr))
			require.Equal(t, 1, lost.Sign(), "failed tx charged no gas")
			require.Equal(t, -1, lost.Cmp(amount), "sender lost the full amount (%s): transfer went through", lost)
		})
	}
}
