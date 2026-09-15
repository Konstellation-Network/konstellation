package app

import (
	"context"
	"strconv"

	feemarkettypes "github.com/cosmos/evm/x/feemarket/types"

	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"github.com/Konstellation-Network/konstellation/app/config"
)

// Base-fee burn (ENGINEERING.md D5, decided 2026-09-15).
//
// Every tx pays gas × (baseFee + tip) into the fee collector: EVM txs via the
// x/vm ante handler (unused gas refunded after execution, so the collector
// keeps exactly gasUsed × effectiveGasPrice), Cosmos txs via DeductFee with the
// feemarket DynamicFeeChecker enforcing gasPrice ≥ baseFee. The SDK default is
// for x/distribution to sweep the whole collector to validators at the start
// of the next block. Ethereum semantics are that the base-fee part is
// destroyed and only the tip is revenue. So, at the end of every block and
// before x/distribution can see it, burn baseFee × gasUsed from the collector.
//
// Why EndBlock and not a per-tx post handler: post-handler state is discarded
// when a tx's messages fail, while the tx still paid its fee and still counts
// toward block gas. EndBlock sees the whole block. It uses the same BlockGasUsed
// (Σ tx GasUsed, failed txs included) that x/feemarket's EndBlock feeds into
// the next base fee, so "what the base fee is charged on" and "what is burned"
// are the same number. The base fee is set in feemarket's BeginBlock and is
// constant for the block; it is snapshotted before the module EndBlockers run
// (see app.EndBlocker for the ordering and why the burn itself runs after them).
//
// Known imprecisions, both leaving more with validators rather than less:
//   - a Cosmos tx pays for gasWanted but only baseFee × gasUsed is burned; the
//     baseFee × (gasWanted − gasUsed) slack goes to validators like a tip.
//   - a tx that fails in the ante handler pays nothing but its ante gas is
//     still in BlockGasUsed, so the burn is over-counted by baseFee × that gas,
//     taken from other txs' tips. Only a proposer can put such a tx in a block.
//     The clamp below means this can never touch anything but this block's
//     fees.

// feeBurnBank is the slice of x/bank the burn needs.
type feeBurnBank interface {
	GetBalance(ctx context.Context, addr sdk.AccAddress, denom string) sdk.Coin
	BurnCoins(ctx context.Context, moduleName string, amt sdk.Coins) error
}

// feeMarketParams is the slice of x/feemarket the burn needs.
type feeMarketParams interface {
	GetParams(ctx sdk.Context) feemarkettypes.Params
}

// snapshotBaseFee returns the base fee every tx in this block was charged
// against, or nil when there is none: feemarket's NoBaseFee, or a height
// below EnableHeight — the Cosmos fee checker treats the base fee as 0 there
// while GetBaseFee would still return the stored value, and burning it would
// eat validator tips (PR #5 review).
func snapshotBaseFee(ctx sdk.Context, fm feeMarketParams) math.LegacyDec {
	p := fm.GetParams(ctx)
	if !p.IsBaseFeeEnabled(ctx.BlockHeight()) {
		return math.LegacyDec{}
	}
	return p.BaseFee
}

const (
	EventTypeBaseFeeBurn     = "base_fee_burn"
	AttributeKeyBaseFee      = "base_fee"
	AttributeKeyBlockGasUsed = "block_gas_used"
	AttributeKeyBurned       = "burned"
	AttributeKeyClamped      = "clamped_to_balance"
)

// BaseFeeBurnAmount is the pure computation: baseFee × gasUsed in base units,
// truncated. A nil base fee (feemarket NoBaseFee) burns nothing.
func BaseFeeBurnAmount(baseFee math.LegacyDec, blockGasUsed uint64) math.Int {
	if baseFee.IsNil() || !baseFee.IsPositive() || blockGasUsed == 0 {
		return math.ZeroInt()
	}
	return baseFee.MulInt(math.NewIntFromUint64(blockGasUsed)).TruncateInt()
}

// burnBaseFee burns baseFee × BlockGasUsed of the base denom from the fee
// collector, clamped to what the collector actually holds. baseFee is the
// value from snapshotBaseFee, taken before the module EndBlockers ran.
func burnBaseFee(ctx sdk.Context, bank feeBurnBank, baseFee math.LegacyDec) error {
	gasUsed := ctx.BlockGasUsed()
	want := BaseFeeBurnAmount(baseFee, gasUsed)
	if !want.IsPositive() {
		return nil
	}

	collector := authtypes.NewModuleAddress(authtypes.FeeCollectorName)
	have := bank.GetBalance(ctx, collector, config.BaseDenom).Amount
	burn := math.MinInt(want, have)
	clamped := burn.LT(want)
	if clamped {
		ctx.Logger().Error("base-fee burn exceeds fee collector balance; clamping",
			"want", want.String(), "have", have.String(), "base_fee", baseFee.String(), "block_gas_used", gasUsed)
	}
	if !burn.IsPositive() {
		return nil
	}

	if err := bank.BurnCoins(ctx, authtypes.FeeCollectorName, sdk.NewCoins(sdk.NewCoin(config.BaseDenom, burn))); err != nil {
		return err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		EventTypeBaseFeeBurn,
		sdk.NewAttribute(AttributeKeyBaseFee, baseFee.String()),
		sdk.NewAttribute(AttributeKeyBlockGasUsed, math.NewIntFromUint64(gasUsed).String()),
		sdk.NewAttribute(AttributeKeyBurned, burn.String()),
		sdk.NewAttribute(AttributeKeyClamped, strconv.FormatBool(clamped)),
	))
	return nil
}
