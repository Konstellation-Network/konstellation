# End-to-end tests

Real `konstellationd` nodes — the Docker image from `../../Dockerfile` — under
[interchaintest](https://github.com/cosmos/interchaintest), driven the way
operators and users drive them: the CLI over `docker exec`, CometBFT RPC/gRPC,
and the EVM JSON-RPC.

```
make docker-build   # konstellation:e2e
make test-e2e       # ~40 s per test; each boots its own one-validator chain
```

This directory is its own Go module so interchaintest's dependency tree (SDK
0.53 and its third-party `replace` pins) never touches the binary's `go.mod`
(ENGINEERING.md §2.2). Nothing here links into `konstellationd`.

What lives here versus `../integration` (the app in-process, no Docker):
in-process covers single-node semantics — ante chain, EVM execution,
x/compliance rules. This layer covers what only a real process shows:

- `restart_test.go` — a node restarted on existing state takes an EVM tx as
  its first tx (STATUS.md §2a, konstellation PR #8).
- `compliance_test.go` — a freeze issued through the CLI; the frozen party's
  txs refused synchronously at `eth_sendRawTransaction` (the JSON-RPC →
  `Mempool.Insert` path `app/mempool.go` wraps, which ABCI-level tests cannot
  reach); the EIP-155 id `init` derives from the genesis chain-id.
- `module_account_test.go` — an EVM transfer to a module account fails in the
  block (ENGINEERING.md §4.1.1), and what that looks like from the RPC: the
  failed SDK tx is not indexed as an Ethereum tx, so `eth_getTransactionReceipt`
  says "not found" and the reason is only in CometBFT's `tx_search`.

Phase 5 drills (state-breaking upgrade, chaos, halt-and-restart) belong here
too, once there is a release to upgrade from.
