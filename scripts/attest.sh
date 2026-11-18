#!/usr/bin/env bash
# Attaches signed attestations to a published image: the SPDX SBOM of the
# index and of every platform image, and the security gate report.
# Usage: scripts/attest.sh <selected.env> <registry-repo> <index-digest>
#
# With CTYL_SIGNER_IDENTITY (a keyless certificate identity regexp), an
# attestation that already verifies for that identity is not repeated, so a
# retry after a partial publish adds only what is missing.
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
# shellcheck disable=SC1090
source "${1:?selected.env}"
repo=${2:?repo} digest=${3:?digest}
d=$(dirname "$IMAGE")
gate_type="${GITHUB_SERVER_URL:-https://github.com}/${GITHUB_REPOSITORY:-local/ctyl}/attestations/security-gate/v1"

attest() {
  local type=$1 predicate=$2 subject=$3
  if [[ -n ${CTYL_SIGNER_IDENTITY:-} ]] && cosign verify-attestation "$subject" --type "$type" \
      --certificate-identity-regexp "$CTYL_SIGNER_IDENTITY" --certificate-oidc-issuer https://token.actions.githubusercontent.com >/dev/null 2>&1; then
    log "$subject already carries a verified $type attestation"
    return
  fi
  cosign attest "${cosign_args[@]}" --yes --type "$type" --predicate "$predicate" "$subject"
}

attest "$gate_type" "$d/gate.json" "$repo@$digest"
if [[ $(tar -xOf "$IMAGE" index.json | jq '.manifests | length') -gt 1 ]]; then
  attest spdxjson "$d/sbom/sbom-index.spdx.json" "$repo@$digest"
fi

tar -xOf "$IMAGE" index.json | jq -r '.manifests[] | "\(.platform.architecture) \(.digest)"' | while read -r arch manifest; do
  apk_arch=$([[ $arch == amd64 ]] && echo x86_64 || echo aarch64)
  attest spdxjson "$d/sbom/sbom-$apk_arch.spdx.json" "$repo@$manifest"
done
