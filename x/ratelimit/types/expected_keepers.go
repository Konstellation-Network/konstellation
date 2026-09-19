package types

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// BankKeeper is the slice of x/bank the module needs: the denom's supply
// is the denominator for every percentage quota.
type BankKeeper interface {
	GetSupply(ctx context.Context, denom string) sdk.Coin
}
