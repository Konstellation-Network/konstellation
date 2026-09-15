package app

import (
	"testing"

	"cosmossdk.io/math"

	"github.com/Konstellation-Network/konstellation/app/config"
)

func kashInt(n int64) math.Int {
	return math.NewIntWithDecimal(1, int(config.Decimals)).MulRaw(n)
}

func TestAnnualIssuance(t *testing.T) {
	one := math.NewIntWithDecimal(1, int(config.Decimals))
	f := math.LegacyNewDec(166)

	t.Run("zero bonded ⇒ zero", func(t *testing.T) {
		got, err := AnnualIssuance(f, math.ZeroInt())
		if err != nil || !got.IsZero() {
			t.Fatalf("got %s, %v", got, err)
		}
	})

	t.Run("F × √bonded, Ethereum sanity point", func(t *testing.T) {
		// 166 × √30,000,000 ≈ 909,241 KASH/yr ⇒ ≈3.03 % on 30 M bonded.
		got, err := AnnualIssuance(f, kashInt(30_000_000))
		if err != nil {
			t.Fatal(err)
		}
		gotKash := got.QuoInt(one)
		if gotKash.LT(math.LegacyNewDec(909_000)) || gotKash.GT(math.LegacyNewDec(909_500)) {
			t.Fatalf("annual = %s KASH, want ≈909,241", gotKash)
		}
		apr := got.QuoInt(kashInt(30_000_000))
		if apr.LT(math.LegacyMustNewDecFromStr("0.0302")) || apr.GT(math.LegacyMustNewDecFromStr("0.0304")) {
			t.Fatalf("APR = %s, want ≈0.0303", apr)
		}
	})

	t.Run("4× the stake ⇒ 2× the issuance, half the yield", func(t *testing.T) {
		a, _ := AnnualIssuance(f, kashInt(1_000_000))
		b, _ := AnnualIssuance(f, kashInt(4_000_000))
		ratio := b.Quo(a)
		if ratio.LT(math.LegacyMustNewDecFromStr("1.9999")) || ratio.GT(math.LegacyMustNewDecFromStr("2.0001")) {
			t.Fatalf("issuance ratio = %s, want 2", ratio)
		}
	})

	t.Run("sub-KASH bonded still positive and exact-ish", func(t *testing.T) {
		// 0.25 KASH bonded ⇒ √0.25 = 0.5 ⇒ 83 KASH/yr
		got, err := AnnualIssuance(f, one.QuoRaw(4))
		if err != nil {
			t.Fatal(err)
		}
		if want := math.LegacyNewDec(83).MulInt(one); !got.Sub(want).Abs().LT(math.LegacyOneDec()) {
			t.Fatalf("got %s want %s", got, want)
		}
	})

	t.Run("decided F = 1265: 8 % APR at 25 % of a 1 B supply bonded", func(t *testing.T) {
		if !config.MintIssuanceFactor.Equal(math.LegacyNewDec(1265)) {
			t.Fatalf("MintIssuanceFactor = %s; changing it is a D4 re-decision, update ENGINEERING.md and this test", config.MintIssuanceFactor)
		}
		bonded := kashInt(250_000_000)
		got, err := AnnualIssuance(config.MintIssuanceFactor, bonded)
		if err != nil {
			t.Fatal(err)
		}
		apr := got.QuoInt(bonded)
		if apr.LT(math.LegacyMustNewDecFromStr("0.0799")) || apr.GT(math.LegacyMustNewDecFromStr("0.0801")) {
			t.Fatalf("APR at 250 M bonded = %s, want 0.08", apr)
		}
		// 20 M KASH/yr = 2 % of the 1 B genesis supply
		if yr := got.QuoInt(one); yr.LT(math.LegacyNewDec(19_990_000)) || yr.GT(math.LegacyNewDec(20_010_000)) {
			t.Fatalf("annual = %s KASH, want ≈20 M", yr)
		}
	})

	t.Run("deterministic", func(t *testing.T) {
		x, _ := AnnualIssuance(config.MintIssuanceFactor, kashInt(123_456_789))
		y, _ := AnnualIssuance(config.MintIssuanceFactor, kashInt(123_456_789))
		if !x.Equal(y) {
			t.Fatal("ApproxSqrt not deterministic")
		}
	})
}

func TestMintGenesisParamsValidate(t *testing.T) {
	gs := NewMintGenesisState()
	if err := gs.Params.Validate(); err != nil {
		t.Fatal(err)
	}
	if gs.Params.MintDenom != config.BaseDenom {
		t.Fatalf("mint denom %q", gs.Params.MintDenom)
	}
	if gs.Params.BlocksPerYear != config.MintBlocksPerYear {
		t.Fatalf("blocks per year %d", gs.Params.BlocksPerYear)
	}
	if !gs.Params.InflationMin.IsZero() || !gs.Params.InflationMax.IsZero() || !gs.Params.InflationRateChange.IsZero() {
		t.Fatal("SDK inflation-band params must be zeroed: they are not enforced")
	}
}
