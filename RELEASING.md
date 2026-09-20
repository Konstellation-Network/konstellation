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

The workflow builds only a tag that passes three gates, checked before any
build starts:

1. The name is `vMAJOR.MINOR.PATCH` with an optional `-suffix` (`v1.0.0-rc1`).
   Nothing else — the name ends up in shell and file names.
2. It is annotated and **signed with a key GitHub verifies for the tagger**
   (the tag shows "Verified" on GitHub). A signature from an unregistered key
   is refused, not just an unsigned tag. Set up signing once — SSH is
   simplest: `git config --global gpg.format ssh` and `user.signingkey`
   pointing at a public key that is also registered on GitHub as a
   *signing* key (Settings → SSH and GPG keys → "Signing Key").
3. The tagged commit is on `main`.

```sh
git checkout main && git pull --ff-only
git tag -s v0.1.0 -m "v0.1.0: testnet-1 genesis binary"
git push origin v0.1.0
```

Version scheme: semver. `v0.x` until mainnet genesis; the mainnet genesis
binary is `v1.0.0`. State-breaking upgrades bump the minor (or major after
1.0), everything else the patch. The tag is what `konstellationd version`
prints — the workflow checks that.

A tag with a suffix (`v1.1.0-rc1` for the testnet upgrade drill, ENGINEERING.md
§15) is published as a GitHub *pre-release*: it never becomes "Latest", so
`gh release download` without a tag and the release badge keep pointing at the
last real release. Mainnet only ever runs an unsuffixed version.

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

- *not a version tag*, *not signed with a key registered on GitHub*, *not on
  main*: delete the tag (`git push origin :refs/tags/vX`, `git tag -d vX`),
  fix the cause, tag again. Never re-use a tag that was published. The
  signature verdict in the log is GitHub's `verification.reason`
  (`unknown_key`: the key is not registered as a signing key for the
  tagger's account; `unverified_email`: the tagger email is not verified on
  that account).
- *non-reproducible build*: the two runners produced different binaries. Do
  not publish by hand. Re-run the workflow once first: the two builds must
  land on the same `ubuntu-24.04` runner image, and during a GitHub image
  rollout they can differ (CGO is on, so the system compiler is an input). If
  it fails again, find the input that differs (a dependency resolved at build
  time, an unpinned tool, a timestamp in the build) and fix it.
- *vulncheck*: a reachable advisory not in `.govulncheck-allowlist`. Fix or
  justify under ENGINEERING.md §4.1.1 — in a PR, not in the release.
