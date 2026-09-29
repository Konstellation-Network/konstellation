package e2e

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
)

const createValidatorURL = "/cosmos.staking.v1beta1.MsgCreateValidator"

// TestValidatorAdmissionGate is ENGINEERING.md D16 on a real node, through
// the CLI an operator would use: `konstellationd init` writes
// MsgCreateValidator into x/circuit's genesis disable list, the chain still
// starts from its gentx (the launch validators are unaffected), and a
// create-validator is refused at submission with the reason. Then the
// admission window from infra/runbooks/validator-admission.md: the super
// admin resets, the operator's create-validator lands and the validator
// joins the set, the admin disables again and the next one is refused.
func TestValidatorAdmissionGate(t *testing.T) {
	k := startChain(t)
	node := k.chain.GetNode()
	refusal := "circuit breaker disables " + createValidatorURL + ": unauthorized"

	// What init wrote is what the node runs: the URL is in the disabled list
	// and the gentx validator is bonded regardless.
	out, _, err := node.ExecQuery(k.ctx, "circuit", "disabled-list")
	require.NoError(t, err)
	require.Contains(t, string(out), createValidatorURL, "disabled-list from genesis: %s", out)
	require.Equal(t, 1, k.bondedValidators(), "the gentx validator must be in the set")

	// An operator with funds and a validator.json.
	const operator = "operator"
	require.NoError(t, node.CreateKey(k.ctx, operator))
	operatorAddr, err := node.AccountKeyBech32(k.ctx, operator)
	require.NoError(t, err)
	k.fund(operatorAddr, 100)
	k.waitBlocks()
	require.NoError(t, node.WriteFile(k.ctx, validatorJSON(t, "operator-1"), "validator-1.json"))
	createValidator := []string{"staking", "create-validator", node.HomeDir() + "/validator-1.json", "--gas", "500000"}

	// Refused at submission, with the exact reason, nothing bonded.
	_, err = node.ExecTx(k.ctx, operator, createValidator...)
	require.Error(t, err, "create-validator accepted while disabled in genesis")
	require.Contains(t, err.Error(), refusal)
	require.Equal(t, 1, k.bondedValidators())

	// The window.
	_, err = node.ExecTx(k.ctx, authorityKey, "circuit", "reset", createValidatorURL)
	require.NoError(t, err)
	// The refused tx consumed no sequence, so re-sending it unchanged would
	// be byte-identical and CometBFT's tx cache answers "tx already seen".
	// An operator re-signs with new bytes; a memo is the smallest change.
	_, err = node.ExecTx(k.ctx, operator, append(createValidator, "--note", "admission window")...)
	require.NoError(t, err, "create-validator refused after the super admin's reset")
	require.Equal(t, 2, k.bondedValidators(), "admitted validator did not join the set")
	_, err = node.ExecTx(k.ctx, authorityKey, "circuit", "disable", createValidatorURL)
	require.NoError(t, err)

	// Shut again for the next one.
	require.NoError(t, node.WriteFile(k.ctx, validatorJSON(t, "operator-2"), "validator-2.json"))
	_, err = node.ExecTx(k.ctx, operator, "staking", "create-validator", node.HomeDir()+"/validator-2.json", "--gas", "500000")
	require.Error(t, err)
	require.Contains(t, err.Error(), refusal)
	require.Equal(t, 2, k.bondedValidators())
}

// validatorJSON is the file `tx staking create-validator` takes, with a
// fresh consensus key (the operator's real one would come from
// `konstellationd comet show-validator` on their node).
func validatorJSON(t *testing.T, moniker string) []byte {
	t.Helper()
	pub := ed25519.GenPrivKey().PubKey().Bytes()
	bz, err := json.Marshal(map[string]any{
		"pubkey":                     map[string]string{"@type": "/cosmos.crypto.ed25519.PubKey", "key": base64.StdEncoding.EncodeToString(pub)},
		"amount":                     fmt.Sprintf("%s%s", kash(10), denom),
		"moniker":                    moniker,
		"commission-rate":            "0.05",
		"commission-max-rate":        "0.20",
		"commission-max-change-rate": "0.01",
		"min-self-delegation":        "1",
	})
	require.NoError(t, err)
	return bz
}

// bondedValidators counts validators with BOND_STATUS_BONDED as the node's
// staking query reports them.
func (k *konsChain) bondedValidators() int {
	k.t.Helper()
	out, _, err := k.chain.GetNode().ExecQuery(k.ctx, "staking", "validators")
	require.NoError(k.t, err)
	return strings.Count(string(out), `"BOND_STATUS_BONDED"`)
}
