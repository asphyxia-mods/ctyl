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
# keyless GitHub OIDC, and verifies with its public half
# (CTYL_COSIGN_PUBLIC_KEY, default: the key path with .key replaced by .pub).
# Neither is used in CI, where CTYL_SIGNER_IDENTITY names the release signer.
skopeo_tls=()
cosign_registry_args=()
if [[ ${CTYL_INSECURE_REGISTRY:-} == 1 ]]; then
  skopeo_tls=(--tls-verify=false)
  cosign_registry_args=(--allow-insecure-registry --allow-http-registry)
fi
cosign_args=("${cosign_registry_args[@]}")
cosign_verify_args=()
if [[ -n ${CTYL_SIGNER_IDENTITY:-} ]]; then
  cosign_verify_args=("${cosign_registry_args[@]}" --certificate-identity-regexp "$CTYL_SIGNER_IDENTITY"
    --certificate-oidc-issuer https://token.actions.githubusercontent.com)
fi
if [[ -n ${CTYL_COSIGN_KEY:-} ]]; then
  cosign_args+=(--key "$CTYL_COSIGN_KEY" --signing-config "${CTYL_COSIGN_SIGNING_CONFIG:?offline signing config}")
  [[ -n ${CTYL_SIGNER_IDENTITY:-} ]] || cosign_verify_args=("${cosign_registry_args[@]}"
    --key "${CTYL_COSIGN_PUBLIC_KEY:-${CTYL_COSIGN_KEY%.key}.pub}" --insecure-ignore-tlog=true)
fi
gate_type="${GITHUB_SERVER_URL:-https://github.com}/${GITHUB_REPOSITORY:-local/ctyl}/attestations/security-gate/v1"

signer_configured() { [[ ${#cosign_verify_args[@]} -gt 0 ]]; }

# Prints the predicate of every security-gate attestation on subject that
# verifies for the release signer, one JSON document per line (none when the
# subject carries no such attestation).
verified_gate_reports() {
  local subject=$1 out
  signer_configured || { echo "no release signer configured (CTYL_SIGNER_IDENTITY or CTYL_COSIGN_KEY)" >&2; return 1; }
  out=$(cosign verify-attestation "${cosign_verify_args[@]}" --type "$gate_type" "$subject" 2>/dev/null) || return 0
  jq -c '.payload | @base64d | fromjson | .predicate' <<< "$out"
}

# Succeeds when subject carries a verified gate attestation whose report is
# exactly the given gate.json (same policy, result and evaluation).
gate_report_attested() {
  local subject=$1 report=$2
  verified_gate_reports "$subject" | jq -se --slurpfile report "$report" 'any(. == $report[0])' >/dev/null
}

# Nanoseconds since the epoch, for comparing RFC 3339 timestamps.
epoch_ns() { date -u -d "$1" +%s%N; }

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
  # Packages are built unsigned so that nothing in them (or in the image's
  # package database, which records their size) depends on the per-run key.
  # Only the repository index is signed; apko verifies the index signature
  # and each package's checksum against the index.
  melange build "$recipe" \
    --source-dir "$source_dir" \
    --arch "$archs" \
    --out-dir build/packages \
    --cache-dir .cache/melange \
    --build-date "$(date -u -d "@$SOURCE_DATE_EPOCH" +%Y-%m-%dT%H:%M:%SZ)" \
    --namespace ctyl \
    --license Apache-2.0 \
    --git-commit "$(revision)" \
    --git-repo-url "${CTYL_SOURCE_URL:-https://github.com/asphyxia-mods/ctyl}" \
    --runner "${MELANGE_RUNNER:-bubblewrap}"
  local arch
  for arch in ${archs//,/ }; do
    melange index --arch "$arch" --signing-key build/packages/ctyl-local.rsa \
      -o "build/packages/$arch/APKINDEX.tar.gz" build/packages/"$arch"/*.apk >/dev/null 2>&1
  done
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
