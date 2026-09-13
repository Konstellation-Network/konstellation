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

| | mainnet | testnet |
|---|---|---|
| Cosmos chain-id | `konstellation-1` | `testnet-1` |
| EIP-155 chain id | `5667` | `56671` |
| Token | KASH — base denom `esp`, 18 decimals | |
| Bech32 prefix | `kons` | |

The EVM chain id is set in `app.toml` → `[evm] evm-chain-id`. `konstellationd init`
writes the **testnet** value by default; mainnet operators must set `5667`.

## Layout

```
app/            module wiring (app.go), genesis defaults, ante chain, upgrades/
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

## Bumping `cosmos/evm`

Treat every patch tag as a security release (§4.2). Diff `x/vm/`, `x/vm/statedb/`,
`precompiles/` in `~/src/evm-reference` first, then bump `go.mod`, `make verify-deps`,
`make test-unit`, `make vulncheck`, and record the review in `ENGINEERING.md §4.1`.
