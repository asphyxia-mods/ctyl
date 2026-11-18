#!/usr/bin/env bash
# Builds, inspects and gates one Java line. Distributions are tried in policy
# order (Wolfi first, then verified vendor archives); the first candidate that
# passes hygiene and the security gate is selected.
#
# Usage: scripts/build-line.sh <major> [--prefer-fresh] [--only DIST] [--archs LIST]
#
# Outputs build/java_<major>/selected.env describing the selected candidate.
# Exit status: 0 selected, 3 no promotable candidate, other: build failure.
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

major=${1:?usage: build-line.sh <major> [--prefer-fresh] [--only DIST] [--archs LIST]}
shift
prefer_fresh=0 only="" archs=x86_64,aarch64
while [[ $# -gt 0 ]]; do
  case $1 in
    --prefer-fresh) prefer_fresh=1 ;;
    --only) only=$2; shift ;;
    --archs) archs=$2; shift ;;
    *) echo "unknown argument $1" >&2; exit 2 ;;
  esac
  shift
done

ensure_ctyl
tag=$(ctyl lines --all | jq -r --argjson m "$major" '.[] | select(.major == $m) | .tag')
[[ -n $tag ]] || { echo "Java $major is not a configured line" >&2; exit 2; }
out=build/$tag
rm -rf "$out"; mkdir -p "$out"

scripts/build-launcher.sh "$archs"

candidates=$(ctyl candidates)
[[ -n $only ]] && candidates=$only

revision=$(revision)
default_dist=$(echo "$candidates" | head -1)
selected="" default_inputs="" fallback_selected=""
summary=${GITHUB_STEP_SUMMARY:-$out/summary.md}

for dist in $candidates; do
  d=$out/$dist
  mkdir -p "$d/sbom"
  vendor_json=""
  render_args=()
  if [[ $dist != wolfi ]]; then
    log "[$tag] fetching and verifying $dist archives"
    if ! ctyl vendor-fetch --line "$major" --distribution "$dist" --archs "$archs" --out "$d/vendor" 2>&1 | tee "$d/vendor-fetch.log" >&2; then
      log "[$tag] $dist: vendor fetch/verification failed; skipping candidate"
      continue
    fi
    [[ -f $d/vendor/vendor.json ]] || continue
    vendor_json=$d/vendor/vendor.json
    log "[$tag] repackaging $dist with melange"
    melange_build "$d/vendor/melange.yaml" "$d/vendor" "$archs"
    render_args=(--vendor-package "ctyl-jdk-$dist-$major")
  fi

  log "[$tag] $dist: render, lock, build"
  # CTYL_WOLFI_CONSTRAINTS_<major> pins Wolfi package versions (emergency
  # rollback of a broken package; regression tests simulate a lagging Wolfi).
  pins_var="CTYL_WOLFI_CONSTRAINTS_$major"
  if [[ $dist == wolfi && -n ${!pins_var:-} ]]; then
    log "[$tag] wolfi: applying package constraints: ${!pins_var}"
    render_args+=(--package-constraints "${!pins_var}")
  fi
  ctyl render --line "$major" --distribution "$dist" "${render_args[@]}" --out "$d/apko.yaml"
  apko lock "$d/apko.yaml" --arch "$archs" --output "$d/apko.lock.json" 2> "$d/lock.log" || { cat "$d/lock.log" >&2; exit 1; }
  inputs=$(ctyl inputs-digest --config "$d/apko.yaml" --lock "$d/apko.lock.json" ${vendor_json:+--vendor "$vendor_json"})
  [[ $dist == "$default_dist" ]] && default_inputs=$inputs

  annotations=(
    "org.opencontainers.image.title:ctyl $tag"
    "org.opencontainers.image.description:Shell-free Minecraft Java $major runtime ($dist)"
    "org.opencontainers.image.licenses:Apache-2.0 AND GPL-2.0-with-classpath-exception"
    "org.opencontainers.image.revision:$revision"
    "dev.ctyl.java.major:$major"
    "dev.ctyl.java.distribution:$dist"
    "dev.ctyl.inputs-digest:$inputs"
  )
  [[ -n ${CTYL_SOURCE_URL:-} ]] && annotations+=("org.opencontainers.image.source:$CTYL_SOURCE_URL")
  printf '%s\n' "${annotations[@]}" > "$d/annotations.txt"
  ann_args=()
  for a in "${annotations[@]}"; do ann_args+=(--annotations "$a"); done

  apko build "$d/apko.yaml" "ctyl:$tag" "$d/image.tar" \
    --lockfile "$d/apko.lock.json" --arch "$archs" --sbom-path "$d/sbom" --sbom-formats spdx --vcs=false \
    "${ann_args[@]}" 2> "$d/build.log" || { tail -30 "$d/build.log" >&2; exit 1; }

  log "[$tag] $dist: hygiene"
  ctyl hygiene --image "$d/image.tar" --line "$major" --distribution "$dist" | tee "$d/hygiene.txt"

  log "[$tag] $dist: security gate"
  set +e
  ctyl gate --image "$d/image.tar" --line "$major" --distribution "$dist" --out "$d/gate.json" --summary "$summary" \
    --run-id "${GITHUB_RUN_ID:-0}"
  rc=$?
  set -e
  echo "$inputs" > "$d/inputs-digest"
  case $rc in
    0)
      if [[ $prefer_fresh == 1 && $(jq -r .behind_upstream "$d/gate.json") == true ]]; then
        log "[$tag] $dist passes the gate but is behind upstream; trying fresher candidates"
        [[ -z $fallback_selected ]] && fallback_selected=$dist
        continue
      fi
      selected=$dist
      break ;;
    3) log "[$tag] $dist rejected by the security gate" ;;
    *) exit "$rc" ;;
  esac
done

[[ -z $selected ]] && selected=$fallback_selected
if [[ -z $selected ]]; then
  log "[$tag] no candidate passed the security gate"
  printf 'SELECTED=\nTAG=%s\nDEFAULT_CANDIDATE_INPUTS=%s\n' "$tag" "$default_inputs" > "$out/selected.env"
  exit 3
fi

d=$out/$selected
{
  echo "SELECTED=$selected"
  echo "TAG=$tag"
  echo "MAJOR=$major"
  echo "IMAGE=$d/image.tar"
  echo "CONFIG=$d/apko.yaml"
  echo "ANNOTATIONS=$d/annotations.txt"
  echo "ARCHS=$archs"
  echo "LOCK=$d/apko.lock.json"
  echo "INPUTS_DIGEST=$(cat "$d/inputs-digest")"
  echo "INDEX_DIGEST=$(jq -r .index_digest "$d/gate.json")"
  echo "REVISION=$revision"
  [[ $selected != "$default_dist" ]] && echo "DEFAULT_CANDIDATE_INPUTS=$default_inputs"
  true
} > "$out/selected.env"
log "[$tag] selected $selected ($(jq -r .index_digest "$d/gate.json"))"
