//go:build test

package integration

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	abcitypes "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"

	basefactory "github.com/cosmos/evm/testutil/integration/base/factory"
	testkeyring "github.com/cosmos/evm/testutil/keyring"

	sdkmath "cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	govv1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/Konstellation-Network/konstellation/app/config"
	compliancetypes "github.com/Konstellation-Network/konstellation/x/compliance/types"
)

// The PR #15 review (2026-09-22) found that the first bank-level binding
// halted the chain: slashing a validator that a frozen delegator had
// redelegated from reaches x/distribution's reward withdrawal to the frozen
// address (SlashRedelegation → Unbond → BeforeDelegationSharesModified),
// the send restriction refused it, BeginBlock returned the error and
// FinalizeBlock failed on every validator — with no tx left to lift the
// freeze. Evidence (double sign) and downtime both get there, and the
// downtime path is forceable by a frozen contract redelegating every block.
//
// The fix marks the whole BeginBlock/EndBlock phase as a protocol flow
// (app.BeginBlocker/EndBlocker): a frozen address may be credited there,
// never debited. These tests are the reviewer's demonstrations, inverted.

// blocks drives FinalizeBlock/Commit by hand so a test controls each
// block's misbehaviour evidence and vote flags. After the first call the
// harness's own block helpers must not be used (their header is stale);
// read state through state().
type blocks struct {
	h      *harness
	header cmtproto.Header
}

func (h *harness) blocks() *blocks { return &blocks{h: h, header: h.ctx().BlockHeader()} }

// next finalizes and commits one block. absent lists validators whose vote
// on the previous block is missing.
func (b *blocks) next(mis []abcitypes.Misbehavior, absent map[string]bool, txs ...[]byte) error {
	b.h.t.Helper()
	b.header.Height++
	b.header.Time = b.header.Time.Add(time.Second)
	b.header.AppHash = b.h.app.LastCommitID().Hash
	var votes []abcitypes.VoteInfo
	for _, v := range b.h.nw.GetValidators() {
		ca, err := v.GetConsAddr()
		require.NoError(b.h.t, err)
		flag := cmtproto.BlockIDFlagCommit
		if absent[string(ca)] {
			flag = cmtproto.BlockIDFlagAbsent
		}
		votes = append(votes, abcitypes.VoteInfo{Validator: abcitypes.Validator{Address: ca, Power: v.ConsensusPower(sdk.DefaultPowerReduction)}, BlockIdFlag: flag})
	}
	_, err := b.h.app.FinalizeBlock(&abcitypes.RequestFinalizeBlock{
		Height: b.header.Height, Time: b.header.Time, Hash: b.header.AppHash,
		NextValidatorsHash: b.header.ValidatorsHash, ProposerAddress: b.header.ProposerAddress,
		DecidedLastCommit: abcitypes.CommitInfo{Votes: votes}, Misbehavior: mis, Txs: txs,
	})
	if err != nil {
		return err
	}
	_, err = b.h.app.Commit()
	return err
}

// state reads the last committed state.
func (b *blocks) state() sdk.Context { return b.h.app.NewUncachedContext(false, b.header) }

func (h *harness) cosmosTxBytes(key testkeyring.Key, msgs ...sdk.Msg) []byte {
	h.t.Helper()
	gas := uint64(500_000)
	signed, err := h.factory.BuildCosmosTx(key.Priv, basefactory.CosmosTxArgs{Msgs: msgs, Gas: &gas, GasPrice: ptr(sdkmath.NewInt(1_000_000_000))})
	require.NoError(h.t, err)
	bz, err := h.factory.EncodeTx(signed)
	require.NoError(h.t, err)
	return bz
}

func kashCoin(n int64) sdk.Coin {
	return sdk.NewCoin(config.BaseDenom, sdkmath.NewIntFromBigInt(new(big.Int).Mul(oneKASH, big.NewInt(n))))
}

