package app

import (
	"context"
	"fmt"

	"cosmossdk.io/math"

	"github.com/cosmos/cosmos-sdk/telemetry"
	sdk "github.com/cosmos/cosmos-sdk/types"
	mintkeeper "github.com/cosmos/cosmos-sdk/x/mint/keeper"
	minttypes "github.com/cosmos/cosmos-sdk/x/mint/types"

	"github.com/Konstellation-Network/konstellation/app/config"
)

// Stake-based issuance (ENGINEERING.md D4, decided 2026-09-15).
//
// Modelled on Ethereum post-merge: new supply per year is a pure function of
// how much is bonded, growing with its square root —
//
//	annual issuance (KASH) = MintIssuanceFactor × √(bonded KASH)
//
// so the yield every staker sees, issuance ÷ bonded = F ÷ √bonded, falls as
// more stake comes in, and the total paid out still rises. There is no
// bonded-ratio target and no rate-of-change loop (the SDK default's
// GoalBonded / InflationRateChange / InflationMin / InflationMax are inert
// here, see NewMintGenesisState). It pairs with the D5 base-fee burn for a
// net issuance-minus-burn supply.
//
// This is installed as the x/mint keeper's MintFn (SDK ≥ 0.53 hook): the
// module, its store, params, queries and the Minter record are all stock, so
// there is nothing new under x/ to audit beyond this file. The Minter's
// Inflation and AnnualProvisions are kept up to date every block so
// `query mint inflation` / explorers show the effective rate.
//
// Rounding: √ is math.LegacyDec.ApproxSqrt (Newton's method, fixed iteration
// cap, integer arithmetic — deterministic across nodes); the per-block
// provision is AnnualProvisions ÷ BlocksPerYear truncated to base units.
// BlocksPerYear is a gov param and must track real block time, exactly as on
// every SDK chain.
//
// Stake-concentration note (D4 row): the curve is chain-wide, distribution
// stays pro-rata through x/distribution, so a validator's share of issuance
// is its share of bonded stake regardless of set size — nothing here favours
// concentration beyond what MaxValidators = 30 already implies.

// bondedSource is the slice of x/staking the mint function needs.
type bondedSource interface {
	TotalValidatorPower(ctx context.Context) (math.Int, error)
}

// AnnualIssuance is the pure curve: F × √(bonded / 10^Decimals), returned in
// base units as a decimal. Zero bonded ⇒ zero issuance.
func AnnualIssuance(factor math.LegacyDec, bondedBase math.Int) (math.LegacyDec, error) {
	if !bondedBase.IsPositive() || !factor.IsPositive() {
		return math.LegacyZeroDec(), nil
	}
	one := math.NewIntWithDecimal(1, int(config.Decimals))
	bondedKash := math.LegacyNewDecFromInt(bondedBase).QuoInt(one)
	root, err := bondedKash.ApproxSqrt()
	if err != nil {
		return math.LegacyZeroDec(), fmt.Errorf("sqrt(bonded): %w", err)
	}
	return factor.Mul(root).MulInt(one), nil
}

// NewSqrtBondedMintFn returns the x/mint MintFn implementing the curve above.
func NewSqrtBondedMintFn(staking bondedSource) mintkeeper.MintFn {
	return func(ctx sdk.Context, k *mintkeeper.Keeper) error {
		minter, err := k.Minter.Get(ctx)
		if err != nil {
			return err
		}
		params, err := k.Params.Get(ctx)
		if err != nil {
			return err
		}

		bonded, err := staking.TotalValidatorPower(ctx)
		if err != nil {
			return err
		}
		annual, err := AnnualIssuance(config.MintIssuanceFactor, bonded)
		if err != nil {
			return err
		}

		// Keep the stock Minter record honest for queries and explorers:
		// Inflation is the effective annual rate against current supply.
		supply, err := k.StakingTokenSupply(ctx)
		if err != nil {
			return err
		}
		minter.AnnualProvisions = annual
		if supply.IsPositive() {
			minter.Inflation = annual.QuoInt(supply)
		} else {
			minter.Inflation = math.LegacyZeroDec()
		}
		if err := k.Minter.Set(ctx, minter); err != nil {
			return err
		}

		mintedCoin := minter.BlockProvision(params)

		// Same MaxSupply cap as the SDK default (0 = unlimited).
		if !params.MaxSupply.IsZero() && supply.Add(mintedCoin.Amount).GT(params.MaxSupply) {
			diff := params.MaxSupply.Sub(supply)
			if diff.IsNegative() {
				diff = math.ZeroInt()
			}
			mintedCoin.Amount = diff
		}

		mintedCoins := sdk.NewCoins(mintedCoin)
		if err := k.MintCoins(ctx, mintedCoins); err != nil {
			return err
		}
		if err := k.AddCollectedFees(ctx, mintedCoins); err != nil {
			return err
		}

		if mintedCoin.Amount.IsInt64() {
			defer telemetry.ModuleSetGauge(minttypes.ModuleName, float32(mintedCoin.Amount.Int64()), "minted_tokens")
		}

		bondedRatio, err := k.BondedRatio(ctx)
		if err != nil {
			return err
		}
		ctx.EventManager().EmitEvent(sdk.NewEvent(
			minttypes.EventTypeMint,
			sdk.NewAttribute(minttypes.AttributeKeyBondedRatio, bondedRatio.String()),
			sdk.NewAttribute(minttypes.AttributeKeyInflation, minter.Inflation.String()),
			sdk.NewAttribute(minttypes.AttributeKeyAnnualProvisions, minter.AnnualProvisions.String()),
			sdk.NewAttribute(sdk.AttributeKeyAmount, mintedCoin.Amount.String()),
		))
		return nil
	}
}
