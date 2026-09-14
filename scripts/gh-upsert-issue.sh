#!/usr/bin/env bash
# Idempotently open or update a GitHub issue, matched by an exact title within
# a label. A past match that is closed is reopened rather than silently
# rewritten while closed — a recurring failure or an unresolved release-review
# issue must stay visible.
#
#   scripts/gh-upsert-issue.sh <label> <label-description> <title> <body-file>
set -euo pipefail

label="$1"
label_description="$2"
title="$3"
body_file="$4"

gh label create "$label" --color B60205 --description "$label_description" 2>/dev/null || true

existing="$(gh issue list --label "$label" --state all --limit 1000 --json number,title,state \
  | jq -r --arg t "$title" '[.[] | select(.title == $t)][0] | if . then "\(.number)\t\(.state)" else empty end')"

if [ -n "$existing" ]; then
  number="${existing%%$'\t'*}"
  state="${existing#*$'\t'}"
  gh issue edit "$number" --body-file "$body_file"
  if [ "$state" = "CLOSED" ]; then
    gh issue reopen "$number"
    echo "reopened and updated #$number"
  else
    echo "updated #$number"
  fi
else
  gh issue create --title "$title" --label "$label" --body-file "$body_file"
fi
