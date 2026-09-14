#!/usr/bin/env bash
# Compare the cosmos/evm version pinned in go.mod against the newest upstream
# tag. If upstream is ahead, print a review brief (commits, diff stat for the
# ENGINEERING.md §4.2 hot zones, published advisories) and exit 10. Uses the
# GitHub API throughout — no local clone — so a 6-hourly run stays cheap.
#
#   scripts/upstream-check.sh            # exit 0: up to date; exit 10: new release, brief on stdout
#   PINNED=v0.7.2 scripts/upstream-check.sh   # simulate an older pin
set -euo pipefail

REPO=cosmos/evm
MODULE=github.com/cosmos/evm
# ENGINEERING.md §4.2's documented hot zones. Keep this in sync with that list.
HOT_PATHS=(x/vm/ x/vm/statedb/ precompiles/)

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
pinned="${PINNED:-$(cd "$ROOT" && go mod edit -json | jq -r --arg m "$MODULE" '.Require[] | select(.Path==$m) | .Version')}"
[ -n "$pinned" ] || { echo "cannot find $MODULE in go.mod" >&2; exit 2; }

set +e
latest="$(gh api "repos/$REPO/tags" --paginate --jq '.[].name' 2>/dev/null | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -1)"
set -e
[ -n "$latest" ] || { echo "cannot list upstream tags" >&2; exit 2; }

if [ "$(printf '%s\n%s\n' "$pinned" "$latest" | sort -V | tail -1)" = "$pinned" ]; then
  echo "up to date: pinned $pinned, latest $latest"
  exit 0
fi

# Resolve a ref to its commit via the GitHub API. go.mod can pin a Go
# pseudo-version (vX.Y.Z-yyyymmddhhmmss-<sha>) instead of a tag; that string
# isn't a resolvable ref on its own, so fall back to its trailing commit SHA.
fetch_commit() {
  local ref="$1" json
  json="$(gh api "repos/$REPO/commits/$ref" 2>/dev/null)" || return 1
  [ -n "$json" ] && echo "$json"
}

latest_json="$(fetch_commit "$latest")" || { echo "cannot resolve upstream tag $latest" >&2; exit 2; }
latest_sha="$(echo "$latest_json" | jq -r .sha)"
tag_date="$(echo "$latest_json" | jq -r '.commit.committer.date[:10]')"

pinned_json="$(fetch_commit "$pinned")" || true
if [ -z "$pinned_json" ] && [[ "$pinned" =~ -([0-9a-f]{12})$ ]]; then
  pinned_json="$(fetch_commit "${BASH_REMATCH[1]}")" || true
fi

if [ -n "$pinned_json" ]; then
  pinned_resolvable=true
  pinned_sha="$(echo "$pinned_json" | jq -r .sha)"
  pinned_date="$(echo "$pinned_json" | jq -r '.commit.committer.date[:10]')"
else
  pinned_resolvable=false
  pinned_date="unresolvable"
fi

compare_json=""
if [ "$pinned_resolvable" = true ]; then
  compare_json="$(gh api "repos/$REPO/compare/${pinned_sha}...${latest_sha}" 2>/dev/null)" || compare_json=""
fi

{
  echo "## cosmos/evm $latest released ($tag_date) — pinned $pinned ($pinned_date)"
  echo
  echo "ENGINEERING.md §4.2: treat as a security release until proven otherwise. Diff the hot zones, record the review in §4.1, bump within 24h if it is one."
  echo
  echo "### Release notes"
  gh release view "$latest" -R "$REPO" --json body --jq .body 2>/dev/null | sed 's/^/> /' || echo "> (none)"
  echo
  echo "### Published advisories since $pinned_date"
  if [ "$pinned_resolvable" = true ]; then
    adv="$(gh api "repos/$REPO/security-advisories?state=published&per_page=20" --jq ".[] | select(.published_at >= \"$pinned_date\") | \"- \(.published_at[:10]) **\(.ghsa_id)** (\(.severity)): \(.summary) — affected: \(.vulnerabilities[0].vulnerable_version_range)\"" 2>/dev/null || true)"
  else
    adv="$(gh api "repos/$REPO/security-advisories?state=published&per_page=20" --jq ".[] | \"- \(.published_at[:10]) **\(.ghsa_id)** (\(.severity)): \(.summary) — affected: \(.vulnerabilities[0].vulnerable_version_range)\"" 2>/dev/null || true)"
  fi
  [ -n "$adv" ] && echo "$adv" || echo "- none published (does not mean none fixed — see §4.2)"
  echo
  echo "### Commits $pinned..$latest"
  if [ -n "$compare_json" ]; then
    echo "$compare_json" | jq -r '.commits[] | "- \(.sha[0:7]) \(.commit.message | split("\n")[0])"'
    total="$(echo "$compare_json" | jq -r '.total_commits // 0')"
    shown="$(echo "$compare_json" | jq -r '.commits | length')"
    if [ "$total" -gt "$shown" ] 2>/dev/null; then
      echo "- (showing $shown of $total commits — range exceeds the compare API's page size; full range: $(echo "$compare_json" | jq -r '.html_url'))"
    fi
  else
    echo "- cannot resolve pinned version $pinned to a commit, or the GitHub compare API call failed; commit list unavailable"
  fi
  echo
  echo "### Hot-zone diff stat (${HOT_PATHS[*]})"
  echo '```'
  if [ -n "$compare_json" ]; then
    hot_paths_json="$(printf '%s\n' "${HOT_PATHS[@]}" | jq -R . | jq -s .)"
    hot_files_json="$(echo "$compare_json" | jq -c --argjson paths "$hot_paths_json" \
      '[.files[]? | select(.filename as $f | any($paths[]; . as $p | $f | startswith($p)))]')"
    echo "$hot_files_json" | jq -r '.[] | "\(.filename) | +\(.additions) -\(.deletions)"'
    echo "$hot_files_json" | jq -r '
      if length == 0 then empty else
        ([.[].additions] | add) as $ins | ([.[].deletions] | add) as $del |
        " \(length) file\(if length == 1 then "" else "s" end) changed, " +
        "\($ins) insertion\(if $ins == 1 then "" else "s" end)(+), " +
        "\($del) deletion\(if $del == 1 then "" else "s" end)(-)"
      end'
    file_count="$(echo "$compare_json" | jq -r '.files | length')"
    if [ "$file_count" = "300" ]; then
      echo "(warning: compare API returned exactly 300 changed files, its cap — the diff may be truncated; verify manually: $(echo "$compare_json" | jq -r '.permalink_url // .html_url'))"
    fi
  else
    echo "(cannot resolve pinned version $pinned to a commit, or the compare API call failed; diff stat unavailable)"
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
