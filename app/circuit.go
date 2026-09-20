package app

import (
	"context"

	errorsmod "cosmossdk.io/errors"

	circuitante "github.com/cosmos/cosmos-sdk/contrib/x/circuit/ante"
	circuitkeeper "github.com/cosmos/cosmos-sdk/contrib/x/circuit/keeper"
	circuittypes "github.com/cosmos/cosmos-sdk/contrib/x/circuit/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/cosmos/cosmos-sdk/x/authz"
	govv1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
)

// maxCircuitAuthzDepth bounds MsgExec nesting in the circuit check so a
// hostile tx cannot make it recurse without limit. authz itself rejects
// deeper nesting at execution.
const maxCircuitAuthzDepth = 4

// circuitProtected are the message types the breaker may never disable:
// its own, so a trip can always be reset, and governance's, so the
// breaker's authority can always revoke a permission or reset a trip. The
// upstream module accepts any URL in MsgTripCircuitBreaker, including
// MsgResetCircuitBreaker; a fat-fingered or compromised admin could weld
// the escape hatch shut, and reopening it would be a hard fork (PR #12
// review, 2026-09-20). circuitBreaker below ignores these even if they are
// in the disable list (a genesis could put them there), and the ante refuses
// a trip that names them.
var circuitProtected = func() map[string]bool {
	out := map[string]bool{}
	for _, m := range []sdk.Msg{
		&circuittypes.MsgTripCircuitBreaker{}, &circuittypes.MsgResetCircuitBreaker{}, &circuittypes.MsgAuthorizeCircuitBreaker{},
		&govv1.MsgSubmitProposal{}, &govv1.MsgDeposit{}, &govv1.MsgVote{}, &govv1.MsgVoteWeighted{},
		&govv1.MsgExecLegacyContent{}, &govv1.MsgCancelProposal{},
	} {
		out[sdk.MsgTypeURL(m)] = true
	}
	return out
}()

// circuitBreaker is what the router, the ante, the mempool pre-check and
// the precompile guard consult: the keeper's disable list, minus the
// protected types.
type circuitBreaker struct{ k *circuitkeeper.Keeper }

func (b circuitBreaker) IsAllowed(ctx context.Context, typeURL string) (bool, error) {
	if circuitProtected[typeURL] {
		return true, nil
	}
	return b.k.IsAllowed(ctx, typeURL)
}

// checkCircuit refuses a tx carrying a message type the circuit breaker
// has disabled — including messages nested in authz MsgExec, which the
// SDK's own decorator does not look into (its doc comment says an app with
// authz must). The router re-checks at execution, but by then the tx has
// taken block space and paid its fee; refusing here keeps a tripped type
// out of blocks altogether. It also refuses a trip of a protected type.
func (app *KonstellationApp) checkCircuit(ctx sdk.Context, tx sdk.Tx) error {
	return checkCircuitMsgs(ctx, circuitBreaker{&app.CircuitKeeper}, tx.GetMsgs(), 0)
}

func checkCircuitMsgs(ctx sdk.Context, ck circuitante.CircuitBreaker, msgs []sdk.Msg, depth int) error {
	for _, msg := range msgs {
		allowed, err := ck.IsAllowed(ctx, sdk.MsgTypeURL(msg))
		if err != nil {
			return err
		}
		if !allowed {
			return errorsmod.Wrapf(sdkerrors.ErrUnauthorized, "circuit breaker disables %s", sdk.MsgTypeURL(msg))
		}
		if trip, ok := msg.(*circuittypes.MsgTripCircuitBreaker); ok {
			for _, url := range trip.MsgTypeUrls {
				if circuitProtected[url] {
					return errorsmod.Wrapf(sdkerrors.ErrUnauthorized, "%s cannot be disabled: the breaker must stay resettable", url)
				}
			}
		}
		if exec, ok := msg.(*authz.MsgExec); ok {
			if depth >= maxCircuitAuthzDepth {
				return errorsmod.Wrapf(sdkerrors.ErrUnauthorized, "authz MsgExec nested deeper than %d", maxCircuitAuthzDepth)
			}
			inner, err := exec.GetMessages()
			if err != nil {
				return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, err.Error())
			}
			if err := checkCircuitMsgs(ctx, ck, inner, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// circuitDecorator is the ante form of checkCircuit. It replaces
// circuitante.CircuitBreakerDecorator so the ante and the mempool pre-check
// share one walk, nested authz included.
type circuitDecorator struct{ app *KonstellationApp }

func (d circuitDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (sdk.Context, error) {
	if err := d.app.checkCircuit(ctx, tx); err != nil {
		return ctx, err
	}
	return next(ctx, tx, simulate)
}
