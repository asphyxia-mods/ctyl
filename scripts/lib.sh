# shellcheck shell=bash
# Shared helpers for build scripts. Source, don't execute.
# Variables set here are used by the scripts that source this file.
# shellcheck disable=SC2034
set -euo pipefail
repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"
export PATH="$repo_root/.tools/bin:$PATH"

# Paths whose content ends up in images. The revision annotation names the
# last commit touching them, so commits that only change docs or advisories
# rebuild to the identical digest instead of publishing a new image.
image_input_paths=(go.mod cmd/ctyl-launch internal/startup internal/launch melange images keys
  policy/java-lines.yaml policy/distributions.yaml)

# Timestamps inside locally built packages and images come from the last
# commit that changed image inputs, so rebuilding unchanged inputs (even at a
# newer HEAD) is reproducible. Needs full history (CI: fetch-depth 0).
if [[ -z ${SOURCE_DATE_EPOCH:-} ]]; then
  SOURCE_DATE_EPOCH=$(git log -1 --format=%ct -- "${image_input_paths[@]}" 2>/dev/null)
  [[ -n $SOURCE_DATE_EPOCH ]] || SOURCE_DATE_EPOCH=$(date -d 2026-01-01 +%s)
fi
export SOURCE_DATE_EPOCH

# Local rehearsals against an http registry on localhost set
# CTYL_INSECURE_REGISTRY=1; cosign then signs with CTYL_COSIGN_KEY instead of
# keyless GitHub OIDC. Neither is used in CI.
skopeo_tls=()
cosign_args=()
if [[ ${CTYL_INSECURE_REGISTRY:-} == 1 ]]; then
  skopeo_tls=(--tls-verify=false)
  cosign_args=(--allow-insecure-registry --allow-http-registry)
fi
if [[ -n ${CTYL_COSIGN_KEY:-} ]]; then
  cosign_args+=(--key "$CTYL_COSIGN_KEY" --signing-config "${CTYL_COSIGN_SIGNING_CONFIG:?offline signing config}")
fi

log() { printf '\033[1m==> %s\033[0m\n' "$*" >&2; }

ensure_ctyl() {
  go build -trimpath -o "$repo_root/.tools/bin/ctyl" ./cmd/ctyl
}

ensure_local_key() {
  mkdir -p build/packages
  if [[ ! -f build/packages/ctyl-local.rsa ]]; then
    (cd build/packages && melange keygen ctyl-local.rsa >/dev/null 2>&1)
  fi
}

melange_build() {
  local recipe=$1 source_dir=$2 archs=$3
  ensure_local_key
  melange build "$recipe" \
    --source-dir "$source_dir" \
    --arch "$archs" \
    --signing-key build/packages/ctyl-local.rsa \
    --out-dir build/packages \
    --cache-dir .cache/melange \
    --build-date "$(date -u -d "@$SOURCE_DATE_EPOCH" +%Y-%m-%dT%H:%M:%SZ)" \
    --namespace ctyl \
    --license Apache-2.0 \
    --git-commit "$(revision)" \
    --git-repo-url "${CTYL_SOURCE_URL:-https://github.com/asphyxia-mods/ctyl}" \
    --runner "${MELANGE_RUNNER:-bubblewrap}"
  # `melange build` does not run the recipe's test block; run it against the
  # freshly built package from the local repository.
  melange test "$recipe" \
    --arch "$archs" \
    --repository-append "$repo_root/build/packages" \
    --keyring-append "$repo_root/build/packages/ctyl-local.rsa.pub" \
    --source-dir "$source_dir" \
    --runner "${MELANGE_RUNNER:-bubblewrap}"
}

revision() {
  local rev
  rev=$(git log -1 --format=%H -- "${image_input_paths[@]}" 2>/dev/null)
  [[ -n $rev ]] || rev=unknown
  if [[ $rev != unknown ]] && [[ -n $(git status --porcelain --untracked-files=no 2>/dev/null) ]]; then
    rev="$rev-dirty"
  fi
  echo "$rev"
}
