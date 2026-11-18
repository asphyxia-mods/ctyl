#!/usr/bin/env bash
# Commits new state records to the release-state branch, appends latency
# records and regenerates STATUS.md.
# Usage: scripts/record-state.sh <dir-with-java_N.json> [reason]
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
new=${1:?directory}
reason=${2:-}
ensure_ctyl
work=$(mktemp -d)
if git ls-remote --exit-code --heads origin release-state >/dev/null 2>&1; then
  git fetch -q origin release-state
  git worktree add -q "$work" origin/release-state
  git -C "$work" switch -q -c release-state-update
else
  git worktree add -q --detach "$work"
  git -C "$work" switch -q --orphan release-state-update
  git -C "$work" rm -rq . 2>/dev/null || true
fi
mkdir -p "$work/state"
shopt -s nullglob
for f in "$new"/java_*.json; do
  tag=$(basename "$f" .json)
  cp "$f" "$work/state/$tag.json"
  jq -c --arg reason "$reason" '{tag, digest, status, promoted, distribution, runtime_version, upstream_latest_ga,
      upstream_latest_ga_date, published_at, latency_hours, reason: $reason}' "$f" >> "$work/state/latency.jsonl"
done
# Lines whose candidates were all rejected: keep the published digest, but
# record the current (non-verified) status so it is visible and alerted.
for env_file in "$new"/gates/java_*/selected.env; do
  # shellcheck disable=SC1090
  (source "$env_file"; [[ -z ${SELECTED:-} ]] || exit 0
   gate=$(find "$(dirname "$env_file")" -mindepth 2 -maxdepth 2 -name gate.json | sort | head -1)
   [[ -n $gate ]] && ctyl mark --report "$gate" --state "$work/state/$TAG.json")
done
ctyl status --state "$work/state" --out "$work/STATUS.md"
cd "$work"
git add -A
if git diff --cached --quiet; then
  log "release state unchanged"
  exit 0
fi
git -c user.name="github-actions[bot]" -c user.email="41898282+github-actions[bot]@users.noreply.github.com" \
  commit -q -m "release state: ${reason:-update}"
git push -q origin HEAD:release-state
log "release-state updated"
