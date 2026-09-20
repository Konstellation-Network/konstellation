package app

import (
	errorsmod "cosmossdk.io/errors"

	circuitante "github.com/cosmos/cosmos-sdk/contrib/x/circuit/ante"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/cosmos/cosmos-sdk/x/authz"
)

// maxCircuitAuthzDepth bounds MsgExec nesting in the circuit check so a
// hostile tx cannot make it recurse without limit. authz itself rejects
// deeper nesting at execution.
const maxCircuitAuthzDepth = 4

// checkCircuit refuses a tx carrying a message type the circuit breaker
// has disabled — including messages nested in authz MsgExec, which the
// SDK's own decorator does not look into (its doc comment says an app with
// authz must). The router re-checks at execution, but by then the tx has
// taken block space and paid its fee; refusing here keeps a tripped type
// out of blocks altogether.
func (app *KonstellationApp) checkCircuit(ctx sdk.Context, tx sdk.Tx) error {
	return checkCircuitMsgs(ctx, &app.CircuitKeeper, tx.GetMsgs(), 0)
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
