# ctyl: hardened Minecraft Java runtimes for Pterodactyl and Calagopus

Minimal, shell-free, signed container images for Minecraft servers. They
drop into the image list of any Pterodactyl or Calagopus egg (or run under
plain Docker) in place of the usual Java "yolks". Everything is built in this
repository from upstream sources: [Wolfi](https://github.com/wolfi-dev/os)
packages assembled with [apko](https://github.com/chainguard-dev/apko), plus
[melange](https://github.com/chainguard-dev/melange) for the launcher. There
is no dependency on third-party yolks, SDKMAN or Docker Hub images.

* **No shell, no package manager, no network tools.** A small static
  launcher expands the panel's startup command without a shell and runs Java
  directly as PID 1.
* **Security status from evidence.** Before an image is published, the
  release gate checks the source commit embedded in the JDK against upstream
  OpenJDK history for every known advisory. Vulnerable builds are never
  promoted. If Wolfi lags behind a fix, a signature-verified Corretto or
  Temurin build is used instead.
* **Fast updates.** Upstream Java releases and Wolfi packages are checked
  hourly, every line is rebuilt and re-tested daily, and emergency releases
  can be dispatched manually.
* **Verifiable.** Each published digest carries a keyless cosign signature,
  SPDX SBOMs, SLSA build provenance and a signed security-gate report. The
  image was rebuilt bit for bit on a second runner before signing.

## Images

Copy an image name into your egg or server settings. Multi-arch:
`linux/amd64` and `linux/arm64`.

<!-- ctyl:status:begin -->
_Generated from the [`release-state`](https://github.com/asphyxia-mods/ctyl/tree/release-state) branch by the release pipeline. Last publication: 2026-11-26 12:41 UTC._

| Image | Java (release date) | Status | Change since previous image | Container built | Image published | Digest |
|---|---|---|---|---|---|---|
| `ghcr.io/asphyxia-mods/ctyl:java_8` | 1.8.0_514 (wolfi 8.514-r0)<br>unknown | ✅ verified | Wolfi dependencies updated; Java runtime/source unchanged | 2026-11-26 12:27 UTC | [2026-11-26 12:40 UTC](https://github.com/asphyxia-mods/ctyl/actions/runs/45819011191) | `sha256:55c3e18f61a2` |
| `ghcr.io/asphyxia-mods/ctyl:java_11` | 11.0.33.1 (wolfi 11.0.33.1-r0)<br>2026-11-17 | ✅ verified | Wolfi dependencies updated; Java runtime/source unchanged | 2026-11-26 12:28 UTC | [2026-11-26 12:40 UTC](https://github.com/asphyxia-mods/ctyl/actions/runs/45819011191) | `sha256:3621619c98f6` |
| `ghcr.io/asphyxia-mods/ctyl:java_17` | 17.0.21.1 (wolfi 17.0.21.1-r1)<br>2026-11-17 | ✅ verified | Wolfi dependencies updated; Java runtime/source unchanged | 2026-11-26 12:27 UTC | [2026-11-26 12:40 UTC](https://github.com/asphyxia-mods/ctyl/actions/runs/45819011191) | `sha256:8646cf1601b8` |
| `ghcr.io/asphyxia-mods/ctyl:java_21` | 21.0.13.1 (wolfi 21.0.13.1-r1)<br>2026-11-17 | ✅ verified | Wolfi dependencies updated; Java runtime/source unchanged | 2026-11-26 12:27 UTC | [2026-11-26 12:40 UTC](https://github.com/asphyxia-mods/ctyl/actions/runs/45819011191) | `sha256:b28acfdd12e8` |
| `ghcr.io/asphyxia-mods/ctyl:java_25` | 25.0.5.1 (wolfi 25.0.5.1-r2)<br>2026-11-17 | ✅ verified | Wolfi dependencies updated; Java runtime/source unchanged | 2026-11-26 12:27 UTC | [2026-11-26 12:40 UTC](https://github.com/asphyxia-mods/ctyl/actions/runs/45819011191) | `sha256:9e71b8d81a09` |
| `ghcr.io/asphyxia-mods/ctyl:java_27` | 27.0.1.1 (wolfi 27.0.1.1-r2)<br>2026-11-17 | ✅ verified | Wolfi dependencies updated; Java runtime/source unchanged | 2026-11-26 12:27 UTC | [2026-11-26 12:41 UTC](https://github.com/asphyxia-mods/ctyl/actions/runs/45819011191) | `sha256:604dc7dca2d1` |

Java release date is JAVA_VERSION_DATE reported by the bundled runtime, not the Wolfi package or container publication date. Unknown dates and unrecorded changes are not inferred from build times. Changes compare published image inputs; a Wolfi package rebuild can keep the same Java source. Identical daily rebuilds keep the original container build and publication times.

<details><summary>Pinned references (exact digests)</summary>

```
ghcr.io/asphyxia-mods/ctyl:java_8@sha256:55c3e18f61a2de3b62380c7ec49d80a580871d6c82ee543e7121b1b2575b9609
ghcr.io/asphyxia-mods/ctyl:java_11@sha256:3621619c98f636cbb7ec3585b0317c6691eaaf68f100f7cefda9fe8bd933627f
ghcr.io/asphyxia-mods/ctyl:java_17@sha256:8646cf1601b810ca23a109d19a9ad648dc7823b771ac16d99e7d74bba040db96
ghcr.io/asphyxia-mods/ctyl:java_21@sha256:b28acfdd12e876c246937bb81bf57e11fbabe8c1aa3f7bbaa433c4adca22b28f
ghcr.io/asphyxia-mods/ctyl:java_25@sha256:9e71b8d81a0909c9ca10bc534e703e7c0818dc43da74cce9f89484967fd385d7
ghcr.io/asphyxia-mods/ctyl:java_27@sha256:604dc7dca2d19d3a51e30ab4587a2c05c0139bb6141e809ddee94930c602829b
```

</details>
<!-- ctyl:status:end -->

Statuses: ✅ verified (every known advisory is verified fixed or not
applicable) · ⏳ awaiting verification · ❌ vulnerable (no fixed build exists
yet; the tag stays on the last published image and an issue is open) ·
⛔ unsupported (past its security support date). Details:
[docs/security-policy.md](docs/security-policy.md).

| Tag | Use for | Security support until |
|---|---|---|
| `java_8` | Minecraft ≤ 1.16.5, Forge 1.12.2 modpacks | 2030-12-31 |
| `java_11` | 1.16.5 | 2027-10-31 |
| `java_17` | 1.18 – 1.20.4 | 2027-10-31 |
| `java_21` | 1.20.5 – 1.21.11 | 2029-12-31 |
| `java_25` | 26.x | 2031-09-30 |
| `java_27` | 26.x (non-LTS) | 2027-03-31 |

Java 22, 23, 24 and 26 are end of life and not built. A tag never moves to a
different Java major version.

## Using them

**Pterodactyl / Calagopus egg:** list the images in the egg's Docker images:

```json
"docker_images": {
  "Java 27": "ghcr.io/asphyxia-mods/ctyl:java_27",
  "Java 25": "ghcr.io/asphyxia-mods/ctyl:java_25",
  "Java 21": "ghcr.io/asphyxia-mods/ctyl:java_21",
  "Java 17": "ghcr.io/asphyxia-mods/ctyl:java_17",
  "Java 11": "ghcr.io/asphyxia-mods/ctyl:java_11",
  "Java 8": "ghcr.io/asphyxia-mods/ctyl:java_8"
}
```

…or set one per server under **Startup → Docker Image** (Pterodactyl's
"custom image" field, or Calagopus's image selector).

Existing startup commands work unchanged: `{{VARIABLES}}`, quoting,
`-jar forge-*.jar` globs and the Forge/NeoForge
`$( [[ ! -f unix_args.txt ]] && printf %s "-jar {{SERVER_JARFILE}}" || printf %s "@unix_args.txt" )`
idiom (evaluated without a shell). Shell operators such as `;`, `|` or `>`
are rejected with a clear message ([docs/launcher.md](docs/launcher.md)).
Install scripts keep running in the egg's installer image.

**Plain Docker:**

```sh
docker run -d --name mc -it -p 25565:25565 -v /srv/mc:/home/container \
  --read-only --tmpfs /tmp:rw,exec,nosuid,size=256m --cap-drop ALL --security-opt no-new-privileges \
  -e 'STARTUP=java -Xms128M -XX:MaxRAMPercentage=95.0 -jar {{SERVER_JARFILE}}' -e SERVER_JARFILE=server.jar \
  ghcr.io/asphyxia-mods/ctyl:java_21
```

**Verify an image** before trusting it:

```sh
cosign verify ghcr.io/asphyxia-mods/ctyl:java_21 \
  --certificate-identity-regexp '^https://github.com/asphyxia-mods/ctyl/.github/workflows/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
gh attestation verify oci://ghcr.io/asphyxia-mods/ctyl:java_21 --repo asphyxia-mods/ctyl
```

**Updating servers:** publishing doesn't restart servers. A new image is
picked up the next time a server starts (Calagopus may reuse a pull from the
last 5 minutes). `ctyl-deploy` finds servers on outdated digests, pre-pulls
the verified one, stops each server gracefully, restarts it, checks the
running digest and rolls back on failure
([docs/deployment.md](docs/deployment.md)).

## What's inside

glibc, the Wolfi OpenJDK JRE (all runtime modules, fonts for headless AWT),
CA certificates and `ctyl-launch`, running as `container` (988:988) in
`/home/container` with a read-only root filesystem. Not included: any shell,
BusyBox, package manager, curl/wget/git, SSH, compilers or debuggers.
Network-service binaries shipped by the JRE packages (`rmiregistry`,
`jwebserver`, ALSA's `aserver`, …) have their exec bits removed.
[docs/java-distributions.md](docs/java-distributions.md) has the full list
and the JRE-vs-JDK trade-off: `jdk.attach` is absent, while `-javaagent`
works.

## How it is tested

Every release runs these on native amd64 and arm64 GitHub runners, and they
were also run locally:

* **Runtime probe on every line, under the exact container settings of
  Pterodactyl Wings and Calagopus Wings:** TLS (valid certificates accepted,
  expired rejected), DNS, fonts and image encoding, crypto, process spawning,
  modules, Unicode file names, time zones, PID 1, no other processes, no
  listening sockets, no capabilities. Also checked: `--cap-drop ALL`,
  `--network none`, noexec `/tmp`, arbitrary UIDs, `-javaagent`, memory
  limits, SIGTERM/SIGINT, console input, root refusal, and the absence of
  any shell.
* **Real servers:** start, console input, `stop`, persistence, a restart
  under the other daemon's settings, and SIGTERM with proof the world was
  saved. The set: Forge 1.12.2 and vanilla 1.16.5 on Java 8; vanilla 1.16.5
  on 11; Paper 1.20.4 and Forge 1.20.1 on 17; Paper 1.21.11, Fabric 1.21.11
  (+ Fabric API, Lithium, spark) and NeoForge 21.1.267 on 21; vanilla, Paper
  and Fabric 26.3 on 25; vanilla and Paper 26.3 on 27 (arm64 runs the vanilla
  and Paper subset). Results:
  [docs/compatibility.md](docs/compatibility.md).
* **Security-gate regression with real artifacts**
  (`tests/security/regression.sh`): Wolfi's pre-Java4Shell-fix
  `openjdk-17 17.0.21.0.3-r1` is rejected, and with Wolfi "lagging",
  verified Corretto 17.0.21.1.2 is substituted.

Not yet exercised: a `ctyl-deploy` rollout against a live panel. Its panel
API code is tested against simulated Pterodactyl and Calagopus APIs.

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

Needs Linux with Docker, bubblewrap (for melange), and qemu-user binfmt for
cross-arch package builds.

## Licence

Apache-2.0 for this repository. The images contain OpenJDK
(GPL-2.0-with-classpath-exception) and other Wolfi packages under their own
licences, listed in each image's SBOM.
