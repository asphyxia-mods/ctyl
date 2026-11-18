# Emergency security-baseline overrides

Overrides are reviewed, expiring exceptions to the automated gate. Each file
in this directory is one override. They are the only way to:

* raise the baseline for a line before an advisory exists (`kind: baseline`),
* record a manual verification when automatic evidence is unavailable
  (`kind: verify`), or
* temporarily accept an unfixed, mitigated build for a floating tag
  (`kind: accept-mitigated`).

Every override must carry `evidence` (URLs a reviewer can check), `approved_by`
and an `expires` date. Expired overrides are ignored and reported by the gate.

```yaml
# overrides/2026-11-17-java4shell-baseline.yaml
kind: baseline            # baseline | verify | accept-mitigated
advisory: CVE-2026-114938 # baseline: optional label; others: required
line: 17
# baseline: candidate source must contain this commit in the line's upstream repo
require_commit: 8ea73158afc9f2c62f72df68beb8723032bf695e
# verify: the exact build being vouched for
# distribution: temurin
# runtime_version: "17.0.21.1+1"
reason: Oracle CPU 2026-11-17 announced fix before the CVE record was published
evidence:
  - https://github.com/openjdk/jdk17u/commit/8ea73158afc9f2c62f72df68beb8723032bf695e
approved_by: "@maintainer"
expires: 2026-12-31
```
