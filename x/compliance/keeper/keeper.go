package keeper

import (
	"context"
	"fmt"
	"time"

	"cosmossdk.io/collections"
	"cosmossdk.io/core/store"
	"cosmossdk.io/log/v2"

	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/Konstellation-Network/konstellation/x/compliance/types"
)

// Keeper owns the two lists, the timelocked queue, and the params.
//
// State:
//
//	Params      the module params (authority, timelocks, enforce)
//	Allow       address bytes → ListEntry, the "verified" list
//	Block       address bytes → ListEntry, the "frozen" list
//	Pending     id → PendingUpdate, scheduled changes waiting out a timelock
//	PendingSeq  next pending id
//	ExecIndex   (execute_at unix, id) so EndBlock finds due updates without a scan
//	ExpiryIndex (expires_at unix, address) so EndBlock finds lapsed emergency freezes
//	Cooldown    address → unix time before which it may not be emergency-frozen again
type Keeper struct {
	cdc          codec.BinaryCodec
	govAuthority string
	// govAddr is govAuthority's 20 bytes: the one sender the bank send
	// restriction lets deposit into a frozen address (restriction.go).
	govAddr []byte
	// protected are addresses that may never be put on the block list:
	// module accounts and precompiles (freezing them is meaningless and
	// makes the precompile lie to contracts) and the governance account.
	// The current list authority is protected dynamically, see isProtected.
	protected map[string]struct{}
	// evm is set by SetEVMKeeper after the EVM keeper exists; see delegation.go.
	evm *evmRef

	Schema      collections.Schema
	Params      collections.Item[types.Params]
	Allow       collections.Map[[]byte, types.ListEntry]
	Block       collections.Map[[]byte, types.ListEntry]
	Pending     collections.Map[uint64, types.PendingUpdate]
	PendingSeq  collections.Sequence
	ExecIndex   collections.KeySet[collections.Pair[int64, uint64]]
	ExpiryIndex collections.KeySet[collections.Pair[int64, []byte]]
	Cooldown    collections.Map[[]byte, int64]
}

// NewKeeper builds the keeper. govAuthority is x/gov's module address: the
// only signer that may GovUpdate / UpdateParams, and a superset of what the
// list authority may do.
//
// protected is the set of bech32 addresses that can never be frozen (the
// app passes its module accounts and precompiles); the gov authority is
// always included.
func NewKeeper(cdc codec.BinaryCodec, storeService store.KVStoreService, govAuthority string, protected []string) Keeper {
	govAddr, err := sdk.AccAddressFromBech32(govAuthority)
	if err != nil {
		panic(fmt.Errorf("compliance: invalid gov authority %q: %w", govAuthority, err))
	}
	prot := make(map[string]struct{}, len(protected)+1)
	for _, a := range append(protected, govAuthority) {
		b, err := types.ParseAddress(a)
		if err != nil {
			panic(fmt.Errorf("compliance: invalid protected address %q: %w", a, err))
		}
		prot[string(b)] = struct{}{}
	}
	sb := collections.NewSchemaBuilder(storeService)
	k := Keeper{
		cdc:          cdc,
		govAuthority: govAuthority,
		govAddr:      govAddr,
		protected:    prot,
		evm:          &evmRef{},
		Params:       collections.NewItem(sb, types.ParamsKey, "params", codec.CollValue[types.Params](cdc)),
		Allow:        collections.NewMap(sb, types.AllowListKey, "allow", collections.BytesKey, codec.CollValue[types.ListEntry](cdc)),
		Block:        collections.NewMap(sb, types.BlockListKey, "block", collections.BytesKey, codec.CollValue[types.ListEntry](cdc)),
		Pending:      collections.NewMap(sb, types.PendingKey, "pending", collections.Uint64Key, codec.CollValue[types.PendingUpdate](cdc)),
		PendingSeq:   collections.NewSequence(sb, types.PendingSeqKey, "pending_seq"),
		ExecIndex:    collections.NewKeySet(sb, types.ExecutionIdxKey, "exec_index", collections.PairKeyCodec(collections.Int64Key, collections.Uint64Key)),
		ExpiryIndex:  collections.NewKeySet(sb, types.ExpiryIndexKey, "expiry_index", collections.PairKeyCodec(collections.Int64Key, collections.BytesKey)),
		Cooldown:     collections.NewMap(sb, types.CooldownKey, "cooldown", collections.BytesKey, collections.Int64Value),
	}
	schema, err := sb.Build()
	if err != nil {
		panic(err)
	}
	k.Schema = schema
	return k
}

