//go:build test

package integration

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	abcitypes "github.com/cometbft/cometbft/abci/types"

	"github.com/cosmos/evm/precompiles/distribution"
	"github.com/cosmos/evm/precompiles/gov"
	"github.com/cosmos/evm/precompiles/staking"
	evmtypes "github.com/cosmos/evm/x/vm/types"
	transfertypes "github.com/cosmos/ibc-go/v11/modules/apps/transfer/types"
	channeltypes "github.com/cosmos/ibc-go/v11/modules/core/04-channel/types"

	"cosmossdk.io/collections"

	sdk "github.com/cosmos/cosmos-sdk/types"
	govv1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/Konstellation-Network/konstellation/tests/integration/testdata"
	compliancekeeper "github.com/Konstellation-Network/konstellation/x/compliance/keeper"
	compliancetypes "github.com/Konstellation-Network/konstellation/x/compliance/types"
)

var (
	stakingAddr = common.HexToAddress(evmtypes.StakingPrecompileAddress)
	distrAddr   = common.HexToAddress(evmtypes.DistributionPrecompileAddress)
	govAddr     = common.HexToAddress(evmtypes.GovPrecompileAddress)
	stakingC    = evmtypes.CompiledContract{ABI: staking.ABI}
	distrC      = evmtypes.CompiledContract{ABI: distribution.ABI}
	govC        = evmtypes.CompiledContract{ABI: gov.ABI}
)

// TestFrozenContractCannotStakeVoteOrRouteRewards is the PR #15 review's
// second finding, inverted. A frozen contract runs whenever anyone calls
// it; the staking precompile delegates the caller's own KASH through
// x/bank's DelegateCoins, which the send restriction never sees, and the
// distribution and gov precompiles act for the caller too — so a frozen
// contract could delegate, point its yield at a clean address and vote.
// Now the tx-path precompiles refuse a frozen caller (app/compliance_precompiles.go)
// and x/staking's bank keeper refuses a frozen delegator regardless.
func TestFrozenContractCannotStakeVoteOrRouteRewards(t *testing.T) {
	h := newHarness(t)
	operator, sink := h.key(2), h.key(3)
	forwarderC := mustLoad(t, testdata.LoadForwarder)
	fc := h.deploy(operator, forwarderC)
	relay := h.deploy(operator, forwarderC)
	// operator → relay → fc → precompile: the ante sees only relay.
	via := func(target common.Address, data []byte) (abcitypes.ExecTxResult, error) {
		return h.call(operator, relay, forwarderC, "call", fc, pack(t, forwarderC, "call", target, data))
	}
	val := h.nw.GetValidators()[0]
	h.fundContract(operator, fc, kashCoin(5).Amount.BigInt())

	// Before the freeze the contract stakes and votes like anyone.
	res, err := via(stakingAddr, pack(t, stakingC, "delegate", fc, val.OperatorAddress, oneKASH))
	require.NoError(t, err, res.Log)
	params, err := h.app.GovKeeper.Params.Get(h.ctx())
	require.NoError(t, err)
	prop, err := govv1.NewMsgSubmitProposal(nil, sdk.NewCoins(params.MinDeposit...), sink.AccAddr.String(), "m", "T", "vote from a contract", false)
	require.NoError(t, err)
	pres, err := h.sendCosmos(sink, prop)
	require.NoError(t, err)
	require.Zero(t, pres.Code, pres.Log)
	res, err = via(govAddr, pack(t, govC, "vote", fc, uint64(1), uint8(govv1.OptionNo), ""))
	require.NoError(t, err, res.Log)

	h.emergencyFreeze(fc)
	require.True(t, h.isFrozen(fc))
	fcBefore, sinkBefore := h.balance(fc), h.balance(sink.Addr)
	for i := 0; i < 3; i++ {
		h.nextBlock() // rewards accrue on the pre-freeze delegation
	}

	// Every tx method reverts with the reason; the call is a mined,
	// visible failure (revert), not an SDK-level one.
	for name, call := range map[string]func() (abcitypes.ExecTxResult, error){
		"delegate": func() (abcitypes.ExecTxResult, error) {
			return via(stakingAddr, pack(t, stakingC, "delegate", fc, val.OperatorAddress, oneKASH))
		},
		"undelegate": func() (abcitypes.ExecTxResult, error) {
			return via(stakingAddr, pack(t, stakingC, "undelegate", fc, val.OperatorAddress, oneKASH))
		},
		"setWithdrawAddress": func() (abcitypes.ExecTxResult, error) {
			return via(distrAddr, pack(t, distrC, "setWithdrawAddress", fc, sink.AccAddr.String()))
		},
		"withdrawDelegatorRewards": func() (abcitypes.ExecTxResult, error) {
			return via(distrAddr, pack(t, distrC, "withdrawDelegatorRewards", fc, val.OperatorAddress))
		},
		"vote": func() (abcitypes.ExecTxResult, error) {
			return via(govAddr, pack(t, govC, "vote", fc, uint64(1), uint8(govv1.OptionYes), ""))
		},
	} {
		res, err := call()
		require.Error(t, err, "%s from a frozen contract succeeded", name)
		require.Zero(t, res.Code, "%s: expected an EVM revert, got an SDK failure: %s", name, res.Log)
		require.Contains(t, err.Error(), compliancetypes.ErrAddressFrozen.Error(), name)
	}
	require.Equal(t, 0, fcBefore.Cmp(h.balance(fc)), "frozen contract's KASH moved")
	require.Equal(t, 0, sinkBefore.Cmp(h.balance(sink.Addr)), "a frozen contract's rewards reached a clean address")
	vote, err := h.app.GovKeeper.Votes.Get(h.ctx(), collections.Join(uint64(1), sdk.AccAddress(fc.Bytes())))
	require.NoError(t, err)
	require.Equal(t, govv1.OptionNo, vote.Options[0].Option, "frozen contract's vote changed")

	// A clean caller naming the frozen address is refused too: relay
	// itself asks to pay its rewards to the frozen contract.
	res, err = h.call(operator, relay, forwarderC, "call", distrAddr, pack(t, distrC, "setWithdrawAddress", relay, sdk.AccAddress(fc.Bytes()).String()))
	require.Error(t, err)
	require.Zero(t, res.Code, res.Log)
	require.Contains(t, err.Error(), compliancetypes.ErrAddressFrozen.Error())

	// The bank-level backstop behind the precompile guard: x/staking's own
	// Delegate refuses a frozen delegator whoever asks.
	cctx, _ := h.ctx().CacheContext()
	_, err = h.app.StakingKeeper.Delegate(cctx, sdk.AccAddress(fc.Bytes()), kashCoin(1).Amount, stakingtypes.Unbonded, val, true)
	require.ErrorIs(t, err, compliancetypes.ErrAddressFrozen)

	// Lifted, the contract stakes again.
	h.liftEmergencyFreeze(fc)
	res, err = via(stakingAddr, pack(t, stakingC, "delegate", fc, val.OperatorAddress, oneKASH))
	require.NoError(t, err, res.Log)
}

