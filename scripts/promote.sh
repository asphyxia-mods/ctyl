#!/usr/bin/env bash
# Moves the floating tag (e.g. java_21) to a published, signed, attested and
# verified digest. Only candidates that passed the security gate reach this
# point; the copy preserves the digest and is verified afterwards.
#
# The tag only moves forward: if it points at an image last evaluated by a
# run that is not older than this candidate's run (a newer run promoted it
# while this one waited, or this is a re-run of an older run), nothing changes
# and the step fails so that no release state is recorded for this run.
# Runs are ordered by their GitHub run ID, which a re-run keeps; evaluation
# times are not used because a re-run evaluates again. A candidate gated under
# a policy other than the one on the default branch now is never promoted.
# Usage: scripts/promote.sh <selected.env> <registry-repo> <index-digest>
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
# shellcheck disable=SC1090
source "${1:?selected.env}"
repo=${2:?repo} digest=${3:?digest}
out=${GITHUB_OUTPUT:-/dev/stdout}
gate=$(dirname "$IMAGE")/gate.json
[[ $(jq -r .promotable "$gate") == true ]] || { echo "gate report is not promotable" >&2; exit 3; }
[[ $(jq -r .index_digest "$gate") == "$digest" ]] || { echo "gate report is for $(jq -r .index_digest "$gate"), not $digest" >&2; exit 3; }

ensure_ctyl
# Only a confirmed missing tag counts as "no current release"; any other
# registry failure stops the promotion.
current=$(ctyl digest --repo "$repo" --tag "$TAG")
current=${current:-none}
if [[ $current == "$digest" ]]; then
  log "$repo:$TAG already points at $digest"
  echo "promoted=true" >> "$out"
  exit 0
fi

candidate_policy=$(jq -r .policy_digest "$gate")
active_policy=$(active_policy_digest)
[[ $candidate_policy == "$active_policy" ]] || {
  echo "$digest was gated under policy $candidate_policy, but the active policy is $active_policy; not promoting" >&2; exit 1; }

gate_report_attested "$repo@$digest" "$gate" || {
  echo "$repo@$digest carries no verified attestation of this run's gate report; not promoting" >&2; exit 1; }

if [[ $current != none ]]; then
  candidate_run=$(jq -r '.run_id // 0' "$gate")
  (( candidate_run > 0 )) || {
    echo "the gate report of $digest names no run (GITHUB_RUN_ID); cannot order it against $current, not promoting" >&2; exit 1; }
  # Reports from before run IDs were recorded count as run 0: every run since is newer.
  current_run=$(verified_gate_reports "$repo@$current" | jq -rs 'map(.run_id // 0) | max // empty')
  [[ -n $current_run ]] || {
    echo "$repo:$TAG points at $current, which carries no verified gate report; cannot tell whether $digest is newer, not promoting" >&2; exit 1; }
  if (( candidate_run <= current_run )); then
    echo "$repo:$TAG points at $current (evaluated by run $current_run), not older than $digest (run $candidate_run); refusing to move the tag backwards" >&2
    exit 1
  fi
fi

# Re-upload the exact manifest bytes under the tag (a copy tool may convert
# the manifest type, which would change the digest).
ctyl retag --repo "$repo" --digest "$digest" --tag "$TAG"
now=$(skopeo inspect "${skopeo_tls[@]}" --raw "docker://$repo:$TAG" | sha256sum | cut -d' ' -f1)
[[ "sha256:$now" == "$digest" ]] || { echo "promotion verification failed: $repo:$TAG is sha256:$now" >&2; exit 1; }
log "promoted $repo:$TAG -> $digest (previously $current)"
echo "promoted=true" >> "$out"
