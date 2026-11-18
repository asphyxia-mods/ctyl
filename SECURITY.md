# Security policy

## Reporting a vulnerability

Please report vulnerabilities in this repository's launcher, build pipeline,
release gate or published images privately via **GitHub Security Advisories**
("Report a vulnerability" on the repository's Security tab). Do not open a
public issue.

Vulnerabilities in the JDK, Wolfi packages, Minecraft server software or the
panels belong upstream. You are welcome to tell us as well, so the release
gate can learn about them (`policy/advisories/`).

## What we commit to

* Floating tags only point at images that passed the security gate
  ([docs/security-policy.md](docs/security-policy.md)), were reproduced bit
  for bit, and were signed and attested.
* Known vulnerable or unverified builds are marked as such on the
  `release-state` branch (`STATUS.md`) and are never labelled verified.
* We target detection within about an hour of an upstream release, and
  same-day publication when a verified fix exists. These are engineering
  targets measured in `release-state/state/latency.jsonl`, not guarantees.

## Supported versions

See [docs/java-distributions.md](docs/java-distributions.md#java-lines).
