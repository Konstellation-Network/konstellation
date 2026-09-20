package app

import (
	"fmt"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	corevm "github.com/ethereum/go-ethereum/core/vm"

	cmn "github.com/cosmos/evm/precompiles/common"
	distributionprecompile "github.com/cosmos/evm/precompiles/distribution"
	govprecompile "github.com/cosmos/evm/precompiles/gov"
	ics02precompile "github.com/cosmos/evm/precompiles/ics02"
	ics20precompile "github.com/cosmos/evm/precompiles/ics20"
	slashingprecompile "github.com/cosmos/evm/precompiles/slashing"
	stakingprecompile "github.com/cosmos/evm/precompiles/staking"
	"github.com/cosmos/evm/x/vm/statedb"
	evmtypes "github.com/cosmos/evm/x/vm/types"
	transfertypes "github.com/cosmos/ibc-go/v11/modules/apps/transfer/types"
	clienttypes "github.com/cosmos/ibc-go/v11/modules/core/02-client/types"

	storetypes "github.com/cosmos/cosmos-sdk/store/v2/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	distributiontypes "github.com/cosmos/cosmos-sdk/x/distribution/types"
	govv1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// The circuit breaker sees messages that go through the MsgServiceRouter.
// cosmos/evm's tx-path precompiles do not: the ICS20 precompile calls
// transferKeeper.Transfer directly, the staking/distribution/gov/slashing
// ones call their msg servers directly. Without this file, tripping
// MsgTransfer stopped Cosmos senders and left every EOA and contract calling
// 0x…0802 free to keep sending — the "IBC emergency stop" of D14 was only
// half an emergency stop (PR #12 review, 2026-09-20).
//
// circuitGuard wraps a precompile and, for its transaction methods, asks the
// breaker about the Cosmos message that method executes before running it.
// A tripped type reverts with the reason, the same words the ante uses.

// precompileMsgTypes maps each tx-path precompile method to the type URL of
// the message it executes (cosmos/evm v0.7.3; app/upstream_pin_test.go
// makes a version bump re-check this table). An entry mapped to "" runs no
// Cosmos message and is never gated. A tx method missing from the table is
// gated by every URL its precompile does map — an upstream addition fails
// closed until it is classified.
var precompileMsgTypes = map[string]map[string]string{
	evmtypes.ICS20PrecompileAddress: {
		ics20precompile.TransferMethod: sdk.MsgTypeURL(&transfertypes.MsgTransfer{}),
	},
	evmtypes.StakingPrecompileAddress: {
		stakingprecompile.CreateValidatorMethod:           sdk.MsgTypeURL(&stakingtypes.MsgCreateValidator{}),
		stakingprecompile.EditValidatorMethod:             sdk.MsgTypeURL(&stakingtypes.MsgEditValidator{}),
		stakingprecompile.DelegateMethod:                  sdk.MsgTypeURL(&stakingtypes.MsgDelegate{}),
		stakingprecompile.UndelegateMethod:                sdk.MsgTypeURL(&stakingtypes.MsgUndelegate{}),
		stakingprecompile.RedelegateMethod:                sdk.MsgTypeURL(&stakingtypes.MsgBeginRedelegate{}),
		stakingprecompile.CancelUnbondingDelegationMethod: sdk.MsgTypeURL(&stakingtypes.MsgCancelUnbondingDelegation{}),
	},
	evmtypes.DistributionPrecompileAddress: {
		distributionprecompile.SetWithdrawAddressMethod:          sdk.MsgTypeURL(&distributiontypes.MsgSetWithdrawAddress{}),
		distributionprecompile.WithdrawDelegatorRewardMethod:     sdk.MsgTypeURL(&distributiontypes.MsgWithdrawDelegatorReward{}),
		distributionprecompile.ClaimRewardsMethod:                sdk.MsgTypeURL(&distributiontypes.MsgWithdrawDelegatorReward{}),
		distributionprecompile.WithdrawValidatorCommissionMethod: sdk.MsgTypeURL(&distributiontypes.MsgWithdrawValidatorCommission{}),
		distributionprecompile.FundCommunityPoolMethod:           sdk.MsgTypeURL(&distributiontypes.MsgFundCommunityPool{}),
		distributionprecompile.DepositValidatorRewardsPoolMethod: sdk.MsgTypeURL(&distributiontypes.MsgDepositValidatorRewardsPool{}),
	},
	evmtypes.GovPrecompileAddress: {
		govprecompile.VoteMethod:           sdk.MsgTypeURL(&govv1.MsgVote{}),
		govprecompile.VoteWeightedMethod:   sdk.MsgTypeURL(&govv1.MsgVoteWeighted{}),
		govprecompile.SubmitProposalMethod: sdk.MsgTypeURL(&govv1.MsgSubmitProposal{}),
		govprecompile.DepositMethod:        sdk.MsgTypeURL(&govv1.MsgDeposit{}),
		govprecompile.CancelProposalMethod: sdk.MsgTypeURL(&govv1.MsgCancelProposal{}),
	},
	evmtypes.SlashingPrecompileAddress: {
		slashingprecompile.UnjailMethod: sdk.MsgTypeURL(&slashingtypes.MsgUnjail{}),
	},
	evmtypes.ICS02PrecompileAddress: {
		ics02precompile.UpdateClientMethod: sdk.MsgTypeURL(&clienttypes.MsgUpdateClient{}),
		// Proof verification against a client's state: no message behind it.
		ics02precompile.VerifyMembershipMethod:    "",
		ics02precompile.VerifyNonMembershipMethod: "",
	},
}

// abiPrecompile is what a cosmos/evm precompile exposes beyond the EVM
// interface: its ABI (MethodById is promoted from the embedded abi.ABI) and
// which methods change state.
type abiPrecompile interface {
	corevm.PrecompiledContract
	MethodById(sigdata []byte) (*abi.Method, error)
	IsTransaction(method *abi.Method) bool
}

// circuitGuard is a precompile whose tx methods consult the circuit breaker.
type circuitGuard struct {
	abiPrecompile
	msgTypes map[string]string
	all      []string
	breaker  circuitBreaker
}

// Run refuses a tx method whose message type is tripped, then defers.
func (g circuitGuard) Run(evm *corevm.EVM, contract *corevm.Contract, readonly bool) ([]byte, error) {
	if len(contract.Input) >= 4 {
		if method, err := g.MethodById(contract.Input[:4]); err == nil && g.IsTransaction(method) {
			if err := g.check(evm, method.Name); err != nil {
				return cmn.ReturnRevertError(evm, err)
			}
		}
	}
	return g.abiPrecompile.Run(evm, contract, readonly)
}

func (g circuitGuard) check(evm *corevm.EVM, method string) error {
	urls := g.all
	if url, known := g.msgTypes[method]; known {
		if url == "" {
			return nil
		}
		urls = []string{url}
	}
	stateDB, ok := evm.StateDB.(*statedb.StateDB)
	if !ok {
		return fmt.Errorf("%s", cmn.ErrNotRunInEvm)
	}
	ctx, err := stateDB.GetCacheContext()
	if err != nil {
		return err
	}
	// One KV read; the EVM already charged RequiredGas for the call.
	ctx = ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	for _, url := range urls {
		allowed, err := g.breaker.IsAllowed(ctx, url)
		if err != nil {
			return err
		}
		if !allowed {
			return fmt.Errorf("circuit breaker disables %s", url)
		}
	}
	return nil
}

// withCircuitGuard wraps every precompile in m that precompileMsgTypes
// knows. Read-only precompiles (bank, bech32, p256, compliance) are left
// alone; so is anything not in the table.
func withCircuitGuard(m map[common.Address]corevm.PrecompiledContract, breaker circuitBreaker) map[common.Address]corevm.PrecompiledContract {
	for hexAddr, msgTypes := range precompileMsgTypes {
		addr := common.HexToAddress(hexAddr)
		p, present := m[addr]
		if !present {
			continue
		}
		ap, ok := p.(abiPrecompile)
		if !ok {
			panic(fmt.Sprintf("precompile %s does not expose its ABI; cannot guard it", hexAddr))
		}
		var all []string
		seen := map[string]bool{}
		for _, url := range msgTypes {
			if url != "" && !seen[url] {
				seen[url] = true
				all = append(all, url)
			}
		}
		m[addr] = circuitGuard{abiPrecompile: ap, msgTypes: msgTypes, all: all, breaker: breaker}
	}
	return m
}
