package e2e

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	transfertypes "github.com/cosmos/ibc-go/v10/modules/apps/transfer/types"
	"github.com/cosmos/interchaintest/v10"
	"github.com/cosmos/interchaintest/v10/chain/cosmos"
	"github.com/cosmos/interchaintest/v10/ibc"
	"github.com/cosmos/interchaintest/v10/testreporter"
	"github.com/cosmos/interchaintest/v10/testutil"

	sdkmath "cosmossdk.io/math"
)

// TestIBCSafetyRails runs two konstellation chains joined by a Hermes relayer
// and exercises Phase 3 (ENGINEERING.md §13, §18) over a real channel:
//
//   - x/ratelimit: governance adds a 1 %/hour limit on esp over channel-0 of
//     chain A; a transfer over it is refused at submission, one at the limit
//     goes through and lands on B, the next one is refused.
//   - x/compliance/ibc: an address frozen on A cannot be funded from B — the
//     packet is error-acked and B refunds its sender; a clean address can.
//     And a sender frozen on B after escrowing is still refunded when the
//     error ack arrives (the refund is a protocol flow), while it can no
//     longer escrow anything new.
//
// The in-process and mock tests prove the logic; this proves the wiring:
// that the limiter really is on x/transfer's send path and outermost on the
// receive path, on a node built from the Dockerfile, with a real relayer.
func TestIBCSafetyRails(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e needs Docker; skipped in -short")
	}
	ctx := context.Background()
	// Governance on the testnet profile takes 2 h; shrink it so the
	// proposal that adds the limit passes inside the test.
	fastGov := []cosmos.GenesisKV{
		cosmos.NewGenesisKV("app_state.gov.params.voting_period", "30s"),
		cosmos.NewGenesisKV("app_state.gov.params.expedited_voting_period", "20s"),
	}
	specA := newChainSpec(ctx, "kons-a", chainID, fastGov...)
	specB := newChainSpec(ctx, "kons-b", "konstellation-local-2")
	cf := interchaintest.NewBuiltinChainFactory(zaptest.NewLogger(t), []*interchaintest.ChainSpec{specA.spec, specB.spec})
	chains, err := cf.Chains(t.Name())
	require.NoError(t, err)
	chainA, chainB := chains[0].(*cosmos.CosmosChain), chains[1].(*cosmos.CosmosChain)

	client, network := interchaintest.DockerSetup(t)
	r := interchaintest.NewBuiltinRelayerFactory(ibc.Hermes, zaptest.NewLogger(t)).Build(t, client, network)
	const path = "kons-a-kons-b"
	ic := interchaintest.NewInterchain().
		AddChain(chainA).AddChain(chainB).
		AddRelayer(r, "hermes").
		AddLink(interchaintest.InterchainLink{Chain1: chainA, Chain2: chainB, Relayer: r, Path: path})
	eRep := testreporter.NewNopReporter().RelayerExecReporter(t)
	require.NoError(t, ic.Build(ctx, eRep, interchaintest.InterchainBuildOptions{
		TestName: t.Name(), Client: client, NetworkID: network, SkipPathCreation: false,
	}))
	t.Cleanup(func() { _ = ic.Close() })
	a := wrapChain(ctx, t, chainA, *specA.authority)
	b := wrapChain(ctx, t, chainB, *specB.authority)

	chanA, err := ibc.GetTransferChannel(ctx, r, eRep, chainA.Config().ChainID, chainB.Config().ChainID)
	require.NoError(t, err)
	chanB, err := ibc.GetTransferChannel(ctx, r, eRep, chainB.Config().ChainID, chainA.Config().ChainID)
	require.NoError(t, err)
	t.Logf("channels: A %s ↔ B %s", chanA.ChannelID, chanB.ChannelID)
	flush := func() {
		t.Helper()
		require.NoError(t, r.Flush(ctx, eRep, path, chanA.ChannelID))
		require.NoError(t, r.Flush(ctx, eRep, path, chanB.ChannelID))
		require.NoError(t, testutil.WaitForBlocks(ctx, 2, chainA, chainB))
	}

	// esp on B, as arrived from A; esp on A, as arrived from B.
	espOnB := transfertypes.NewDenom(denom, transfertypes.NewHop("transfer", chanB.ChannelID)).IBCDenom()
	espOnA := transfertypes.NewDenom(denom, transfertypes.NewHop("transfer", chanA.ChannelID)).IBCDenom()

	users := interchaintest.GetAndFundTestUsers(t, ctx, "u", sdkmath.NewInt(3_000_000).Mul(sdkmath.NewIntFromBigInt(oneKASH)), chainA, chainB)
	userA, userB := users[0], users[1]

	// ── baseline: a transfer A → B lands ──────────────────────────────────
	// Through the CLI: interchaintest's SendIBCTransfer decodes the tx with
	// its own codec, which does not know cosmos/evm's eth_secp256k1 pubkey.
	send := func(from *cosmos.CosmosChain, channel string, user ibc.Wallet, to string, amount sdkmath.Int) error {
		_, err := from.GetNode().ExecTx(ctx, user.KeyName(),
			"ibc-transfer", "transfer", "transfer", channel, to, amount.String()+denom, "--gas", "auto")
		return err
	}
	require.NoError(t, send(chainA, chanA.ChannelID, userA, userB.FormattedAddress(), sdkmath.NewIntFromBigInt(oneKASH)))
	flush()
	bal, err := chainB.GetBalance(ctx, userB.FormattedAddress(), espOnB)
	require.NoError(t, err)
	require.True(t, bal.Equal(sdkmath.NewIntFromBigInt(oneKASH)), "baseline transfer did not land on B: %s", bal)

	// ── governance adds the limit: 1 % of supply per hour, each way ───────
	govAddr := bech32Of(moduleAddress("gov"))
	msg, err := json.Marshal(map[string]any{
		"@type":            "/konstellation.ratelimit.v1.MsgAddRateLimit",
		"authority":        govAddr,
		"denom":            denom,
		"channel_id":       chanA.ChannelID,
		"max_percent_send": "1",
		"max_percent_recv": "1",
		"duration_hours":   "1",
	})
	require.NoError(t, err)
	propID := submitProposal(ctx, t, chainA, userA.KeyName(), msg, "rate-limit esp on "+chanA.ChannelID)
	require.NoError(t, chainA.VoteOnProposalAllValidators(ctx, propID, cosmos.ProposalVoteYes))
	waitForProposal(ctx, t, chainA, propID, "PROPOSAL_STATUS_PASSED")

	// Read the limit back: the threshold is 1 % of the supply snapshot.
	out, _, err := chainA.GetNode().ExecQuery(ctx, "ratelimit", "show", chanA.ChannelID, denom)
	require.NoError(t, err)
	var shown struct {
		RateLimit struct {
			Flow struct {
				ChannelValue string `json:"channel_value"`
			} `json:"flow"`
		} `json:"rate_limit"`
	}
	require.NoError(t, json.Unmarshal(out, &shown), string(out))
	channelValue, ok := sdkmath.NewIntFromString(shown.RateLimit.Flow.ChannelValue)
	require.True(t, ok, string(out))
	threshold := channelValue.QuoRaw(100)
	t.Logf("supply snapshot %s, 1%% threshold %s", channelValue, threshold)

	// ── over the limit: refused at submission ─────────────────────────────
	err = send(chainA, chanA.ChannelID, userA, userB.FormattedAddress(), threshold.AddRaw(1))
	require.Error(t, err, "transfer over the rate limit was accepted")
	require.Contains(t, err.Error(), "rate limit")

	// ── at the limit: goes through and lands ──────────────────────────────
	require.NoError(t, send(chainA, chanA.ChannelID, userA, userB.FormattedAddress(), threshold))
	flush()
	bal, err = chainB.GetBalance(ctx, userB.FormattedAddress(), espOnB)
	require.NoError(t, err)
	require.True(t, bal.Equal(threshold.Add(sdkmath.NewIntFromBigInt(oneKASH))), "limit-sized transfer did not land: %s", bal)

	// ── the window is used up: even 1 esp is refused ──────────────────────
	err = send(chainA, chanA.ChannelID, userA, userB.FormattedAddress(), sdkmath.OneInt())
	require.Error(t, err)
	require.Contains(t, err.Error(), "rate limit")

	// ── frozen receiver on A cannot be funded from B ──────────────────────
	frozenA := interchaintest.GetAndFundTestUsers(t, ctx, "frozen", sdkmath.NewIntFromBigInt(oneKASH), chainA)[0]
	cleanA := interchaintest.GetAndFundTestUsers(t, ctx, "clean", sdkmath.NewIntFromBigInt(oneKASH), chainA)[0]
	a.emergencyFreeze(frozenA.FormattedAddress())
	require.True(t, a.isFrozen(frozenA.FormattedAddress()))

	bBefore, err := chainB.GetBalance(ctx, userB.FormattedAddress(), denom)
	require.NoError(t, err)
	require.NoError(t, send(chainB, chanB.ChannelID, userB, frozenA.FormattedAddress(), sdkmath.NewIntFromBigInt(oneKASH)))
	require.NoError(t, send(chainB, chanB.ChannelID, userB, cleanA.FormattedAddress(), sdkmath.NewIntFromBigInt(oneKASH)))
	flush()

	got, err := chainA.GetBalance(ctx, frozenA.FormattedAddress(), espOnA)
	require.NoError(t, err)
	require.True(t, got.IsZero(), "frozen address received %s over IBC", got)
	got, err = chainA.GetBalance(ctx, cleanA.FormattedAddress(), espOnA)
	require.NoError(t, err)
	require.True(t, got.Equal(sdkmath.NewIntFromBigInt(oneKASH)), "clean address did not receive: %s", got)
	// B refunded the frozen one: userB is down one transfer plus fees, not two.
	bAfter, err := chainB.GetBalance(ctx, userB.FormattedAddress(), denom)
	require.NoError(t, err)
	spent := bBefore.Sub(bAfter)
	require.True(t, spent.LT(sdkmath.NewIntFromBigInt(oneKASH).MulRaw(2)), "no refund for the error-acked packet: spent %s", spent)
	require.True(t, spent.GT(sdkmath.NewIntFromBigInt(oneKASH)), "clean transfer not debited: spent %s", spent)

	// ── a refund to a sender frozen after escrow still lands ──────────────
	// userB sends to frozenA again (error-acked on A) and is frozen on B
	// before the acknowledgement is relayed. The refund is the transfer
	// module's own completion (x/compliance/ibc.RefundMarker): it reaches
	// the frozen account, where it stays immobile, instead of the packet
	// failing on every relayer retry. Relaying is manual here (flush), so
	// the ordering is exact.
	bBefore, err = chainB.GetBalance(ctx, userB.FormattedAddress(), denom)
	require.NoError(t, err)
	require.NoError(t, send(chainB, chanB.ChannelID, userB, frozenA.FormattedAddress(), sdkmath.NewIntFromBigInt(oneKASH)))
	b.emergencyFreeze(userB.FormattedAddress())
	require.True(t, b.isFrozen(userB.FormattedAddress()))
	flush()
	bAfter, err = chainB.GetBalance(ctx, userB.FormattedAddress(), denom)
	require.NoError(t, err)
	spent = bBefore.Sub(bAfter)
	require.True(t, spent.LT(sdkmath.NewIntFromBigInt(oneKASH)), "escrowed KASH not refunded to the since-frozen sender: spent %s", spent)
	// Frozen, userB cannot escrow anything new.
	err = send(chainB, chanB.ChannelID, userB, cleanA.FormattedAddress(), sdkmath.NewIntFromBigInt(oneKASH))
	require.Error(t, err)
	require.Contains(t, err.Error(), "address is frozen")
}

