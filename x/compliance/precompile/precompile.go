// Package precompile exposes the compliance lists to Solidity at a fixed
// address. Read-only: every list mutation goes through x/compliance messages
// (timelocked authority or governance), never through the EVM.
package precompile

import (
	"bytes"
	"context"
	"fmt"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/vm"

	_ "embed"

	cmn "github.com/cosmos/evm/precompiles/common"

	storetypes "github.com/cosmos/cosmos-sdk/store/v2/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// Address is where the precompile lives. 0x900 is the first address of the
// Konstellation range; cosmos/evm's own precompiles occupy 0x100–0x807.
const Address = "0x0000000000000000000000000000000000000900"

const (
	IsVerifiedMethod = "isVerified"
	IsFrozenMethod   = "isFrozen"
	StatusMethod     = "status"
)

//go:embed abi.json
var abiJSON []byte

// ABI is the parsed interface (ICompliance.sol).
var ABI abi.ABI

func init() {
	var err error
	ABI, err = abi.JSON(bytes.NewReader(abiJSON))
	if err != nil {
		panic(err)
	}
}

var _ vm.PrecompiledContract = &Precompile{}

// Precompile is the compliance precompile.
type Precompile struct {
	cmn.Precompile
	abi.ABI
	keeper Keeper
}

// Keeper is what the precompile needs from x/compliance.
type Keeper interface {
	IsVerified(ctx context.Context, addr []byte) bool
	IsFrozen(ctx context.Context, addr []byte) bool
	FrozenUntilUnix(ctx sdk.Context, addr []byte) (frozen bool, until uint64)
}

// NewPrecompile returns the precompile bound to k.
func NewPrecompile(k Keeper) *Precompile {
	return &Precompile{
		Precompile: cmn.Precompile{
			KvGasConfig:          storetypes.KVGasConfig(),
			TransientKVGasConfig: storetypes.TransientGasConfig(),
			ContractAddress:      common.HexToAddress(Address),
		},
		ABI:    ABI,
		keeper: k,
	}
}

// Name is the precompile's name.
func (Precompile) Name() string { return "compliance" }

// RequiredGas: read-cost only; nothing here is a transaction.
func (p Precompile) RequiredGas(input []byte) uint64 {
	if len(input) < 4 {
		return 0
	}
	if _, err := p.MethodById(input[:4]); err != nil {
		return 0
	}
	return p.Precompile.RequiredGas(input, false)
}

// IsTransaction: no method mutates state.
func (Precompile) IsTransaction(*abi.Method) bool { return false }

// Run dispatches a call.
func (p Precompile) Run(evm *vm.EVM, contract *vm.Contract, readonly bool) ([]byte, error) {
	return p.RunNativeAction(evm, contract, func(ctx sdk.Context) ([]byte, error) {
		return p.Execute(ctx, contract, readonly)
	})
}

// Execute runs one method against ctx.
func (p Precompile) Execute(ctx sdk.Context, contract *vm.Contract, readOnly bool) ([]byte, error) {
	method, args, err := cmn.SetupABI(p.ABI, contract, readOnly, p.IsTransaction)
	if err != nil {
		return nil, err
	}
	if len(args) != 1 {
		return nil, fmt.Errorf("%s: expected 1 argument, got %d", method.Name, len(args))
	}
	account, ok := args[0].(common.Address)
	if !ok {
		return nil, fmt.Errorf("%s: argument is not an address", method.Name)
	}
	addr := account.Bytes()

	switch method.Name {
	case IsVerifiedMethod:
		return method.Outputs.Pack(p.keeper.IsVerified(ctx, addr))
	case IsFrozenMethod:
		return method.Outputs.Pack(p.keeper.IsFrozen(ctx, addr))
	case StatusMethod:
		frozen, until := p.keeper.FrozenUntilUnix(ctx, addr)
		return method.Outputs.Pack(p.keeper.IsVerified(ctx, addr), frozen, until)
	default:
		return nil, fmt.Errorf(cmn.ErrUnknownMethod, method.Name)
	}
}
