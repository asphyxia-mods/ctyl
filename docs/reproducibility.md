# Reproducibility, provenance and verification

## What is reproducible, and how it is checked

| Artifact | Inputs | Check |
|---|---|---|
| `ctyl-launch` apk | git tree, Go toolchain from the locked Wolfi `go-1.27` package, `SOURCE_DATE_EPOCH` = last commit time, `-trimpath -buildvcs=false -buildid=`, `GOPROXY=off` | The publish job rebuilds it on a fresh runner and compares the apk **byte for byte** with the tested one (`scripts/publish.sh`). Local result: two builds gave identical apks (sha256 `a7c38282…`), signature included. |
| vendor JDK apk | verified vendor archive (sha256 pinned in the rendered recipe), melange recipe | rebuilt and compared byte for byte, as above |
| image (per platform manifest and index) | rendered apko config, `apko.lock.json` (every package with URL, version, control and data checksums), local packages, annotations, `SOURCE_DATE_EPOCH` | The publish job runs `apko publish` with the same lock and must produce the **digest that was tested and gated**; otherwise nothing is signed or promoted. Locally: repeated builds gave identical layer digests, and `apko publish` reproduced the tested manifest digest `sha256:f5a0caaa…`. |

This is a genuine reproducibility check: an independent build on a different
machine, from locked inputs, must match bit for bit. It is not a re-run that
merely succeeds. Because the gate and tests ran on the tarball from the first
build, a match also proves that the published bytes are the tested bytes.

## Known nondeterministic or external inputs

* **The local signing key** (`build/packages/ctyl-local.rsa`) is generated
  per pipeline run, and apko copies its public half into
  `/etc/apk/keys/`. It travels with the build artifact so the publish job can
  reproduce the image. A rebuild in a different run uses a new key, so it
  yields a different digest even with identical packages. The key is inert
  in the image: there is no `apk` tool.
* **Package resolution is time-dependent by design.** A new lock is
  resolved hourly; reproducibility holds for a given lock.
* **The Wolfi binaries** are reproduced from Wolfi's signed packages, not
  rebuilt from source. Their source provenance is the `SOURCE` commit check
  and Wolfi's own build attestations.
* **Annotations.** `org.opencontainers.image.revision` is the last commit
  that changed image inputs, not `HEAD`, so commits that touch only docs or
  advisories rebuild to the same digest.

## Provenance and signatures (per published digest)

* **cosign keyless signature**: Fulcio certificate for this repository's
  workflow identity, logged in Rekor.
* **SLSA build provenance**: `actions/attest-build-provenance`, pushed to the
  registry.
* **SPDX SBOM attestations**: one for the index and one for each platform
  manifest. apko generates them from the installed package database.
* **Security gate attestation**: predicate type
  `https://github.com/OWNER/REPO/attestations/security-gate/v1`. It contains
  the full gate report: runtime version, source commit, findings, evidence,
  policy digest and package list per architecture.
* The **release state** (`release-state` branch) records the digest,
  immutable tag, inputs digest, policy digest, status and latency for every
  publication, in git history.

Verify a release:

```sh
IMG=ghcr.io/OWNER/REPO@sha256:...
ID='^https://github.com/OWNER/REPO/.github/workflows/'
cosign verify "$IMG" --certificate-identity-regexp "$ID" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
cosign verify-attestation "$IMG" --type spdxjson --certificate-identity-regexp "$ID" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com | jq -r .payload | base64 -d | jq .predicate.name
cosign verify-attestation "$IMG" --type https://github.com/OWNER/REPO/attestations/security-gate/v1 \
  --certificate-identity-regexp "$ID" --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  | jq -r .payload | base64 -d | jq '.predicate | {status, promotable, architectures: [.architectures[] | {arch, runtime_version, source_commit, findings}]}'
gh attestation verify "oci://$IMG" --repo OWNER/REPO
```

The local rehearsal (`localhost:5000`, key-based signing) verified that the
signature, the SBOM attestation and the gate attestation bind to the
published digest (`subject f5a0caaa…`), and that verifying an unsigned digest
fails.

## Tooling provenance

`scripts/tool-versions.env` pins Go, apko, melange, cosign and grype by
version and sha256. `scripts/install-tools.sh` refuses mismatches. GitHub
Actions are pinned by commit SHA.