// submitProposal submits a gov v1 proposal carrying one message through the
// CLI and returns its id. (interchaintest's SubmitProposal decodes the tx
// in Go and cannot resolve our module's message types.)
func submitProposal(ctx context.Context, t *testing.T, chain *cosmos.CosmosChain, keyName string, msg json.RawMessage, title string) uint64 {
	t.Helper()
	node := chain.GetNode()
	prop, err := json.Marshal(map[string]any{
		"messages": []json.RawMessage{msg},
		"deposit":  "10000000000000000000" + denom, // 10 KASH, the testnet minimum
		"title":    title,
		"summary":  "e2e",
		"metadata": "",
	})
	require.NoError(t, err)
	require.NoError(t, node.WriteFile(ctx, prop, "proposal.json"))
	_, err = node.ExecTx(ctx, keyName, "gov", "submit-proposal", node.HomeDir()+"/proposal.json", "--gas", "auto")
	require.NoError(t, err)

	out, _, err := node.ExecQuery(ctx, "gov", "proposals")
	require.NoError(t, err)
	var list struct {
		Proposals []struct {
			ID string `json:"id"`
		} `json:"proposals"`
	}
	require.NoError(t, json.Unmarshal(out, &list), string(out))
	require.NotEmpty(t, list.Proposals)
	id, err := strconv.ParseUint(list.Proposals[len(list.Proposals)-1].ID, 10, 64)
	require.NoError(t, err)
	return id
}

// waitForProposal polls the proposal's status through the CLI.
func waitForProposal(ctx context.Context, t *testing.T, chain *cosmos.CosmosChain, id uint64, want string) {
	t.Helper()
	var last string
	require.Eventually(t, func() bool {
		out, _, err := chain.GetNode().ExecQuery(ctx, "gov", "proposal", strconv.FormatUint(id, 10))
		if err != nil {
			return false
		}
		var res struct {
			Proposal struct {
				Status string `json:"status"`
			} `json:"proposal"`
		}
		if err := json.Unmarshal(out, &res); err != nil {
			return false
		}
		last = res.Proposal.Status
		return last == want
	}, 2*time.Minute, 2*time.Second, "proposal %d: last status %q, want %s", id, last, want)
}
