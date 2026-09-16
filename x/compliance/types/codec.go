package types

import (
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/codec/legacy"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/msgservice"
)

// RegisterLegacyAminoCodec registers the messages for amino (ledger/EIP-712
// signing paths still route through it).
func RegisterLegacyAminoCodec(cdc *codec.LegacyAmino) {
	legacy.RegisterAminoMsg(cdc, &MsgScheduleUpdate{}, "kons/compliance/MsgScheduleUpdate")
	legacy.RegisterAminoMsg(cdc, &MsgCancelUpdate{}, "kons/compliance/MsgCancelUpdate")
	legacy.RegisterAminoMsg(cdc, &MsgEmergencyFreeze{}, "kons/compliance/MsgEmergencyFreeze")
	legacy.RegisterAminoMsg(cdc, &MsgLiftEmergencyFreeze{}, "kons/compliance/MsgLiftEmergencyFreeze")
	legacy.RegisterAminoMsg(cdc, &MsgGovUpdate{}, "kons/compliance/MsgGovUpdate")
	legacy.RegisterAminoMsg(cdc, &MsgUpdateParams{}, "kons/compliance/MsgUpdateParams")
}

// RegisterInterfaces registers the messages with the interface registry.
func RegisterInterfaces(registry codectypes.InterfaceRegistry) {
	registry.RegisterImplementations((*sdk.Msg)(nil),
		&MsgScheduleUpdate{},
		&MsgCancelUpdate{},
		&MsgEmergencyFreeze{},
		&MsgLiftEmergencyFreeze{},
		&MsgGovUpdate{},
		&MsgUpdateParams{},
	)
	msgservice.RegisterMsgServiceDesc(registry, &_Msg_serviceDesc)
}
