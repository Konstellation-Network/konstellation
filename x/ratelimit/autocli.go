package ratelimit

import (
	autocliv1 "cosmossdk.io/api/cosmos/autocli/v1"

	"github.com/Konstellation-Network/konstellation/x/ratelimit/types"
)

// AutoCLIOptions gives `konstellationd query ratelimit …`. The tx side is
// governance-only, so it is reached through gov proposals, not the CLI.
func (AppModuleBasic) AutoCLIOptions() *autocliv1.ModuleOptions {
	return &autocliv1.ModuleOptions{
		Query: &autocliv1.ServiceCommandDescriptor{
			Service: types.Query_serviceDesc.ServiceName,
			RpcCommandOptions: []*autocliv1.RpcCommandOptions{
				{RpcMethod: "RateLimits", Use: "list", Short: "Every rate limit and its current flow"},
				{
					RpcMethod: "RateLimit", Use: "show [channel-id] [denom]", Short: "One path's limit and flow",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "channel_id"}, {ProtoField: "denom"}},
				},
				{
					RpcMethod: "RateLimitsByChannel", Use: "by-channel [channel-id]", Short: "The limits on one channel",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "channel_id"}},
				},
			},
		},
		Tx: &autocliv1.ServiceCommandDescriptor{
			Service: types.Msg_serviceDesc.ServiceName,
			RpcCommandOptions: []*autocliv1.RpcCommandOptions{
				{RpcMethod: "AddRateLimit", Skip: true},
				{RpcMethod: "UpdateRateLimit", Skip: true},
				{RpcMethod: "RemoveRateLimit", Skip: true},
				{RpcMethod: "ResetRateLimit", Skip: true},
			},
		},
	}
}
