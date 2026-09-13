#!/usr/bin/env bash
# Run govulncheck and fail on any finding reachable from our code that is not
# in .govulncheck-allowlist (ENGINEERING.md §4.3).
#
#   scripts/vulncheck.sh source            # govulncheck ./...  (nightly; slow, full traces)
#   scripts/vulncheck.sh binary <path>     # govulncheck -mode=binary <path>  (PRs; fast)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ALLOW="$ROOT/.govulncheck-allowlist"
mode="${1:-source}"

case "$mode" in
  source) args=(./...) ;;
  binary) [ -n "${2:-}" ] || { echo "usage: $0 binary <path-to-binary>" >&2; exit 2; }; args=(-mode=binary "$2") ;;
  *) echo "unknown mode: $mode" >&2; exit 2 ;;
esac

command -v jq >/dev/null || { echo "jq is required" >&2; exit 2; }
GOVULNCHECK="$(command -v govulncheck || true)"
if [ -z "$GOVULNCHECK" ]; then
  go install golang.org/x/vuln/cmd/govulncheck@latest
  GOVULNCHECK="$(go env GOPATH)/bin/govulncheck"
fi
[ -x "$GOVULNCHECK" ] || { echo "govulncheck not found on PATH or in $(go env GOPATH)/bin" >&2; exit 2; }

out="$(mktemp)"; trap 'rm -f "$out"' EXIT
# exit 3 = vulnerabilities found; we decide below. Any other non-zero is a real failure.
"$GOVULNCHECK" -format json "${args[@]}" > "$out" || [ $? -eq 3 ]

allowed="$(grep -Ev '^\s*(#|$)' "$ALLOW" | awk '{print $1}' | sort -u || true)"

# "reachable" = a finding whose trace starts at a function (symbol-level hit).
reachable="$(jq -r 'select(.finding? and .finding.trace[0].function?) | .finding.osv' "$out" | sort -u)"

blocked="$(comm -23 <(echo "$reachable") <(echo "$allowed") | sed '/^$/d' || true)"
ignored="$(comm -12 <(echo "$reachable") <(echo "$allowed") | sed '/^$/d' || true)"

if [ -n "$ignored" ]; then
  echo "allow-listed (see .govulncheck-allowlist):"
  for id in $ignored; do echo "  $id  $(grep -E "^$id " "$ALLOW" | cut -d' ' -f2-)"; done
fi

if [ -n "$blocked" ]; then
  echo
  echo "BLOCKING vulnerabilities reachable from our code:"
  for id in $blocked; do
    jq -r --arg id "$id" 'select(.osv? and .osv.id==$id) | "  \(.osv.id)  \(.osv.summary)"' "$out"
    jq -r --arg id "$id" 'select(.finding? and .finding.osv==$id and .finding.trace[0].function?) | "      \(.finding.trace[0].module)@\(.finding.trace[0].version // "?")  \(.finding.trace[0].package).\(.finding.trace[0].function)"' "$out" | sort -u | head -3 || true
  done
  echo
  echo "Bump the dependency, or add an entry to .govulncheck-allowlist with a justification recorded in ENGINEERING.md §4.1.1."
  exit 1
fi

echo "vulncheck OK: no unlisted reachable vulnerabilities ($mode)"
