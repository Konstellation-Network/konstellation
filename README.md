# konstellation

The Konstellation Network chain. The **only** repo in the org that produces a
binary: `konstellationd`.

Read `../ENGINEERING.md` (org root) before working here. §2 lists hard constraints.

## Stack

| | |
|---|---|
| `cosmos/evm` | v0.7.3 — imported via `go.mod`, never forked (§2.1) |
| Cosmos SDK / CometBFT / ibc-go | as pulled by `cosmos/evm` (§3) |
| BlockSTM | **off** — `txnrunner.NewDefaultRunner` (§2.5) |

## Chain identity

| | mainnet | testnet | local dev |
|---|---|---|---|
| Cosmos chain-id | `konstellation-1` | `testnet-1` | `konstellation-local-1` (or anything unlisted) |
| EIP-155 chain id | `5667` | `56671` | `56670` |
| Token | KASH — base denom `esp`, 18 decimals | | |
| Bech32 prefix | `kons` | | |

The EVM chain id lives in `app.toml` → `[evm] evm-chain-id`. `konstellationd init
--chain-id <id>` writes the matching value; unrecognised chain-ids get the local
id so a dev-chain signature can never replay on a real network. At startup the
node refuses to run `konstellation-1` / `testnet-1` with the wrong EVM id.

## Layout

```
app/            module wiring (app.go, incl. upstream evmante handler), genesis defaults, upgrades/
app/config/     chain constants, bech32, app.toml defaults, module permissions
cmd/konstellationd/
x/              compliance (D6), ratelimit (D15)
local_node.sh   single-validator dev chain
tests/integration/  the real app in-process, signed txs, no Docker (`-tags test`)
tests/e2e/      real nodes under interchaintest (own go.mod; needs Docker)
```

## Develop

```sh
make build            # → build/konstellationd + sha256
make verify-deps      # §2.2/§2.3 dependency policy
make vulncheck        # govulncheck (also runs in CI on PR + nightly)
make test-unit
make test-integration # ~2 s
make docker-build && make test-e2e   # ~1 min per test, Docker required
./local_node.sh -y    # dev chain; JSON-RPC on :8545, WebSocket on :8546
```

## What `init` writes that is not an SDK default

`konstellationd init --chain-id <id>` produces a complete Konstellation genesis
(`app.DefaultGenesis`); nothing is patched afterwards. Beyond denoms, EVM params
and the D10/D11 economics:

- **Validator admission is closed (D16).** `x/circuit` starts with
  `/cosmos.staking.v1beta1.MsgCreateValidator` in `disabled_type_urls` on every
  chain-id — mainnet, testnet-1 and dev chains alike, so the dev chain shows what
  the networks do. The genesis validators are gentxs, which run before the circuit
  state is loaded, so they are unaffected. A `create-validator` afterwards is
  refused at submission with
  `circuit breaker disables /cosmos.staking.v1beta1.MsgCreateValidator: unauthorized`.
  The super admin (`account_permissions`, a per-network genesis entry: the 3-of-5
  operations multisig on a real network, the validator key `mykey` on
  `local_node.sh`) opens the window:
  ```sh
  konstellationd tx circuit reset   /cosmos.staking.v1beta1.MsgCreateValidator --from mykey
  konstellationd tx staking create-validator validator.json --from operator
  konstellationd tx circuit disable /cosmos.staking.v1beta1.MsgCreateValidator --from mykey
  ```
  (`infra/runbooks/validator-admission.md`; a governance `reset` makes it
  permissionless for good.) Tested in `app/genesis_test.go`,
  `tests/integration` (`TestValidatorAdmissionWindow`) and `tests/e2e`
  (`TestValidatorAdmissionGate`).
- **Active precompiles are the ones cosmos/evm implements.** v0.7.3 lists a
  `vesting` precompile at `0x…0803` in `AvailableStaticPrecompiles` but ships no
  code for it; marking it active made every call to that address fail with
  "precompiled contract not stored in memory". Genesis leaves it out
  (`app.InertUpstreamPrecompiles`); `eth_getCode` there is empty and a call is an
  ordinary call to an empty account. The active set is `0x…0100` (p256),
  `0x…0400` (bech32), `0x…0800`–`0x…0802`, `0x…0804`–`0x…0807`, `0x…0900`
  (compliance), plus the dynamic WKASH precompile at
  `0xD4949664cD82660AaE99bEdc034a0deA8A0bd517`. Re-checked on every cosmos/evm
  bump (`app/upstream_pin_test.go`).

## JSON-RPC over WebSocket from a browser

`app.toml` `[json-rpc] ws-origins` is the allow-list of browser Origin **hosts**
(cosmos/evm compares `url.Parse(Origin).Hostname()`, so scheme and port do not
matter: `["localhost", "app.example.com"]` admits `http://localhost:5173` and
`https://app.example.com`; `"*"` admits every origin). Requests without an
`Origin` header — Node, Go, curl — are always admitted. A dapp served from
another host needs its host in that array on the RPC node it talks to; the
default is `["127.0.0.1", "localhost"]`.

Until this repo's fix, the array never reached the server: cosmos-sdk's config
interception copies each app.toml value onto the matching `start` flag with
`fmt.Sprintf("%v", value)`, which turns a TOML array into the single string
`[127.0.0.1 localhost]`, and every browser Origin got a 403 while curl worked
(STATUS.md §5a P27). `cmd/konstellationd/cmd/flags.go` restores the array after
the interception; `TestStartHonoursAppTomlWSOrigins` drives cosmos/evm's real
upgrade handler with the config a node loads. Passing
`--json-rpc.ws-origins a,b` on the command line always worked and still wins.

## Upstream watch

`.github/workflows/upstream-watch.yml` runs `scripts/upstream-check.sh` four times a
day. When cosmos/evm publishes a tag newer than `go.mod`'s pin it opens an issue
(label `upstream-release`) with the release notes, published advisories, commit
list and hot-zone diff stat. The nightly `govulncheck` job opens an issue (label
`vulncheck`) on failure. Both need an owner (ENGINEERING.md §17).

## Bumping `cosmos/evm`

Treat every patch tag as a security release (§4.2). Diff `x/vm/`, `x/vm/statedb/`,
`precompiles/` in `~/src/evm-reference` first, then bump `go.mod`, `make verify-deps`,
`make test-unit`, `make vulncheck`, and record the review in `ENGINEERING.md §4.1`.
