package compliance

import (
	autocliv1 "cosmossdk.io/api/cosmos/autocli/v1"

	"github.com/Konstellation-Network/konstellation/x/compliance/types"
)

// AutoCLIOptions gives `konstellationd query compliance …` and
// `konstellationd tx compliance …` without hand-written CLI.
func (AppModuleBasic) AutoCLIOptions() *autocliv1.ModuleOptions {
	return &autocliv1.ModuleOptions{
		Query: &autocliv1.ServiceCommandDescriptor{
			Service: types.Query_serviceDesc.ServiceName,
			RpcCommandOptions: []*autocliv1.RpcCommandOptions{
				{RpcMethod: "Params", Use: "params", Short: "Module parameters"},
				{
					RpcMethod: "Status", Use: "status [address]", Short: "An address's standing on both lists",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "address"}},
				},
				{
					RpcMethod: "Entries", Use: "entries [allow|block]", Short: "Page through one list",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "list"}},
				},
				{RpcMethod: "PendingUpdates", Use: "pending", Short: "Scheduled updates waiting out their timelock"},
				{
					RpcMethod: "PendingUpdate", Use: "pending-update [id]", Short: "One scheduled update",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "id"}},
				},
			},
		},
		Tx: &autocliv1.ServiceCommandDescriptor{
			Service: types.Msg_serviceDesc.ServiceName,
			RpcCommandOptions: []*autocliv1.RpcCommandOptions{
				{RpcMethod: "ScheduleUpdate", Use: "schedule-update", Short: "Queue list changes behind the timelock (authority)"},
				{
					RpcMethod: "CancelUpdate", Use: "cancel-update [id]", Short: "Drop a pending update (authority or gov)",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "id"}},
				},
				{
					RpcMethod: "EmergencyFreeze", Use: "emergency-freeze [addresses...]", Short: "Freeze now, for one timelock period (authority)",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "addresses", Varargs: true}},
				},
				{
					RpcMethod: "LiftEmergencyFreeze", Use: "lift-emergency-freeze [addresses...]", Short: "End an emergency freeze early",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "addresses", Varargs: true}},
				},
				{RpcMethod: "GovUpdate", Skip: true},    // governance proposals only
				{RpcMethod: "UpdateParams", Skip: true}, // governance proposals only
			},
		},
	}
}
