package app

import (
	"context"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"

	evmmempool "github.com/cosmos/evm/mempool"
	"github.com/cosmos/evm/server"
	evmtypes "github.com/cosmos/evm/x/vm/types"

	"cosmossdk.io/log/v2"

	"github.com/cosmos/cosmos-sdk/baseapp"
	servertypes "github.com/cosmos/cosmos-sdk/server/types"
	sdk "github.com/cosmos/cosmos-sdk/types"

	complianceante "github.com/Konstellation-Network/konstellation/x/compliance/ante"
)

// configureEVMMempool sets up the EVM mempool and related handlers using viper configuration.
func (app *KonstellationApp) configureEVMMempool(appOpts servertypes.AppOptions, logger log.Logger) error {
	if evmtypes.GetChainConfig() == nil {
		logger.Debug("evm chain config is not set, skipping mempool configuration")
		return nil
	}

	var (
		mpConfig = server.ResolveMempoolConfig(app.GetAnteHandler(), appOpts, logger)

		txEncoder       = evmmempool.NewTxEncoder(app.txConfig)
		evmRechecker    = evmmempool.NewTxRechecker(mpConfig.AnteHandler, txEncoder)
		cosmosRechecker = evmmempool.NewTxRechecker(mpConfig.AnteHandler, txEncoder)
		cosmosPoolMaxTx = server.GetCosmosPoolMaxTx(appOpts, logger)
		checkTxTimeout  = server.GetMempoolCheckTxTimeout(appOpts, logger)
	)

	if cosmosPoolMaxTx < 0 {
		logger.Debug("evm mempool is disabled, skipping configuration")
		return nil
	}

	if err := server.ValidateReapBounds(appOpts, mpConfig.BlockGasLimit); err != nil {
		return err
	}

	// create mempool
	mempool := evmmempool.NewMempool(
		app.CreateQueryContext,
		logger,
		app.EVMKeeper,
		app.FeeMarketKeeper,
		app.txConfig,
		evmRechecker,
		cosmosRechecker,
		mpConfig,
		cosmosPoolMaxTx,
	)

	// The JSON-RPC backend calls Mempool.Insert directly (no ABCI), so the
	// D6 pre-check has to sit on the mempool itself as well as on the ABCI
	// handlers below.
	app.EVMMempool = &complianceMempool{Mempool: mempool, app: app}

	// create ABCI handlers
	proposalHandler := baseapp.NewDefaultProposalHandler(mempool, NewNoCheckProposalTxVerifier(app.BaseApp))

	insertTxHandler := app.withCompliancePreCheckInsert(mempool.NewInsertTxHandler(app.TxDecode))
	reapTxsHandler := mempool.NewReapTxsHandler()
	checkTxHandler := app.withCompliancePreCheckCheckTx(mempool.NewCheckTxHandler(app.TxDecode, checkTxTimeout))

	// set handlers and the mempool
	app.SetPrepareProposal(proposalHandler.PrepareProposalHandler())
	app.SetProcessProposal(proposalHandler.ProcessProposalHandler())
	app.SetInsertTxHandler(insertTxHandler)
	app.SetReapTxsHandler(reapTxsHandler)
	app.SetCheckTxHandler(checkTxHandler)

	app.SetMempool(mempool)

	app.SetPrepareCheckStater(func(_ sdk.Context) {
		if !mempool.HasEventBus() {
			mempool.NotifyNewBlock()
		}
	})

	return nil
}

// D6 synchronous pre-check.
//
// The authoritative freeze check is in the ante handler (x/compliance/ante).
// The EVM mempool only runs the ante in its asynchronous recheck after
// insertion, so without a pre-check a frozen party's EVM tx is accepted by
// eth_sendRawTransaction and then silently dropped: never mined, never
// explained. The pre-check runs the same address extraction against the
// latest committed state at each entry point and turns that into an
// immediate "address is frozen" error. If committed state is not readable
// yet (first block not committed) it defers to the ante handler.
//
// The EVM sender used here is the decoder-populated From, not yet
// signature-verified. A forged From can at worst make an invalid tx fail
// with the wrong error; it cannot let a frozen signer through, because the
// ante handler re-checks the verified sender.
func (app *KonstellationApp) compliancePreCheck(tx sdk.Tx) error {
	// The latest-context lookup fails in the short window after Commit
	// before the check state is re-pointed (the same race STATUS.md records
	// for the EVM rechecker). A few short retries close it; only if state
	// is still unreadable do we defer to the ante handler, which enforces
	// at recheck and in the block regardless.
	var (
		ctx sdk.Context
		err error
	)
	for attempt := 0; attempt < compliancePreCheckAttempts; attempt++ {
		if ctx, err = app.CreateQueryContext(0, false); err == nil {
			break
		}
		time.Sleep(compliancePreCheckBackoff)
	}
	if err != nil {
		app.Logger().Warn("compliance pre-check skipped: state not readable, ante handler will enforce", "err", err)
		return nil
	}
	return complianceante.Check(ctx, app.appCodec, app.ComplianceKeeper, tx)
}

const (
	compliancePreCheckAttempts = 5
	compliancePreCheckBackoff  = 20 * time.Millisecond
)

// complianceMempool wraps the EVM mempool so the JSON-RPC path
// (rpc/backend SendRawTransaction → Mempool.Insert) is pre-checked. Every
// other method, including GetTxPool, SetEventBus and TrackTx, is the
// embedded mempool's.
type complianceMempool struct {
	*evmmempool.Mempool
	app *KonstellationApp
}

func (m *complianceMempool) Insert(ctx context.Context, tx sdk.Tx) error {
	if err := m.app.compliancePreCheck(tx); err != nil {
		return err
	}
	return m.Mempool.Insert(ctx, tx)
}

// withCompliancePreCheckInsert covers CometBFT's ABCI InsertTx (broadcast
// through the CometBFT RPC with the app-side mempool). ResponseInsertTx has
// only a code field, so unlike CheckTx below the rejection reason cannot be
// returned to the submitter.
func (app *KonstellationApp) withCompliancePreCheckInsert(inner sdk.InsertTxHandler) sdk.InsertTxHandler {
	return func(req *abci.RequestInsertTx) (*abci.ResponseInsertTx, error) {
		if tx, err := app.TxDecode(req.GetTx()); err == nil {
			if err := app.compliancePreCheck(tx); err != nil {
				return &abci.ResponseInsertTx{Code: evmmempool.CodeTypeNoRetry}, nil
			}
		}
		return inner(req)
	}
}

// withCompliancePreCheckCheckTx covers ABCI CheckTx, which carries the error
// text back to the submitter.
func (app *KonstellationApp) withCompliancePreCheckCheckTx(inner sdk.CheckTxHandler) sdk.CheckTxHandler {
	return func(runTx sdk.RunTx, req *abci.RequestCheckTx) (*abci.ResponseCheckTx, error) {
		if tx, err := app.TxDecode(req.GetTx()); err == nil {
			if err := app.compliancePreCheck(tx); err != nil {
				return evmmempool.ErrAsCheckTxResponse(err), nil
			}
		}
		return inner(runTx, req)
	}
}
