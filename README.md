# ctyl: hardened Minecraft Java runtimes for Pterodactyl and Calagopus

Minimal, shell-free, signed OCI images for running Minecraft servers under
[Pterodactyl](https://pterodactyl.io) Wings and
[Calagopus](https://github.com/calagopus/panel) Wings, or plain Docker. They
replace the Java "yolks" in an egg's image list. Everything is built here
from upstream sources: [Wolfi](https://github.com/wolfi-dev/os) packages
assembled with [apko](https://github.com/chainguard-dev/apko), and
[melange](https://github.com/chainguard-dev/melange) for the parts we build.
Nothing depends on a third-party yolk, SDKMAN catalog or Docker Hub image.

* **No shell, no package manager, no network tools.** The entrypoint is a
  small statically linked launcher that expands the panel's `STARTUP`
  template without a shell and `execve`s Java as PID 1.
* **Security status from evidence, not version strings.** The release gate
  reads the JDK's embedded source commit and checks it against upstream
  OpenJDK history for every known advisory. Vulnerable builds cannot be
  promoted; when Wolfi lags, a signature-verified Corretto or Temurin build
  is substituted automatically.
* **Fast response.** Upstream checks run hourly, with a daily full
  reconciliation, rebuilds when inputs change, and manual emergency dispatch.
* **Verifiable.** Locked package resolutions, SPDX SBOMs, SLSA provenance,
  keyless cosign signatures and a signed gate report for every digest. The
  publish job must reproduce the tested digest bit for bit.

## Images

`ghcr.io/<owner>/<repo>:<tag>`. Floating tags move only to verified
releases. Every release also gets an immutable tag
`java_21-wolfi-21.0.13.1-r0-<digest12>`, and every release can be pulled by
digest.

| Tag | Java | Typical Minecraft | Security support until |
|---|---|---|---|
| `java_8` | OpenJDK 8u | ≤ 1.16.5, Forge 1.12.2 packs | 2030-12-31 |
| `java_11` | 11 | 1.16.5 | 2027-10-31 |
| `java_17` | 17 | 1.18 – 1.20.4 | 2027-10-31 |
| `java_21` | 21 | 1.20.5 – 1.21.11 | 2029-12-31 |
| `java_25` | 25 | 26.x | 2031-09-30 |
| `java_27` | 27 (non-LTS) | 26.x | 2027-03-31 |

Java 22, 23, 24 and 26 are end of life and not built. Status per tag:
`STATUS.md` on the `release-state` branch.

Each image is multi-arch (`linux/amd64`, `linux/arm64`). It contains glibc,
the Wolfi OpenJDK JRE (all runtime modules, fonts for headless AWT), CA
certificates and `ctyl-launch`. It runs as `container` (988:988) in
`/home/container`.
[docs/java-distributions.md](docs/java-distributions.md) has the full content
list and the JRE-vs-JDK trade-off.

## Using them

In the egg (or per server), select the image:

```json
"docker_images": { "Java 21": "ghcr.io/OWNER/REPO:java_21" }
```

Startup commands work unchanged: `{{VARIABLES}}`, quoting, `-jar forge-*.jar`
globs, and the Forge/NeoForge
`$( [[ ! -f unix_args.txt ]] && printf %s "-jar {{SERVER_JARFILE}}" || printf %s "@unix_args.txt" )`
idiom (evaluated without a shell). Shell operators such as `;`, `|` or `>`
are rejected with a clear message ([docs/launcher.md](docs/launcher.md)).
Install scripts keep using the egg's installer image.

Verify before trusting a digest:

```sh
cosign verify ghcr.io/OWNER/REPO@sha256:… \
  --certificate-identity-regexp '^https://github.com/OWNER/REPO/.github/workflows/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Publishing does not restart servers. Wings/Calagopus pick up a new image
only on the next start, and Calagopus caches pulls for 5 minutes.
`ctyl-deploy` finds outdated servers, pre-pulls the verified digest, stops
servers gracefully (never a forced kill), restarts them, verifies the running
digest and rolls back on failure ([docs/deployment.md](docs/deployment.md)).

## Tested

Run in this environment (2026-11-18, amd64), using a harness that creates
containers through the Docker API with the exact settings Pterodactyl Wings
and Calagopus Wings use (read-only rootfs, `/tmp` tmpfs, dropped
capabilities, `no-new-privileges`, TTY + stdin, daemon UID):

* **Runtime probe on every line:** TLS (valid and rejected certificates),
  DNS, fonts and PNG encoding, crypto, `ProcessBuilder`, `Unsafe`, modules,
  Unicode file names, time zones, PID 1, no other processes, no listening
  sockets, no capabilities. Also covered: `--cap-drop ALL`, `--network none`,
  noexec `/tmp`, arbitrary UIDs, `-javaagent`, memory limits, SIGTERM/SIGINT
  and console input, root refusal, and the absence of any shell.
* **Real servers** (start, console input, `stop`, persistence, restart under
  the other daemon's settings, SIGTERM with proof the world was saved):
  Forge 1.12.2 and vanilla 1.16.5 on Java 8; vanilla 1.16.5 on 11; Paper
  1.20.4 and Forge 1.20.1 on 17; Paper 1.21.11, Fabric 1.21.11 (+ Fabric API,
  Lithium, spark) and NeoForge 21.1.267 on 21; vanilla, Paper and Fabric 26.3
  on 25; vanilla and Paper 26.3 on 27. Results are in
  [docs/compatibility.md](docs/compatibility.md).
* **Security gate regression with real artifacts**
  (`tests/security/regression.sh`): Wolfi's pre-Java4Shell-fix
  `openjdk-17 17.0.21.0.3-r1` is rejected; with Wolfi "lagging", verified
  Corretto 17.0.21.1.2 is substituted.
* **Release path rehearsal** against a local registry: independent rebuild
  reproduced the tested digest, then sign, SBOM and gate attestations,
  verification, promotion, and deployment status on simulated Wings
  containers.

**Not yet run:** the GitHub Actions workflows themselves (the repository has
not been pushed), arm64 native runners, keyless signing on GitHub, and a
rollout against a live panel. See
[docs/operations.md](docs/operations.md#one-time-repository-setup).

## Documentation

| | |
|---|---|
| [docs/architecture.md](docs/architecture.md) | components and pipeline |
| [docs/launcher.md](docs/launcher.md) | exact startup parsing and substitution semantics |
| [docs/security-policy.md](docs/security-policy.md) | evidence, statuses, gate, overrides, Java4Shell |
| [docs/threat-model.md](docs/threat-model.md) | clients, server software, plugins, escapes, supply chain |
| [docs/java-distributions.md](docs/java-distributions.md) | vendor evaluation, Java lines, image contents |
| [docs/compatibility.md](docs/compatibility.md) | egg patterns, test results, limitations |
| [docs/deployment.md](docs/deployment.md) | update lifecycle, `ctyl-deploy`, hardening settings |
| [docs/reproducibility.md](docs/reproducibility.md) | reproducibility, provenance, verification |
| [docs/operations.md](docs/operations.md) | setup, emergency releases, latency, retiring lines |

## Repository layout

```
cmd/ctyl-launch   container entrypoint          cmd/ctyl         release tooling (render, gate, plan, …)
cmd/ctyl-deploy   rollout tool                   internal/        implementation packages
images/           shared apko definition         melange/         launcher package recipe
policy/           lines, distributions, advisories, overrides, gate settings
keys/             pinned Wolfi, Corretto, Adoptium keys
scripts/          build, publish, attest, promote, record
tests/            image tests, Minecraft tests, security regression, Docker harness, probe
.github/          watch (discovery) → pipeline (build/test/publish) ← ci (PRs)
```

## Building locally

```sh
scripts/install-tools.sh        # pinned, checksum-verified toolchain into .tools/
make test
make image LINE=21 ARCHS=x86_64 # build/java_21/<dist>/image.tar + gate.json + SBOMs
make image-test LINE=21
```

Needs Linux with Docker, bubblewrap (melange), and qemu-user binfmt for
cross-arch package builds.

## Licence

Apache-2.0 for this repository. The images contain OpenJDK
(GPL-2.0-with-classpath-exception) and other Wolfi packages under their own
licences, listed in each image's SBOM.
