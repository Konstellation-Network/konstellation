# Releasing `konstellationd`

Every binary a network runs is built by `.github/workflows/release.yml` from a
signed tag (ENGINEERING.md §2.6). The workflow builds twice on independent
runners, publishes only if the checksums agree, attaches GitHub build
provenance, and creates the GitHub Release with the asset and `SHA256SUMS`.
Nothing is built by hand.

## Before tagging

1. `main` is green: CI (build, unit, integration, e2e, lint) and the nightly
   `vuln` job.
2. `go.mod`'s `toolchain` is the latest patch of its Go line (ENGINEERING.md
   §3), and `ci.yml`, `release.yml`, `vuln.yml` and `tests/e2e/go.mod` carry
   the same version. `curl -s 'https://go.dev/dl/?mode=json' | jq -r '.[].version'`
   lists what is current.
3. If the release is a state-breaking upgrade of a running network: an
   `app/upgrades/<name>/` package with the handler and store upgrades
   (`app/upgrades/README.md`; one package per release, permanently), and the
   upgrade drill run on testnet first (ENGINEERING.md §15 phase 5).
4. `STATUS.md` says what this release contains.

## Tag

Tags are annotated and **signed**; the workflow refuses anything else. Set up
signing once (SSH is simplest — `git config --global gpg.format ssh` and
`user.signingkey` pointing at a public key that is also registered on GitHub
as a *signing* key, so the tag shows "Verified"):

```sh
git checkout main && git pull --ff-only
git tag -s v0.1.0 -m "v0.1.0: testnet-1 genesis binary"
git push origin v0.1.0
```

Version scheme: semver. `v0.x` until mainnet genesis; the mainnet genesis
binary is `v1.0.0`. State-breaking upgrades bump the minor (or major after
1.0), everything else the patch. The tag is what `konstellationd version`
prints — the workflow checks that.

## After the workflow finishes

1. Download the asset and verify:
   ```sh
   gh release download v0.1.0 -R Konstellation-Network/konstellation
   sha256sum -c SHA256SUMS
   gh attestation verify konstellationd-v0.1.0-linux-amd64 --owner Konstellation-Network
   ```
2. Record the version, date and SHA256 in `networks/RELEASES.md` — the ledger
   operators and `infra` take checksums from (ENGINEERING.md §5.2). Nothing
   runs a binary that is not in that table.
3. For an upgrade: `networks/<net>/upgrades/<version>.md` from the template,
   with the same checksum; then the proposal (mainnet) or
   `infra/ansible/upgrade.yml` (testnet).
4. `infra`: set `konstellationd_version` / `konstellationd_sha256`.

## If the workflow fails

- *tag is not signed*: delete the tag (`git push origin :refs/tags/vX`,
  `git tag -d vX`), sign, push again. Never re-use a tag that was published.
- *non-reproducible build*: the two runners produced different binaries. Do
  not publish by hand. Find the input that differs (a dependency resolved at
  build time, an unpinned tool, a timestamp in the build) and fix it.
- *vulncheck*: a reachable advisory not in `.govulncheck-allowlist`. Fix or
  justify under ENGINEERING.md §4.1.1 — in a PR, not in the release.
