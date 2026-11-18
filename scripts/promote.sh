#!/usr/bin/env bash
# Moves the floating tag (e.g. java_21) to a published, signed, attested and
# verified digest. Only candidates that passed the security gate reach this
# point; the copy preserves the digest and is verified afterwards.
# Usage: scripts/promote.sh <selected.env> <registry-repo> <index-digest>
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
# shellcheck disable=SC1090
source "${1:?selected.env}"
repo=${2:?repo} digest=${3:?digest}
out=${GITHUB_OUTPUT:-/dev/stdout}
[[ $(jq -r .promotable "$(dirname "$IMAGE")/gate.json") == true ]] || { echo "gate report is not promotable" >&2; exit 3; }

current=none
if raw=$(skopeo inspect "${skopeo_tls[@]}" --raw "docker://$repo:$TAG" 2>/dev/null); then
  current=sha256:$(printf '%s' "$raw" | sha256sum | cut -d' ' -f1)
fi
if [[ $current == "$digest" ]]; then
  log "$repo:$TAG already points at $digest"
  echo "promoted=true" >> "$out"
  exit 0
fi
skopeo copy "${skopeo_tls[@]/--tls-verify/--src-tls-verify}" "${skopeo_tls[@]/--tls-verify/--dest-tls-verify}" --all --preserve-digests "docker://$repo@$digest" "docker://$repo:$TAG"
now=$(skopeo inspect "${skopeo_tls[@]}" --raw "docker://$repo:$TAG" | sha256sum | cut -d' ' -f1)
[[ "sha256:$now" == "$digest" ]] || { echo "promotion verification failed: $repo:$TAG is sha256:$now" >&2; exit 1; }
log "promoted $repo:$TAG -> $digest (previously $current)"
echo "promoted=true" >> "$out"
