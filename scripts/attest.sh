#!/usr/bin/env bash
# Attaches signed attestations to a published image: the SPDX SBOM of the
# index and of every platform image, and the security gate report.
# Usage: scripts/attest.sh <selected.env> <registry-repo> <index-digest>
#
# With a release signer configured (see lib.sh), an attestation that already
# verifies is not repeated, so a retry after a partial publish adds only what
# is missing. A gate report is reused only if it is this run's exact report:
# re-gating an unchanged digest (new policy, new findings) attests anew.
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
# shellcheck disable=SC1090
source "${1:?selected.env}"
repo=${2:?repo} digest=${3:?digest}
d=$(dirname "$IMAGE")

attest_sbom() {
  local predicate=$1 subject=$2
  if signer_configured && cosign verify-attestation "${cosign_verify_args[@]}" "$subject" --type spdxjson >/dev/null 2>&1; then
    log "$subject already carries a verified SBOM attestation"
    return
  fi
  cosign attest "${cosign_args[@]}" --yes --type spdxjson --predicate "$predicate" "$subject"
}

attest_gate_report() {
  local report=$1 subject=$2
  if signer_configured && gate_report_attested "$subject" "$report"; then
    log "$subject already carries a verified attestation of this gate report"
    return
  fi
  cosign attest "${cosign_args[@]}" --yes --type "$gate_type" --predicate "$report" "$subject"
}

attest_gate_report "$d/gate.json" "$repo@$digest"
if [[ $(tar -xOf "$IMAGE" index.json | jq '.manifests | length') -gt 1 ]]; then
  attest_sbom "$d/sbom/sbom-index.spdx.json" "$repo@$digest"
fi

tar -xOf "$IMAGE" index.json | jq -r '.manifests[] | "\(.platform.architecture) \(.digest)"' | while read -r arch manifest; do
  apk_arch=$([[ $arch == amd64 ]] && echo x86_64 || echo aarch64)
  attest_sbom "$d/sbom/sbom-$apk_arch.spdx.json" "$repo@$manifest"
done

if signer_configured && ! gate_report_attested "$repo@$digest" "$d/gate.json"; then
  echo "$repo@$digest does not carry a verified attestation of $d/gate.json" >&2
  exit 1
fi
