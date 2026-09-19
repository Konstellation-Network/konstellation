# End-to-end tests

Multi-node tests via interchaintest (ENGINEERING.md §6.1). Not yet populated:
needs Docker and a `Dockerfile` for `konstellationd`. First jobs, when it
exists: the Phase 5 drills (state-breaking upgrade, chaos, halt-and-restart).

Single-node semantics — ante chain, EVM execution, x/compliance — are covered
in-process by `tests/integration` (`make test-integration`), which needs no
Docker.
