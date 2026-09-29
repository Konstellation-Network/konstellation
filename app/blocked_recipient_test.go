package app

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	erc20precompile "github.com/cosmos/evm/precompiles/erc20"
)

func TestERC20TransferParties(t *testing.T) {
	from := common.HexToAddress("0x1111111111111111111111111111111111111111")
	to := common.HexToAddress("0x2222222222222222222222222222222222222222")

	pack := func(method string, args ...any) []byte {
		bz, err := erc20precompile.ABI.Pack(method, args...)
		require.NoError(t, err)
		return bz
	}
	require.Equal(t, []common.Address{to}, erc20TransferParties(pack("transfer", to, big.NewInt(1))))
	require.Equal(t, []common.Address{from, to}, erc20TransferParties(pack("transferFrom", from, to, big.NewInt(1))))
	// Not a transfer: the spender of approve is not a party to a move.
	require.Nil(t, erc20TransferParties(pack("approve", to, big.NewInt(1))))
	require.Nil(t, erc20TransferParties(pack("balanceOf", to)))
	// Malformed: unknown selector, truncated arguments.
	require.Nil(t, erc20TransferParties([]byte{0xde, 0xad, 0xbe, 0xef}))
	require.Nil(t, erc20TransferParties(pack("transfer", to, big.NewInt(1))[:20]))
}
