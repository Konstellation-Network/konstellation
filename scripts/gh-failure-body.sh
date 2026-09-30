#!/usr/bin/env bash
# Build a standard CI-failure issue body: an intro line, a link to the run,
# the tail of a log file (or a note that none was captured), and an optional
# trailing note. Shared by .circleci/upstream-watch.yml and
# .circleci/vuln.yml so the two don't independently drift.
#
#   scripts/gh-failure-body.sh <intro-line> <log-file> [trailing-note]
#
# Relies on CIRCLE_BUILD_URL, which CircleCI sets on every job.
set -euo pipefail

intro="$1"
logfile="$2"
trailing="${3:-}"

echo "$intro"
echo
echo "Run: ${CIRCLE_BUILD_URL}"
echo
echo '```'
if [ -s "$logfile" ]; then
  tail -60 "$logfile"
else
  echo "(no log captured for this failure — check the run above)"
fi
echo '```'

if [ -n "$trailing" ]; then
  echo
  echo "$trailing"
fi
