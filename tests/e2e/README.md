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
- `ibc_test.go` — two chains joined by Hermes (Phase 3, §13): governance adds a
  rate limit, over-limit transfers are refused, at-limit ones land, a frozen
  receiver is error-acked and refunded. ~4 min; pulls the Hermes image.
- `module_account_test.go` — an EVM transfer to a module account is refused at
  `eth_sendRawTransaction` with the reason and nothing charged
  (`app/blocked_recipient.go`; ENGINEERING.md §4.1.1). Before that check the
  same tx failed inside the block and vanished from `eth_*` — this test is what
  showed it.

Phase 5 drills (state-breaking upgrade, chaos, halt-and-restart) belong here
too, once there is a release to upgrade from.
