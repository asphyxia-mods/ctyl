# Java distributions, versions and runtime contents

## Distribution evaluation (November 2026)

| Distribution | Licence / redistribution | Security cadence | Integrity evidence | glibc/Wolfi fit | Decision |
|---|---|---|---|---|---|
| **Wolfi `openjdk-N`** | GPLv2+CPE; redistributable | Built from OpenJDK GA tags, usually within hours (the Java4Shell x.1 builds were in the index on CPU day, 2026-11-17 ~21:50 UTC) | Signed APKINDEX; package checksums; `release` file `SOURCE` = upstream commit | native | **default** |
| **Amazon Corretto** | GPLv2+CPE; redistributable | Same-day CPU releases (17.0.21.1.2 on 2026-11-17) | OpenPGP-signed archives, sha256 in release notes; `SOURCE` in `corretto/corretto-N`, which merges upstream history | glibc builds; repackaged with melange | **first fallback** |
| **Eclipse Temurin** | GPLv2+CPE; redistributable | Usually within days of a CPU; no Java4Shell fix by 2026-11-18 | OpenPGP-signed, sha256 via the Adoptium API; `SOURCE` + `SOURCE_REPO` | glibc JRE archives | **second fallback** |
| Azul Zulu | GPLv2+CPE (community builds) | fast | sha256 via API; signatures less uniformly published | glibc | not configured (could be added as another `kind`) |
| GraalVM CE | GPLv2+CPE | follows Oracle's GraalVM schedule, lagging OpenJDK CPUs; Java4Shell fix unconfirmed for GraalVM | sha256 | glibc | not used: no demonstrated, reproducible benefit for Minecraft servers that would justify a slower security path |
| Oracle JDK / Oracle GraalVM | NFTC/GFTC terms restrict redistribution and use in public images | fast | signed | glibc | excluded without a separate licence review |

Adding a vendor means adding a `kind` to `internal/vendor` and an entry with
a pinned key fingerprint to `policy/distributions.yaml`.

## Java lines

| Tag | Status | Security support until* | Notes |
|---|---|---|---|
| `java_8` | supported | 2030-12-31 | OpenJDK 8u is maintained (Red Hat-led updates project; Temurin/Corretto roadmaps). Needed for Minecraft ≤ 1.16 and Forge 1.12.2 modpacks. |
| `java_11` | supported | 2027-10-31 | 11u is maintained; few Minecraft versions need it specifically (it runs 1.16.5). |
| `java_17` | supported | 2027-10-31 | Minecraft 1.18–1.20.4. |
| `java_21` | supported | 2029-12-31 | Minecraft 1.20.5–1.21.x. |
| `java_25` | supported | 2031-09-30 | Minecraft 26.x requires Java 25. |
| `java_27` | supported, non-LTS | 2027-03-31 | Runs 26.x. Gets the October and January CPUs only, then becomes unsupported when Java 28 ships. |
| 22, 23, 24, 26 | not built | — | non-LTS and end of life; no maintained source |

\*These are the minimum public commitments of maintained OpenJDK update
projects (`support_evidence` in `policy/java-lines.yaml`). On the date, the
line stops being promoted and is reported `unsupported`. Extending it requires
a reviewed change that cites a current roadmap. A tag is never silently
pointed at another major version.

## JRE, not JDK, and no jlink

The images use Wolfi's `openjdk-N-jre`: the full Java SE runtime module set
plus `jdk.*` runtime modules, including `jdk.unsupported`, `jdk.zipfs`,
`jdk.management`, `jdk.jfr`, `jdk.crypto.*`, `jdk.localedata`,
`jdk.incubator.vector` and `jdk.jdwp.agent` (for an explicitly enabled
debugger). Removing modules with `jlink` would save tens of MB and silently
break mods, so it is not done.

What the JRE lacks compared to the JDK, as an informed trade-off:

* **`jdk.attach`.** Libraries that *self-attach* an agent at runtime
  (`ByteBuddyAgent.install()`, some profilers) cannot. Agents passed with
  `-javaagent:` work (tested). spark's sampler uses its bundled
  async-profiler and works (spark on Fabric is in the test matrix; Paper
  bundles spark).
* **`jdk.compiler`, `jdk.jshell` and the tools** (`javac`, `jcmd`, `jstack`,
  `jmap`, `jhsdb`, `jstatd`…). Debugging tools are intentionally absent;
  heap dumps still work with `-XX:+HeapDumpOnOutOfMemoryError
  -XX:HeapDumpPath=/home/container`.

If a real mod needs `jdk.attach`, the fix is a `-jdk` flavour of that line
(Wolfi `openjdk-N` instead of `-jre`). That is a one-line distribution
entry, not done preemptively.

## What is in the image (Java 21, amd64, 48 packages; 41–49 across lines)

`wolfi-baselayout`, `glibc`, `glibc-locale-posix`, `ld-linux`, `libgcc`,
`ca-certificates(-bundle)`, `java-cacerts` (+ `p11-kit`, `openssl` libcrypto
and `libtasn1` pulled in by the CA tooling), `openjdk-21-jre`,
`openjdk-21-default-jvm`, `java-common-jre`, font stack (`fontconfig`,
`freetype`, `ttf-dejavu`, `libpng`, `brotli`, `bzip2`, `expat`), image
libraries (`libjpeg-turbo`, `giflib`, `lcms2`), X11/ALSA client libraries
(`libx11` & co., `alsa-lib`), `zlib`, `ctyl-launch`. The exact list with
versions and checksums is in each image's SBOM and lock file.

* **Fonts and headless AWT** are kept because servers render maps, images
  and text (map plugins, Dynmap, BlueMap tiles, `ImageIO`). The probe test
  draws text and encodes a PNG on every line. That test is why `java_8`
  explicitly adds `libfontconfig1` and `ttf-dejavu`: Wolfi's 8u JRE does not
  depend on them.
* **X11 and ALSA client libraries** come from the dependencies of Wolfi's JRE
  package. They are never loaded in headless mode. Removing them would mean
  maintaining a repackaged JRE that lags Wolfi, which is worse for priority 3
  (fast security response) than shipping inert libraries.
* **Disabled executables**: `aserver` (ALSA's network sound server),
  `rmiregistry`, `rmid`, `jrunscript`, `jjs`, `jwebserver`, `orbd`,
  `tnameserv`, `servertool`, `policytool` and `jaotc` are present in the
  packages but have mode `0000` in the image, set declaratively with apko
  `paths`. A hygiene check enforces this.
* **Absent**: any shell, BusyBox, package manager (`apk-tools` is not
  installed), `curl`, `wget`, `git`, `jq`, SSH, compilers, debuggers,
  Python/Perl, cron, syslog, init systems, setuid binaries. `ctyl hygiene`
  fails the build if any appears.
