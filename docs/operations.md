# Maintainer operations

## One-time repository setup

These steps were not done in this environment; they need the GitHub
repository owner.

1. Push the repository to GitHub. The image name is
   `ghcr.io/<owner>/<repo>`, derived from `github.repository`.
2. **Settings → Actions → General**: set "Workflow permissions" to
   *read repository contents* (jobs request what they need). Allow GitHub
   Actions to create issues (default).
3. Create the `security-release` issue label (optional; alerts fall back to
   no label).
4. After the first publication, make the GHCR package **public** (Packages →
   package settings) so panels can pull anonymously, and link it to the
   repository.
5. Branch protection on `main` as desired. The `release-state` branch is
   written by the workflow's `GITHUB_TOKEN`: allow `github-actions[bot]` to
   push to it, or leave it unprotected.
6. Optional: an external webhook relay can trigger
   `repository_dispatch` with `event_type=upstream-release`
   (`client_payload.lines` = e.g. `"17,21"`).

No other secrets are needed. Registry pushes use `GITHUB_TOKEN` and signing
uses keyless OIDC.

## Routine

* **Hourly** (`watch.yml`): fresh Wolfi resolution and upstream GA check
  for every line. A changed inputs digest, policy digest, non-verified
  status, upstream lag or registry mismatch triggers the pipeline for that
  line.
* **Daily** (04:43 UTC): forced rebuild, re-test and re-gate of every line.
  Unchanged inputs reproduce the same digest, which is then not re-published;
  only the status is refreshed.
* **Push to `main`** touching image inputs, policy or tooling runs the same
  planner.
* Alerts go to two issues: **"Security status alerts"** (updated in place,
  closed automatically when clear) and **"Release pipeline failure"** (one
  comment per failed run).

## Emergency security release

1. If a fix is public but no advisory exists yet: add
   `policy/advisories/<id>.yaml`, or an expiring `baseline` override with the
   fix commit (`policy/overrides/README.md`).
2. Run **Actions → watch → Run workflow** with `lines` set (e.g. `17,21`),
   `force` on, and, if Wolfi lags a vendor, `prefer_fresh` on.
3. Follow the run summary: gate tables per candidate, then tests, publish,
   signature verification and promotion.
4. If no fixed build exists anywhere, the line is marked `vulnerable` and
   alerted. A maintainer may accept a declared JVM mitigation with an
   expiring `accept-mitigated` override. That is a visible, reviewed
   decision, and the status stays `vulnerable`.
5. Notify operators to run `ctyl-deploy` (`deployment.md`).

## Measuring latency

`release-state/state/latency.jsonl` holds one record per publication with
`upstream_latest_ga_date`, `published_at` and `latency_hours`. Example:

```sh
git show origin/release-state:state/latency.jsonl | jq -s 'map(select(.latency_hours)) | group_by(.tag) | map({tag: .[0].tag, worst: (map(.latency_hours) | max), last: .[-1].latency_hours})'
```

The planner raises an alert when an upstream GA is older than
`publication_target_hours` (`policy/gate.yaml`) and still not published.

## Retiring a line

When `security_support_until` passes, the line stops being rebuilt and
promoted, the planner alerts, and the status is `unsupported`. Announce it,
then move the line to `unsupported:` in `java-lines.yaml` with a reason. Do
**not** retag it to another major version.

## Updating the toolchain

Update a version and **all** of its sha256 values in
`scripts/tool-versions.env` together. Prefer releases older than 7 days. Pin
new or updated actions by commit SHA, with the tag in a comment.

## Local development

```sh
scripts/install-tools.sh                 # pinned Go/apko/melange/cosign/grype in .tools/
make test                                # unit tests, fuzz smoke, shellcheck
make image LINE=21 ARCHS=x86_64          # build + hygiene + gate
make image-test LINE=21                  # Wings/Calagopus-profile runtime tests (Docker)
make minecraft LINE=21                   # real servers (downloads ~1 GB, accepts the EULA for test servers)
tests/security/regression.sh             # Java4Shell gate regression with real artifacts
```
