package app

import (
	evmtypes "github.com/cosmos/evm/x/vm/types"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

// checkBlockedRecipient rejects an EVM tx that sends value straight to an
// address x/vm will refuse at commit: a module account, or anything on the
// bank's blocked list (module accounts and precompiles — config.BlockedAddresses).
//
// x/vm's guard (SetBalanceWithLocked, ENGINEERING.md §4.1.1) fires when the
// stateDB is committed, i.e. after the tx is already in a block. That
// rejection is an SDK-level failure (code 4), and cosmos/evm does not index
// those as Ethereum txs: the sender is charged gas, eth_getTransactionReceipt
// answers "not found", and the reason is only in CometBFT's tx_search. A
// simulation never commits, so eth_estimateGas does not warn either. Doing
// the same check here, in the ante handler and the mempool pre-check, turns
// that into a synchronous, explained refusal from eth_sendRawTransaction with
// nothing charged — the guard itself is untouched and still the last line.
//
// What this cannot see: value sent to a blocked address by an internal call
// (contract → module account). That still fails at commit, invisibly to
// eth_*; a contract that forwards user-supplied addresses should check them.
func (app *KonstellationApp) checkBlockedRecipient(ctx sdk.Context, tx sdk.Tx) error {
	for _, msg := range tx.GetMsgs() {
		ethMsg, ok := msg.(*evmtypes.MsgEthereumTx)
		if !ok {
			continue
		}
		ethTx := ethMsg.AsTransaction()
		if ethTx == nil || ethTx.To() == nil || ethTx.Value().Sign() == 0 {
			continue
		}
		to := sdk.AccAddress(ethTx.To().Bytes())
		if acct, isModule := app.AccountKeeper.GetAccount(ctx, to).(sdk.ModuleAccountI); isModule {
			return errorsmod.Wrapf(sdkerrors.ErrUnauthorized, "%s (%s) is not allowed to receive funds: it is the %q module account", to, ethTx.To().Hex(), acct.GetName())
		}
		if app.BankKeeper.BlockedAddr(to) {
			return errorsmod.Wrapf(sdkerrors.ErrUnauthorized, "%s (%s) is not allowed to receive funds: blocked address (module account or precompile)", to, ethTx.To().Hex())
		}
	}
	return nil
}

// withBlockedRecipientCheck runs inner and then checkBlockedRecipient on
// the resulting context.
func (app *KonstellationApp) withBlockedRecipientCheck(inner sdk.AnteHandler) sdk.AnteHandler {
	return func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		newCtx, err := inner(ctx, tx, simulate)
		if err != nil {
			return newCtx, err
		}
		return newCtx, app.checkBlockedRecipient(newCtx, tx)
	}
}
