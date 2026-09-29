package types

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// protocolFlowKey marks a context in which the chain itself is completing
// something it already owes — not a party asking to move funds.
type protocolFlowKey struct{}

// WithProtocolFlow marks ctx as a protocol-completion flow. The bank send
// restriction (keeper.SendRestriction) lets such a flow deposit into a
// frozen address, because refusing would not stop the frozen party from
// moving anything — the funds land on an account that still cannot spend —
// but would leave a module's books inconsistent or a packet stuck.
//
// Set it only around code that is not user-controlled and that cannot be
// re-entered by user code: the transfer module's own refund on an error
// acknowledgement or timeout (x/compliance/ibc.RefundMarker), and the
// distribution hook that pays out commission when a validator is removed
// (keeper.MarkValidatorRemoval). Never around a contract callback or a
// message handler — anything that could call a precompile under the mark
// would inherit the exemption.
func WithProtocolFlow(ctx sdk.Context) sdk.Context {
	return ctx.WithValue(protocolFlowKey{}, true)
}

// IsProtocolFlow reports whether ctx carries the WithProtocolFlow mark.
func IsProtocolFlow(ctx context.Context) bool {
	v, _ := ctx.Value(protocolFlowKey{}).(bool)
	return v
}
