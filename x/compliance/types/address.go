package types

import (
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/common"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// Lists are keyed by the raw 20-byte account address, which is the same bytes
// whether a user writes it as kons1… or 0x…: Konstellation accounts are
// eth_secp256k1 and both encodings wrap the same 20 bytes. ParseAddress
// accepts either form; every stored key and every ante-handler comparison is
// on the bytes.

// ParseAddress accepts a bech32 (kons1…) or 0x-hex address and returns its
// 20-byte form.
func ParseAddress(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("empty address")
	}
	if common.IsHexAddress(s) {
		return common.HexToAddress(s).Bytes(), nil
	}
	acc, err := sdk.AccAddressFromBech32(s)
	if err != nil {
		return nil, fmt.Errorf("address %q is neither 0x-hex nor bech32: %w", s, err)
	}
	if len(acc) != common.AddressLength {
		return nil, fmt.Errorf("address %q is %d bytes, want %d", s, len(acc), common.AddressLength)
	}
	return acc.Bytes(), nil
}

// Bech32 renders 20 address bytes in the chain's bech32 form.
func Bech32(addr []byte) string {
	return sdk.AccAddress(addr).String()
}