// GovAuthority is the governance module address.
func (k Keeper) GovAuthority() string { return k.govAuthority }

// Logger returns a module-tagged logger.
func (k Keeper) Logger(ctx context.Context) log.Logger {
	return sdk.UnwrapSDKContext(ctx).Logger().With("module", "x/"+types.ModuleName)
}

// GetParams returns the params, defaulting if unset (only before InitGenesis).
func (k Keeper) GetParams(ctx context.Context) types.Params {
	p, err := k.Params.Get(ctx)
	if err != nil {
		return types.DefaultParams()
	}
	return p
}

// isGov reports whether signer is the governance module.
func (k Keeper) isGov(signer string) bool { return signer == k.govAuthority }

// isListAuthority reports whether signer is the configured list authority.
func (k Keeper) isListAuthority(ctx context.Context, signer string) bool {
	a := k.GetParams(ctx).Authority
	return a != "" && signer == a
}

// requireAuthorityOrGov gates the list-authority actions.
func (k Keeper) requireAuthorityOrGov(ctx context.Context, signer string) error {
	if k.isGov(signer) || k.isListAuthority(ctx, signer) {
		return nil
	}
	if k.GetParams(ctx).Authority == "" {
		return types.ErrNoAuthority
	}
	return types.ErrUnauthorized
}

// ── list reads ────────────────────────────────────────────────────────────

// IsVerified reports whether addr is on the allow list.
func (k Keeper) IsVerified(ctx context.Context, addr []byte) bool {
	ok, err := k.Allow.Has(ctx, addr)
	return err == nil && ok
}

// IsFrozen reports whether addr is on the block list and, for an emergency
// freeze, not yet expired. Expiry is judged against block time here as well
// as in EndBlock, so a lapsed freeze stops binding at the exact block it
// expires even before the entry is swept.
func (k Keeper) IsFrozen(ctx context.Context, addr []byte) bool {
	e, err := k.Block.Get(ctx, addr)
	if err != nil {
		return false
	}
	return e.ExpiresAt == nil || sdk.UnwrapSDKContext(ctx).BlockTime().Before(*e.ExpiresAt)
}

// FrozenUntil returns (frozen, expiry): expiry is nil for a permanent entry.
func (k Keeper) FrozenUntil(ctx context.Context, addr []byte) (bool, *time.Time) {
	e, err := k.Block.Get(ctx, addr)
	if err != nil {
		return false, nil
	}
	if e.ExpiresAt != nil && !sdk.UnwrapSDKContext(ctx).BlockTime().Before(*e.ExpiresAt) {
		return false, nil
	}
	return true, e.ExpiresAt
}

// Entries returns the entries present for addr (0, 1 or 2).
func (k Keeper) Entries(ctx context.Context, addr []byte) []types.ListEntry {
	var out []types.ListEntry
	if e, err := k.Allow.Get(ctx, addr); err == nil {
		out = append(out, e)
	}
	if e, err := k.Block.Get(ctx, addr); err == nil {
		out = append(out, e)
	}
	return out
}

// Enforce reports whether the ante-handler check is switched on.
func (k Keeper) Enforce(ctx context.Context) bool { return k.GetParams(ctx).Enforce }

// FrozenUntilUnix is FrozenUntil for the precompile: until is 0 when not
// frozen or permanently frozen.
func (k Keeper) FrozenUntilUnix(ctx sdk.Context, addr []byte) (bool, uint64) {
	frozen, until := k.FrozenUntil(ctx, addr)
	if !frozen || until == nil {
		return frozen, 0
	}
	return true, uint64(until.Unix()) //nolint:gosec // block times are positive
}

// isProtected reports whether addr may never be frozen: a module account, a
// precompile, governance, or whoever is currently the list authority (so
// the authority cannot lock itself out with a fat-fingered batch — a
// lockout that would take a full governance cycle to undo on mainnet).
func (k Keeper) isProtected(ctx context.Context, addr []byte) bool {
	if _, ok := k.protected[string(addr)]; ok {
		return true
	}
	if a := k.GetParams(ctx).Authority; a != "" {
		if b, err := types.ParseAddress(a); err == nil && string(b) == string(addr) {
			return true
		}
	}
	return false
}

// checkFreezable rejects a block-list add on a protected address.
func (k Keeper) checkFreezable(ctx context.Context, addr []byte) error {
	if k.isProtected(ctx, addr) {
		return types.ErrProtectedAddress.Wrapf("%s", types.Bech32(addr))
	}
	return nil
}
