package keeper

import (
	"context"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"

	evmtypes "github.com/cosmos/evm/x/vm/types"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/Konstellation-Network/konstellation/x/compliance/types"
)

// EVMKeeper is the slice of x/vm the keeper needs to undo an EIP-7702
// delegation on an account it has just frozen. *evmkeeper.Keeper satisfies it.
type EVMKeeper interface {
	GetCodeHash(ctx sdk.Context, addr common.Address) common.Hash
	GetCode(ctx sdk.Context, codeHash common.Hash) []byte
	DeleteCodeHash(ctx sdk.Context, addr common.Address)
}

// late holds the references wired after construction, behind a pointer so
// every copy of the Keeper (module, precompile, ante, bank wrappers) sees
// them once the setters run. The compliance keeper is built before the EVM
// and IBC keepers — the precompile and the staking bank wrapper need it —
// so these cannot be constructor arguments.
type late struct {
	evm EVMKeeper
	// isEscrow reports whether addr is an ICS-20 escrow account; see
	// SetEscrowChecker.
	isEscrow func(ctx sdk.Context, addr []byte) bool
}

// SetEVMKeeper wires the EVM keeper. app.New always calls it; a keeper
// without one (unit tests) leaves delegations alone.
func (k Keeper) SetEVMKeeper(evm EVMKeeper) { k.late.evm = evm }

// HasEVMKeeper reports whether SetEVMKeeper has run. Pinned by an app test so
// the wiring cannot be dropped silently.
func (k Keeper) HasEVMKeeper() bool { return k.late.evm != nil }

// SetEscrowChecker wires the test for ICS-20 escrow accounts, which join
// the protected set: freezing one would refuse every unescrow and refund on
// its channel (the escrow is the *sender* there, and senders are never
// exempt) — a stuck channel until the entry is lifted, for no compliance
// gain, since an escrow never acts on anyone's behalf (PR #15 review).
func (k Keeper) SetEscrowChecker(fn func(ctx sdk.Context, addr []byte) bool) { k.late.isEscrow = fn }

// HasEscrowChecker reports whether SetEscrowChecker has run.
func (k Keeper) HasEscrowChecker() bool { return k.late.isEscrow != nil }

// resetDelegation removes the EIP-7702 delegation on addr, if it carries
// one, and emits the delegate it removed. Called on every path that puts an
// address on the block list.
//
// Why: the ante handler stops a frozen EOA from signing anything, including
// a new 7702 authorization, and from being the direct `to` of a tx. It
// cannot see internal calls. An EOA delegated *before* the freeze still runs
// its delegate's code when any contract calls it — a smart-account wallet
// with a session key, an ERC-4337 EntryPoint relaying a UserOp signed
// off-chain, a sweep contract — so its funds stay movable by anyone with a
// clean relayer. Clearing the delegation at freeze time closes that; once
// frozen, nothing can re-delegate, so the reset is final until the freeze
// is lifted.
//
// Only the code hash goes. Storage at the address is untouched, so an owner
// whose freeze is lifted restores the account with one new authorization.
// Real contract bytecode is never touched: freezing a contract address must
// not brick its holders, and ParseDelegation only matches the 23-byte
// 0xef0100‖address form.
//
// Genesis import does not call this: a fresh chain has no code, and an
// exported state's entries were frozen — and reset — on the chain that
// exported them.
func (k Keeper) resetDelegation(ctx context.Context, addr []byte, by string) {
	if k.late.evm == nil {
		return
	}
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	ethAddr := common.BytesToAddress(addr)
	codeHash := k.late.evm.GetCodeHash(sdkCtx, ethAddr)
	if evmtypes.IsEmptyCodeHash(codeHash.Bytes()) {
		return
	}
	delegate, ok := ethtypes.ParseDelegation(k.late.evm.GetCode(sdkCtx, codeHash))
	if !ok {
		return
	}
	k.late.evm.DeleteCodeHash(sdkCtx, ethAddr)
	sdkCtx.EventManager().EmitEvent(sdk.NewEvent(types.EventTypeDelegationReset,
		sdk.NewAttribute(types.AttributeKeyAddress, types.Bech32(addr)),
		sdk.NewAttribute(types.AttributeKeyDelegate, delegate.Hex()),
		sdk.NewAttribute(types.AttributeKeyBy, by),
	))
}
