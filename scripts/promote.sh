#!/usr/bin/env bash
# Moves the floating tag (e.g. java_21) to a published, signed, attested and
# verified digest. Only candidates that passed the security gate reach this
# point; the copy preserves the digest and is verified afterwards.
#
# The tag only moves forward: if it points at an image whose latest verified
# gate evaluation is not older than this candidate's (a newer run promoted it
# while this one waited, or this is a retry of an older run), nothing changes
# and the step fails so that no release state is recorded for this run.
# Usage: scripts/promote.sh <selected.env> <registry-repo> <index-digest>
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
# shellcheck disable=SC1090
source "${1:?selected.env}"
repo=${2:?repo} digest=${3:?digest}
out=${GITHUB_OUTPUT:-/dev/stdout}
gate=$(dirname "$IMAGE")/gate.json
[[ $(jq -r .promotable "$gate") == true ]] || { echo "gate report is not promotable" >&2; exit 3; }
[[ $(jq -r .index_digest "$gate") == "$digest" ]] || { echo "gate report is for $(jq -r .index_digest "$gate"), not $digest" >&2; exit 3; }

current=none
if raw=$(skopeo inspect "${skopeo_tls[@]}" --raw "docker://$repo:$TAG" 2>/dev/null); then
  current=sha256:$(printf '%s' "$raw" | sha256sum | cut -d' ' -f1)
fi
if [[ $current == "$digest" ]]; then
  log "$repo:$TAG already points at $digest"
  echo "promoted=true" >> "$out"
  exit 0
fi

gate_report_attested "$repo@$digest" "$gate" || {
  echo "$repo@$digest carries no verified attestation of this run's gate report; not promoting" >&2; exit 1; }

if [[ $current != none ]]; then
  candidate_at=$(jq -r .evaluated_at "$gate")
  current_at=$(verified_gate_reports "$repo@$current" | jq -rs 'map(.evaluated_at) | max // empty')
  [[ -n $current_at ]] || {
    echo "$repo:$TAG points at $current, which carries no verified gate report; cannot tell whether $digest is newer, not promoting" >&2; exit 1; }
  if (( $(epoch_ns "$candidate_at") <= $(epoch_ns "$current_at") )); then
    echo "$repo:$TAG points at $current (gated $current_at), not older than $digest (gated $candidate_at); refusing to move the tag backwards" >&2
    exit 1
  fi
fi

# Re-upload the exact manifest bytes under the tag (a copy tool may convert
# the manifest type, which would change the digest).
ensure_ctyl
ctyl retag --repo "$repo" --digest "$digest" --tag "$TAG"
now=$(skopeo inspect "${skopeo_tls[@]}" --raw "docker://$repo:$TAG" | sha256sum | cut -d' ' -f1)
[[ "sha256:$now" == "$digest" ]] || { echo "promotion verification failed: $repo:$TAG is sha256:$now" >&2; exit 1; }
log "promoted $repo:$TAG -> $digest (previously $current)"
echo "promoted=true" >> "$out"
