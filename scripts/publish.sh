#!/usr/bin/env bash
# Publishes a gated candidate. The image is rebuilt on this runner from the
# same locked inputs (launcher and vendor packages are rebuilt with melange
# too) and is pushed only if the rebuilt index digest equals the digest that
# was tested and gated. This doubles as the reproducibility check.
#
# Usage: scripts/publish.sh <selected.env> <registry-repo>
# Writes digest=, tags=, already_published= to $GITHUB_OUTPUT when set.
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
env_file=${1:?selected.env}
repo=${2:?registry repository, e.g. ghcr.io/owner/ctyl}
# shellcheck disable=SC1090
source "$env_file"
[[ -n ${SELECTED:-} ]] || { echo "no promotable candidate to publish" >&2; exit 3; }
: "${ARCHS:?selected.env lacks ARCHS}" "${TAG:?}" "${IMAGE:?}" "${LOCK:?}" "${CONFIG:?}" "${ANNOTATIONS:?}" "${INDEX_DIGEST:?}"
out=${GITHUB_OUTPUT:-/dev/stdout}
d=$(dirname "$IMAGE")

log "verifying package reproducibility"
tested_pkgs=$(mktemp -d)
cp -a build/packages/. "$tested_pkgs/"
rm -f build/packages/*/APKINDEX.tar.gz build/packages/*/*.apk build/packages/ctyl-launch.inputs
scripts/build-launcher.sh "$ARCHS"
if [[ $SELECTED != wolfi ]]; then
  melange_build "$d/vendor/melange.yaml" "$d/vendor" "$ARCHS"
fi
# Exactly the locally built packages the image was assembled from.
mapfile -t local_pkgs < <(jq -r '.contents.packages[].url | select(startswith("https://") | not) | sub("^\\./build/packages/"; "")' "$LOCK")
[[ ${#local_pkgs[@]} -gt 0 ]] || { echo "lock lists no locally built packages" >&2; exit 1; }
for rel in "${local_pkgs[@]}"; do
  if ! cmp -s "$tested_pkgs/$rel" "build/packages/$rel"; then
    echo "package $rel is not reproducible: $(sha256sum "$tested_pkgs/$rel" | cut -c1-16) vs $(sha256sum "build/packages/$rel" 2>/dev/null | cut -c1-16)" >&2
    exit 1
  fi
  log "reproduced $rel"
done

short=${INDEX_DIGEST#sha256:}
short=${short:0:12}
version=$(jq -r '.architectures[0].distribution_version' "$d/gate.json")
immutable="$TAG-$SELECTED-${version//[^A-Za-z0-9_.-]/_}-$short"

if current=$(skopeo inspect "${skopeo_tls[@]}" --raw "docker://$repo:$immutable" 2>/dev/null | sha256sum | cut -d' ' -f1) && [[ "sha256:$current" == "$INDEX_DIGEST" ]]; then
  log "$repo:$immutable already holds $INDEX_DIGEST"
  { echo "digest=$INDEX_DIGEST"; echo "tags=$immutable"; echo "already_published=true"; } >> "$out"
  exit 0
fi

ann_args=()
while IFS= read -r a; do ann_args+=(--annotations "$a"); done < "$ANNOTATIONS"
mkdir -p "$d/publish-sbom"
log "rebuilding and publishing $repo:$immutable"
apko publish "$CONFIG" "$repo:$immutable" --lockfile "$LOCK" --arch "$ARCHS" \
  --sbom-path "$d/publish-sbom" "${ann_args[@]}" --image-refs "$d/image-refs.txt" > "$d/publish.log" 2>&1 || {
  tail -30 "$d/publish.log" >&2; exit 1; }
# What the immutable tag now points at: the index (or, for a single
# architecture, the platform manifest). --image-refs also lists every
# platform manifest, so it is not used to pick the digest.
published="sha256:$(skopeo inspect "${skopeo_tls[@]}" --raw "docker://$repo:$immutable" | sha256sum | cut -d' ' -f1)"
if [[ $published != "$INDEX_DIGEST" ]]; then
  echo "NOT REPRODUCIBLE: $repo:$immutable is $published but the tested image is $INDEX_DIGEST; refusing to sign or promote" >&2
  exit 1
fi
log "published $repo@$published (reproduced the tested digest)"
{ echo "digest=$published"; echo "tags=$immutable"; echo "already_published=false"; } >> "$out"
