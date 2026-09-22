package app

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	distributionprecompile "github.com/cosmos/evm/precompiles/distribution"
	ics20precompile "github.com/cosmos/evm/precompiles/ics20"
	stakingprecompile "github.com/cosmos/evm/precompiles/staking"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

func TestNamedAddresses(t *testing.T) {
	alice := common.HexToAddress("0x1111111111111111111111111111111111111111")
	bob := common.HexToAddress("0x2222222222222222222222222222222222222222")
	bobBech := sdk.AccAddress(bob.Bytes()).String()
	valoper := sdk.ValAddress(alice.Bytes()).String()

	// distribution.setWithdrawAddress(address delegator, string withdrawer)
	m := distributionprecompile.ABI.Methods[distributionprecompile.SetWithdrawAddressMethod]
	packed, err := m.Inputs.Pack(alice, bobBech)
	require.NoError(t, err)
	args, err := m.Inputs.Unpack(packed)
	require.NoError(t, err)
	require.Equal(t, [][]byte{alice.Bytes(), bob.Bytes()}, namedAddresses(args))

	// staking.delegate(address delegator, string validator, uint256): the
	// validator operator address is not an account address.
	m = stakingprecompile.ABI.Methods[stakingprecompile.DelegateMethod]
	packed, err = m.Inputs.Pack(alice, valoper, big.NewInt(1))
	require.NoError(t, err)
	args, err = m.Inputs.Unpack(packed)
	require.NoError(t, err)
	require.Equal(t, [][]byte{alice.Bytes()}, namedAddresses(args))

	// ics20.transfer: sender (address) and a receiver in either form; a
	// foreign bech32 receiver is not ours and is skipped.
	m = ics20precompile.ABI.Methods[ics20precompile.TransferMethod]
	for receiver, want := range map[string][][]byte{
		bob.Hex(): {alice.Bytes(), bob.Bytes()},
		"cosmos1yg7ln9rmlz2hjhqc5rnl9xll0z0d9lnjtxd3ug": {alice.Bytes()},
	} {
		packed, err = m.Inputs.Pack("transfer", "channel-0", "esp", big.NewInt(1), alice, receiver, struct {
			RevisionNumber uint64
			RevisionHeight uint64
		}{}, uint64(0), "")
		require.NoError(t, err)
		args, err = m.Inputs.Unpack(packed)
		require.NoError(t, err)
		require.Equal(t, want, namedAddresses(args), receiver)
	}
}
