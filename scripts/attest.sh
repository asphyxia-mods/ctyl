#!/usr/bin/env bash
# Attaches signed attestations to a published image: the SPDX SBOM of the
# index and of every platform image, and the security gate report.
# Usage: scripts/attest.sh <selected.env> <registry-repo> <index-digest>
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
# shellcheck disable=SC1090
source "${1:?selected.env}"
repo=${2:?repo} digest=${3:?digest}
d=$(dirname "$IMAGE")
gate_type="${GITHUB_SERVER_URL:-https://github.com}/${GITHUB_REPOSITORY:-local/ctyl}/attestations/security-gate/v1"

cosign attest "${cosign_args[@]}" --yes --type "$gate_type" --predicate "$d/gate.json" "$repo@$digest"
if [[ $(tar -xOf "$IMAGE" index.json | jq '.manifests | length') -gt 1 ]]; then
  cosign attest "${cosign_args[@]}" --yes --type spdxjson --predicate "$d/sbom/sbom-index.spdx.json" "$repo@$digest"
fi

tar -xOf "$IMAGE" index.json | jq -r '.manifests[] | "\(.platform.architecture) \(.digest)"' | while read -r arch manifest; do
  apk_arch=$([[ $arch == amd64 ]] && echo x86_64 || echo aarch64)
  cosign attest "${cosign_args[@]}" --yes --type spdxjson --predicate "$d/sbom/sbom-$apk_arch.spdx.json" "$repo@$manifest"
done
