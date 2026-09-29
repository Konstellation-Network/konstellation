package app

import (
	"context"
	"fmt"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	corevm "github.com/ethereum/go-ethereum/core/vm"

	cmn "github.com/cosmos/evm/precompiles/common"
	"github.com/cosmos/evm/x/vm/statedb"

	errorsmod "cosmossdk.io/errors"

	storetypes "github.com/cosmos/cosmos-sdk/store/v2/types"

	compliancetypes "github.com/Konstellation-Network/konstellation/x/compliance/types"
)

// The tx-path precompiles (staking, distribution, gov, slashing, ICS20,
// ICS02) act for contract.Caller(): the staking precompile delegates the
// caller's own KASH, distribution redirects and withdraws the caller's
// rewards, gov votes the caller's stake. A frozen EOA never reaches them
// (the ante refuses its txs), but a frozen *contract* runs whenever anyone
// calls it, and x/bank's DelegateCoins moves stake around the send
// restriction — so a frozen contract could still delegate, point its
// rewards at a clean address and vote (PR #15 review, 2026-09-22).
//
// complianceGuard wraps such a precompile the way circuitGuard does: a tx
// method reverts, with the reason, when the caller is frozen or when any
// address it names is — the delegator/voter/sender arguments (which
// cosmos/evm requires to equal the caller anyway), a withdraw address, an
// ICS20 receiver. Query methods are untouched: reading is not touching.

// complianceGuard is a precompile whose tx methods refuse frozen parties.
type complianceGuard struct {
	abiPrecompile
	keeper freezeChecker
}

type freezeChecker interface {
	IsFrozen(ctx context.Context, addr []byte) bool
	Enforce(ctx context.Context) bool
}

// Run refuses a tx method that involves a frozen address, then defers.
func (g complianceGuard) Run(evm *corevm.EVM, contract *corevm.Contract, readonly bool) ([]byte, error) {
	if len(contract.Input) >= 4 {
		if method, err := g.MethodById(contract.Input[:4]); err == nil && g.IsTransaction(method) {
			if err := g.check(evm, contract, method); err != nil {
				return cmn.ReturnRevertError(evm, err)
			}
		}
	}
	return g.abiPrecompile.Run(evm, contract, readonly)
}

func (g complianceGuard) check(evm *corevm.EVM, contract *corevm.Contract, method *abi.Method) error {
	stateDB, ok := evm.StateDB.(*statedb.StateDB)
	if !ok {
		return fmt.Errorf("%s", cmn.ErrNotRunInEvm)
	}
	ctx, err := stateDB.GetCacheContext()
	if err != nil {
		return err
	}
	// A few KV reads; the EVM already charged RequiredGas for the call.
	ctx = ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	if !g.keeper.Enforce(ctx) {
		return nil
	}
	if g.keeper.IsFrozen(ctx, contract.Caller().Bytes()) {
		return errorsmod.Wrapf(compliancetypes.ErrAddressFrozen, "caller %s", compliancetypes.Bech32(contract.Caller().Bytes()))
	}
	// Arguments that do not unpack are the precompile's own error to report.
	args, err := method.Inputs.Unpack(contract.Input[4:])
	if err != nil {
		return nil
	}
	for _, addr := range namedAddresses(args) {
		if g.keeper.IsFrozen(ctx, addr) {
			return errorsmod.Wrapf(compliancetypes.ErrAddressFrozen, "%s named in %s", compliancetypes.Bech32(addr), method.Name)
		}
	}
	return nil
}

// namedAddresses returns the 20-byte addresses among args: `address`
// arguments, and `string` arguments that parse as an account address in
// either form (a validator address or a memo does not). Structs and arrays
// are not walked.
func namedAddresses(args []any) [][]byte {
	var out [][]byte
	for _, a := range args {
		switch v := a.(type) {
		case common.Address:
			out = append(out, v.Bytes())
		case string:
			if b, err := compliancetypes.ParseAddress(v); err == nil {
				out = append(out, b)
			}
		}
	}
	return out
}

// withComplianceGuard wraps every precompile in m that precompileMsgTypes
// lists — the tx-path set circuitGuard covers.
func withComplianceGuard(m map[common.Address]corevm.PrecompiledContract, k freezeChecker) map[common.Address]corevm.PrecompiledContract {
	for hexAddr := range precompileMsgTypes {
		addr := common.HexToAddress(hexAddr)
		p, present := m[addr]
		if !present {
			continue
		}
		ap, ok := p.(abiPrecompile)
		if !ok {
			panic(fmt.Sprintf("precompile %s does not expose its ABI; cannot guard it", hexAddr))
		}
		m[addr] = complianceGuard{abiPrecompile: ap, keeper: k}
	}
	return m
}