// TestSlashOfFrozenRedelegatorKeepsChainAlive: alice delegates to src,
// redelegates to dst and is frozen; src is then slashed — by double-sign
// evidence, and by downtime — while her redelegation is young enough to be
// slashed with it. The block finalizes, src is jailed, her withdrawn rewards
// land on the frozen account, and the chain keeps going.
func TestSlashOfFrozenRedelegatorKeepsChainAlive(t *testing.T) {
	t.Run("evidence", func(t *testing.T) {
		h := newHarnessN(t, 2)
		alice := h.key(1)
		src, dst := h.nw.GetValidators()[0], h.nw.GetValidators()[1]
		res, err := h.sendCosmos(alice, stakingtypes.NewMsgDelegate(alice.AccAddr.String(), src.OperatorAddress, kashCoin(5)))
		require.NoError(t, err)
		require.Zero(t, res.Code, res.Log)
		res, err = h.sendCosmos(alice, stakingtypes.NewMsgBeginRedelegate(alice.AccAddr.String(), src.OperatorAddress, dst.OperatorAddress, kashCoin(5)))
		require.NoError(t, err)
		require.Zero(t, res.Code, res.Log)
		redelHeight := h.ctx().BlockHeight()
		h.emergencyFreeze(alice.Addr)
		require.True(t, h.isFrozen(alice.Addr))
		for i := 0; i < 3; i++ {
			h.nextBlock() // rewards accrue on dst for alice
		}
		before := h.balance(alice.Addr)

		srcCons, err := src.GetConsAddr()
		require.NoError(t, err)
		b := h.blocks()
		require.NoError(t, b.next([]abcitypes.Misbehavior{{
			Type:             abcitypes.MisbehaviorType_DUPLICATE_VOTE,
			Validator:        abcitypes.Validator{Address: srcCons, Power: src.ConsensusPower(sdk.DefaultPowerReduction)},
			Height:           redelHeight - 1,
			Time:             b.header.Time.Add(-10 * time.Second),
			TotalVotingPower: 2 * src.ConsensusPower(sdk.DefaultPowerReduction),
		}}, nil), "BeginBlock with double-sign evidence against src: chain halt")
		st := b.state()
		val, err := h.app.StakingKeeper.GetValidator(st, mustValAddr(t, src.OperatorAddress))
		require.NoError(t, err)
		require.True(t, val.Jailed, "src not slashed; the test did not reach the hook")
		require.True(t, h.app.ComplianceKeeper.IsFrozen(st, alice.Addr.Bytes()))
		require.Equal(t, 1, h.app.BankKeeper.GetBalance(st, alice.AccAddr, config.BaseDenom).Amount.BigInt().Cmp(before), "rewards were not withdrawn to the frozen delegator by the slash")
		for i := 0; i < 3; i++ {
			require.NoError(t, b.next(nil, nil), "chain stopped after the slash")
		}
	})

	t.Run("downtime", func(t *testing.T) {
		h := newHarnessN(t, 2)
		alice := h.key(1)
		src, dst := h.nw.GetValidators()[0], h.nw.GetValidators()[1]
		res, err := h.sendCosmos(alice, stakingtypes.NewMsgDelegate(alice.AccAddr.String(), src.OperatorAddress, kashCoin(5)))
		require.NoError(t, err)
		require.Zero(t, res.Code, res.Log)
		srcCons, err := src.GetConsAddr()
		require.NoError(t, err)
		params, err := h.app.SlashingKeeper.GetParams(h.ctx())
		require.NoError(t, err)
		minSigned, err := h.app.SlashingKeeper.MinSignedPerWindow(h.ctx())
		require.NoError(t, err)
		maxMissed := params.SignedBlocksWindow - minSigned

		// Downtime only counts once the window has passed; then src misses
		// blocks until one more would jail it.
		b := h.blocks()
		for b.header.Height <= params.SignedBlocksWindow {
			require.NoError(t, b.next(nil, nil))
		}
		absent := map[string]bool{string(srcCons): true}
		for {
			info, err := h.app.SlashingKeeper.GetValidatorSigningInfo(b.state(), srcCons)
			require.NoError(t, err)
			if info.MissedBlocksCounter >= maxMissed-1 {
				break
			}
			require.NoError(t, b.next(nil, absent))
		}
		// The block before the jailing one: alice redelegates and is frozen,
		// so the redelegation is younger than the infraction and is slashed.
		require.NoError(t, b.next(nil, absent,
			h.cosmosTxBytes(alice, stakingtypes.NewMsgBeginRedelegate(alice.AccAddr.String(), src.OperatorAddress, dst.OperatorAddress, kashCoin(5))),
			h.cosmosTxBytes(h.authority, &compliancetypes.MsgEmergencyFreeze{Authority: h.authority.AccAddr.String(), Addresses: []string{alice.Addr.Hex()}, Reason: "downtime"}),
		))
		st := b.state()
		require.True(t, h.app.ComplianceKeeper.IsFrozen(st, alice.Addr.Bytes()), "freeze tx did not land")
		redel, err := h.app.StakingKeeper.GetRedelegation(st, alice.AccAddr, mustValAddr(t, src.OperatorAddress), mustValAddr(t, dst.OperatorAddress))
		require.NoError(t, err, "redelegation tx did not land")
		require.Equal(t, b.header.Height, redel.Entries[0].CreationHeight)
		before := h.app.BankKeeper.GetBalance(st, alice.AccAddr, config.BaseDenom)

		require.NoError(t, b.next(nil, absent), "BeginBlock jailing src for downtime: chain halt")
		st = b.state()
		val, err := h.app.StakingKeeper.GetValidator(st, mustValAddr(t, src.OperatorAddress))
		require.NoError(t, err)
		require.True(t, val.Jailed, "src not jailed; the test did not reach the hook")
		require.Equal(t, 1, h.app.BankKeeper.GetBalance(st, alice.AccAddr, config.BaseDenom).Amount.BigInt().Cmp(before.Amount.BigInt()), "rewards were not withdrawn to the frozen delegator by the slash")
		for i := 0; i < 3; i++ {
			require.NoError(t, b.next(nil, nil), "chain stopped after the slash")
		}
	})
}

