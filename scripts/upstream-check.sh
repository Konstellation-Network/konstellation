#!/usr/bin/env bash
# Compare the cosmos/evm version pinned in go.mod against the newest upstream
# tag. If upstream is ahead, print a review brief (commits, diff stat for the
# ENGINEERING.md §4.2 hot zones, published advisories) and exit 10.
#
#   scripts/upstream-check.sh            # exit 0: up to date; exit 10: new release, brief on stdout
#   PINNED=v0.7.2 scripts/upstream-check.sh   # simulate an older pin
set -euo pipefail

UPSTREAM=https://github.com/cosmos/evm.git
MODULE=github.com/cosmos/evm
# ENGINEERING.md §4.2's documented hot zones. Keep this in sync with that list.
HOT_PATHS=(x/vm/ x/vm/statedb/ precompiles/)

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
pinned="${PINNED:-$(cd "$ROOT" && go mod edit -json | jq -r --arg m "$MODULE" '.Require[] | select(.Path==$m) | .Version')}"
[ -n "$pinned" ] || { echo "cannot find $MODULE in go.mod" >&2; exit 2; }

set +e
latest="$(git ls-remote --tags --refs "$UPSTREAM" | awk '{print $2}' | sed 's#refs/tags/##' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -1)"
set -e
[ -n "$latest" ] || { echo "cannot list upstream tags" >&2; exit 2; }

if [ "$(printf '%s\n%s\n' "$pinned" "$latest" | sort -V | tail -1)" = "$pinned" ]; then
  echo "up to date: pinned $pinned, latest $latest"
  exit 0
fi

work="$(mktemp -d)"; trap 'rm -rf "$work"' EXIT
git clone -q --filter=blob:none --no-checkout "$UPSTREAM" "$work/evm"
cd "$work/evm"
tag_date="$(git log -1 --format=%ad --date=short "$latest")"

# go.mod can pin a Go pseudo-version (vX.Y.Z-yyyymmddhhmmss-<sha>) instead of a
# tag; that string isn't a git ref on its own, so probe before trusting it as
# one and degrade gracefully rather than aborting the whole brief.
if pinned_date="$(git log -1 --format=%ad --date=short "$pinned" 2>/dev/null)"; then
  pinned_resolvable=true
else
  pinned_resolvable=false
  pinned_date="unresolvable"
fi

{
  echo "## cosmos/evm $latest released ($tag_date) — pinned $pinned ($pinned_date)"
  echo
  echo "ENGINEERING.md §4.2: treat as a security release until proven otherwise. Diff the hot zones, record the review in §4.1, bump within 24h if it is one."
  echo
  echo "### Release notes"
  gh release view "$latest" -R cosmos/evm --json body --jq .body 2>/dev/null | sed 's/^/> /' || echo "> (none)"
  echo
  echo "### Published advisories since $pinned_date"
  if [ "$pinned_resolvable" = true ]; then
    adv="$(gh api "repos/cosmos/evm/security-advisories?state=published&per_page=20" --jq ".[] | select(.published_at >= \"$pinned_date\") | \"- \(.published_at[:10]) **\(.ghsa_id)** (\(.severity)): \(.summary) — affected: \(.vulnerabilities[0].vulnerable_version_range)\"" 2>/dev/null || true)"
  else
    adv="$(gh api "repos/cosmos/evm/security-advisories?state=published&per_page=20" --jq ".[] | \"- \(.published_at[:10]) **\(.ghsa_id)** (\(.severity)): \(.summary) — affected: \(.vulnerabilities[0].vulnerable_version_range)\"" 2>/dev/null || true)"
  fi
  [ -n "$adv" ] && echo "$adv" || echo "- none published (does not mean none fixed — see §4.2)"
  echo
  echo "### Commits $pinned..$latest"
  if [ "$pinned_resolvable" = true ]; then
    git log --oneline "$pinned..$latest" | sed 's/^/- /'
  else
    echo "- cannot resolve pinned version $pinned as a git ref (e.g. a Go pseudo-version); commit list unavailable"
  fi
  echo
  echo "### Hot-zone diff stat (${HOT_PATHS[*]})"
  echo '```'
  if [ "$pinned_resolvable" = true ]; then
    git diff --stat "$pinned..$latest" -- "${HOT_PATHS[@]}" || true
  else
    echo "(cannot resolve pinned version $pinned as a git ref; diff stat unavailable)"
  fi
  echo '```'
  echo
  echo "### Review checklist"
  echo "- [ ] \`git -C ~/src/evm-reference diff $pinned..$latest -- x/vm/ precompiles/\` read in full"
  echo "- [ ] go.mod bumped, \`make verify-deps\`, \`make test-unit\`, \`make vulncheck\` green"
  echo "- [ ] ENGINEERING.md §3 version matrix and §4.1 advisory table updated"
  echo "- [ ] state-breaking? → coordinated upgrade plan (§9.4)"
} 
exit 10
