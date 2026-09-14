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
local_node.sh   single-validator dev chain
tests/e2e/      interchaintest (todo)
```

## Develop

```sh
make build            # → build/konstellationd + sha256
make verify-deps      # §2.2/§2.3 dependency policy
make vulncheck        # govulncheck (also runs in CI on PR + nightly)
make test-unit
./local_node.sh -y    # dev chain; JSON-RPC on :8545
```

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
