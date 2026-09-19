//go:build test

package integration

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"

	"github.com/cosmos/evm/crypto/ethsecp256k1"
	testkeyring "github.com/cosmos/evm/testutil/keyring"
)

// UserOperation mirrors the ERC-4337 v0.6 struct cosmos/evm's SimpleEntryPoint
// test contract takes. Field names must match the ABI so the packer maps them.
type UserOperation struct {
	Sender               common.Address
	Nonce                *big.Int
	InitCode             []byte
	CallData             []byte
	CallGasLimit         *big.Int
	VerificationGasLimit *big.Int
	PreVerificationGas   *big.Int
	MaxFeePerGas         *big.Int
	MaxPriorityFeePerGas *big.Int
	PaymasterAndData     []byte
	Signature            []byte
}

// signedUserOp builds a UserOperation for sender's smart account, signed by
// owner over the hash SimpleEntryPoint computes. The signature is the whole
// point: it is made off-chain, by a key the chain may have frozen.
func signedUserOp(t *testing.T, owner testkeyring.Key, sender, entryPoint common.Address, nonce uint64, callData []byte) UserOperation {
	t.Helper()
	op := UserOperation{
		Sender:               sender,
		Nonce:                new(big.Int).SetUint64(nonce),
		InitCode:             []byte{},
		CallData:             callData,
		CallGasLimit:         big.NewInt(100_000),
		VerificationGasLimit: big.NewInt(200_000),
		PreVerificationGas:   big.NewInt(50_000),
		MaxFeePerGas:         big.NewInt(900_000_000),
		MaxPriorityFeePerGas: big.NewInt(100_000_000),
		PaymasterAndData:     []byte{},
	}

	addressT, _ := abi.NewType("address", "", nil)
	uint256T, _ := abi.NewType("uint256", "", nil)
	bytes32T, _ := abi.NewType("bytes32", "", nil)
	packed, err := abi.Arguments{
		{Type: addressT},
		{Type: uint256T},
		{Type: bytes32T},
		{Type: bytes32T},
		{Type: uint256T},
		{Type: uint256T},
		{Type: uint256T},
		{Type: uint256T},
		{Type: uint256T},
		{Type: bytes32T},
		{Type: addressT},
		{Type: uint256T},
	}.Pack(
		op.Sender, op.Nonce,
		crypto.Keccak256Hash(op.InitCode), crypto.Keccak256Hash(op.CallData),
		op.CallGasLimit, op.VerificationGasLimit, op.PreVerificationGas, op.MaxFeePerGas, op.MaxPriorityFeePerGas,
		crypto.Keccak256Hash(op.PaymasterAndData), entryPoint, new(big.Int).SetUint64(evmChainID),
	)
	require.NoError(t, err)

	priv, err := owner.Priv.(*ethsecp256k1.PrivKey).ToECDSA()
	require.NoError(t, err)
	sig, err := crypto.Sign(crypto.Keccak256(packed), priv)
	require.NoError(t, err)
	if sig[64] < 27 {
		sig[64] += 27 // ecrecover wants 27/28
	}
	op.Signature = sig
	return op
}