// TestEscrowAccountsCannotBeFrozen: an ICS-20 escrow is the sender of
// every unescrow and refund on its channel, and senders are never exempt,
// so freezing one would wedge the channel until the entry lifted. The
// list authority cannot; the app wires the check over the real channel and
// client state.
func TestEscrowAccountsCannotBeFrozen(t *testing.T) {
	h := newHarness(t)
	require.True(t, h.app.ComplianceKeeper.HasEscrowChecker(), "app.New did not wire the escrow check")
	cctx, _ := h.ctx().CacheContext()
	h.app.IBCKeeper.ChannelKeeper.SetChannel(cctx, transfertypes.PortID, "channel-7", channeltypes.Channel{State: channeltypes.OPEN})
	escrow := transfertypes.GetEscrowAddress(transfertypes.PortID, "channel-7")
	other := transfertypes.GetEscrowAddress(transfertypes.PortID, "channel-8") // no such channel: just an address

	ms := compliancekeeper.NewMsgServerImpl(h.app.ComplianceKeeper)
	_, err := ms.EmergencyFreeze(cctx, &compliancetypes.MsgEmergencyFreeze{Authority: h.authority.AccAddr.String(), Addresses: []string{escrow.String()}, Reason: "t"})
	require.ErrorIs(t, err, compliancetypes.ErrProtectedAddress)
	_, err = ms.GovUpdate(cctx, &compliancetypes.MsgGovUpdate{Authority: h.app.ComplianceKeeper.GovAuthority(), Changes: []compliancetypes.Change{{Address: escrow.String(), List: compliancetypes.LIST_BLOCK, Action: compliancetypes.ACTION_ADD, Reason: "t"}}})
	require.ErrorIs(t, err, compliancetypes.ErrProtectedAddress)
	_, err = ms.EmergencyFreeze(cctx, &compliancetypes.MsgEmergencyFreeze{Authority: h.authority.AccAddr.String(), Addresses: []string{other.String()}, Reason: "t"})
	require.NoError(t, err, "an address that is no channel's escrow must stay freezable")
}
