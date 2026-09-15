package app

import (
	"context"
	"testing"

	"cosmossdk.io/log/v2"
	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"github.com/Konstellation-Network/konstellation/app/config"
)

type fakeBank struct {
	balance math.Int
	burned  sdk.Coins
	module  string
}

func (b *fakeBank) GetBalance(_ context.Context, _ sdk.AccAddress, denom string) sdk.Coin {
	return sdk.NewCoin(denom, b.balance)
}

func (b *fakeBank) BurnCoins(_ context.Context, module string, amt sdk.Coins) error {
	b.module = module
	b.burned = amt
	b.balance = b.balance.Sub(amt.AmountOf(config.BaseDenom))
	return nil
}

type fakeFeemarket struct{ baseFee math.LegacyDec }

func (f fakeFeemarket) GetBaseFee(sdk.Context) math.LegacyDec { return f.baseFee }

func burnCtx(gasUsed uint64) sdk.Context {
	return sdk.Context{}.
		WithLogger(log.NewNopLogger()).
		WithEventManager(sdk.NewEventManager()).
		WithBlockGasUsed(gasUsed)
}

func TestBaseFeeBurnAmount(t *testing.T) {
	gwei := math.LegacyNewDec(1_000_000_000)
	cases := []struct {
		name    string
		baseFee math.LegacyDec
		gas     uint64
		want    math.Int
	}{
		{"nil base fee (NoBaseFee)", math.LegacyDec{}, 21_000, math.ZeroInt()},
		{"zero base fee", math.LegacyZeroDec(), 21_000, math.ZeroInt()},
		{"zero gas", gwei, 0, math.ZeroInt()},
		{"1 gwei × 21000", gwei, 21_000, math.NewInt(21_000_000_000_000)},
		{"fractional base fee truncates", math.LegacyNewDecWithPrec(15, 1), 3, math.NewInt(4)}, // 1.5 × 3 = 4.5 → 4
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := BaseFeeBurnAmount(tc.baseFee, tc.gas); !got.Equal(tc.want) {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}

func TestBurnBaseFee(t *testing.T) {
	gwei := math.LegacyNewDec(1_000_000_000)

	t.Run("burns baseFee × gasUsed from the fee collector", func(t *testing.T) {
		bank := &fakeBank{balance: math.NewInt(100_000_000_000_000)} // 100k gwei: covers fee + tip
		ctx := burnCtx(21_000)
		if err := burnBaseFee(ctx, bank, fakeFeemarket{gwei}); err != nil {
			t.Fatal(err)
		}
		if bank.module != authtypes.FeeCollectorName {
			t.Fatalf("burned from %q, want fee collector", bank.module)
		}
		if got, want := bank.burned.AmountOf(config.BaseDenom), math.NewInt(21_000_000_000_000); !got.Equal(want) {
			t.Fatalf("burned %s want %s", got, want)
		}
		// the tip share is untouched for x/distribution
		if want := math.NewInt(79_000_000_000_000); !bank.balance.Equal(want) {
			t.Fatalf("collector left with %s want %s", bank.balance, want)
		}
		evs := ctx.EventManager().Events()
		if len(evs) != 1 || evs[0].Type != EventTypeBaseFeeBurn {
			t.Fatalf("expected one %s event, got %v", EventTypeBaseFeeBurn, evs)
		}
	})

	t.Run("clamps to collector balance and says so", func(t *testing.T) {
		bank := &fakeBank{balance: math.NewInt(5)}
		ctx := burnCtx(21_000)
		if err := burnBaseFee(ctx, bank, fakeFeemarket{gwei}); err != nil {
			t.Fatal(err)
		}
		if !bank.burned.AmountOf(config.BaseDenom).Equal(math.NewInt(5)) || !bank.balance.IsZero() {
			t.Fatalf("expected the whole 5 burned, got %s left %s", bank.burned, bank.balance)
		}
		var clamped string
		for _, a := range ctx.EventManager().Events()[0].Attributes {
			if a.Key == AttributeKeyClamped {
				clamped = a.Value
			}
		}
		if clamped != "true" {
			t.Fatalf("clamped attribute = %q, want true", clamped)
		}
	})

	t.Run("nothing to burn: no call, no event", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			fee  math.LegacyDec
			gas  uint64
			bal  math.Int
		}{
			{"NoBaseFee", math.LegacyDec{}, 21_000, math.NewInt(1)},
			{"empty block", gwei, 0, math.NewInt(1)},
			{"empty collector", gwei, 21_000, math.ZeroInt()},
		} {
			bank := &fakeBank{balance: tc.bal}
			ctx := burnCtx(tc.gas)
			if err := burnBaseFee(ctx, bank, fakeFeemarket{tc.fee}); err != nil {
				t.Fatal(err)
			}
			if bank.burned != nil || len(ctx.EventManager().Events()) != 0 {
				t.Fatalf("%s: unexpected burn %v / events %v", tc.name, bank.burned, ctx.EventManager().Events())
			}
		}
	})
}