// TestABCIPhasesRunAsProtocolFlow pins the mechanism: every bank send made
// in BeginBlock or EndBlock carries the protocol mark, none made by a tx
// does — observed through a second send restriction appended behind
// compliance's.
func TestABCIPhasesRunAsProtocolFlow(t *testing.T) {
	h := newHarness(t)
	bob := h.key(2)
	type seen struct{ inTx, marked bool }
	var log []seen
	h.app.BankKeeper.AppendSendRestriction(func(ctx context.Context, _, to sdk.AccAddress, _ sdk.Coins) (sdk.AccAddress, error) {
		log = append(log, seen{inTx: len(sdk.UnwrapSDKContext(ctx).TxBytes()) > 0, marked: compliancetypes.IsProtocolFlow(ctx)})
		return to, nil
	})

	// A tx (deposit into x/gov) and, at the proposal's end, an EndBlock
	// refund; BeginBlock sends issuance to the fee collector every block.
	params, err := h.app.GovKeeper.Params.Get(h.ctx())
	require.NoError(t, err)
	prop, err := govv1.NewMsgSubmitProposal(nil, sdk.NewCoins(params.MinDeposit...), bob.AccAddr.String(), "m", "T", "abci mark", false)
	require.NoError(t, err)
	res, err := h.sendCosmos(bob, prop)
	require.NoError(t, err)
	require.Zero(t, res.Code, res.Log)
	proposal, err := h.app.GovKeeper.Proposals.Get(h.ctx(), 1)
	require.NoError(t, err)
	h.nextBlockAfter(proposal.VotingEndTime.Sub(h.ctx().BlockTime()) + time.Second)

	var txSends, phaseSends int
	for _, s := range log {
		if s.inTx {
			txSends++
			require.False(t, s.marked, "a tx-path send carried the protocol mark")
		} else {
			phaseSends++
			require.True(t, s.marked, "a BeginBlock/EndBlock send ran without the protocol mark")
		}
	}
	require.Positive(t, txSends)
	require.Positive(t, phaseSends)

	// The mark exempts recipients only: nothing in either phase can move
	// funds out of a frozen address.
	h.emergencyFreeze(bob.Addr)
	marked := compliancetypes.WithProtocolFlow(h.ctx())
	one := sdk.NewCoins(kashCoin(1))
	require.ErrorIs(t, h.app.BankKeeper.SendCoins(marked, bob.AccAddr, h.key(3).AccAddr, one), compliancetypes.ErrAddressFrozen)
	require.NoError(t, h.app.BankKeeper.SendCoins(marked, h.key(3).AccAddr, bob.AccAddr, one))
	val := h.nw.GetValidators()[0]
	_, err = h.app.StakingKeeper.Delegate(marked, bob.AccAddr, kashCoin(1).Amount, stakingtypes.Unbonded, val, true)
	require.ErrorIs(t, err, compliancetypes.ErrAddressFrozen, "a frozen delegator's stake moved under the protocol mark")
}

func mustValAddr(t *testing.T, s string) sdk.ValAddress {
	t.Helper()
	v, err := sdk.ValAddressFromBech32(s)
	require.NoError(t, err)
	return v
}
