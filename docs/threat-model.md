# Threat model

## Scope and assets

* **Players and server operators**: world data, player data, credentials in
  server configuration (RCON passwords, database credentials, plugin
  tokens).
* **The node**: other servers on the same host, the container runtime,
  Wings/Calagopus and its panel credentials.
* **The supply chain**: what this repository publishes as
  `ghcr.io/<owner>/<repo>:java_N`, and the trust users place in it.

Out of scope, but listed under "residual responsibilities": the host OS and
kernel, Docker/containerd/runc, the panel and its daemon, and the server
software and plugins the operator installs.

## Trust boundaries

```
player client ──(Minecraft protocol)──▶ server JVM (PID 1, uid 988, no caps)
                                         │  reads/writes /home/container (bind mount)
server owner ──(panel: files, console, variables)──▶ same JVM
                                         │
                       container boundary (namespaces, seccomp, cgroups, no-new-privileges)
                                         │
                                 node: Docker + Wings/Calagopus
```

The image is designed so that full code execution **inside** the JVM gives
an attacker nothing beyond what the JVM already has: no shell, no package
manager, no setuid binaries, no capabilities, no writable system paths, and
no tools to stage the next step.

## Threats and mitigations

### Malicious Minecraft clients

*Example: crafted packets exploiting deserialization, chat or Base64 parsing
(Java4Shell is reachable wherever attacker-controlled Base64 meets
`java.util.Base64`).*

* Fast JDK patching: hourly detection, an ancestry-verified gate, and vendor
  fallback when Wolfi lags (`security-policy.md`).
* The JDK is not slimmed in ways that change behaviour. Code-level
  mitigations belong to the server software (Paper, Forge…), which this
  project does not ship.
* After compromise: the attacker has the JVM's privileges, i.e. the server's
  own files. The container limits blast radius (see "Container escape").

### Vulnerable server software

*Example: an outdated Paper build, or a Forge version with a known RCE.*

* Not fixable at the image level: the operator chooses the server version.
  The image never downloads or updates server software.
* No listening services besides the game port. Tests assert that the only
  listening socket is 25565 and that the JVM is the only process. There is no
  JMX, JDWP or management agent unless the operator adds the flags.
  `rmiregistry`, `jwebserver`, `jrunscript` and the alsa `aserver` are
  stripped of their exec bits.

### Compromised plugins or mods

*Example: a malicious jar from a third-party site, or a hijacked update
channel.*

* A plugin runs with full JVM privileges; no image can prevent that. What the
  image removes is the follow-up tooling: there is no `sh` for
  `Runtime.exec("sh -c ...")`, no `curl`/`wget` to fetch payloads, no package
  manager, and no compilers. `-XX:OnError` / `OnOutOfMemoryError` handlers
  that call `/bin/sh` cannot run.
* The root filesystem is read-only under Wings and Calagopus. A plugin cannot
  plant persistent binaries outside `/home/container` (which the owner
  controls anyway) and `/tmp` (tmpfs, discarded on restart).
* `java` resolution ignores relative `PATH` entries, so a dropped
  `/home/container/java` cannot hijack the startup command.
* Outbound network stays open by default because Minecraft needs it
  (authentication at `sessionserver.mojang.com`, skins, update checks,
  integrations). `deployment.md` describes optional egress restriction.

### Container escape

*Example: a kernel or runc vulnerability, or abuse of capabilities and
devices.*

* Runs as non-root (988:988). The launcher refuses real root unless
  overridden.
* Works with `--cap-drop ALL`, `no-new-privileges`, a read-only rootfs,
  `noexec` /tmp, `pids-limit` and `--network none`; these are all tested.
  Neither daemon requires any added capability.
* No setuid or setgid files; a test enforces this.
* The kernel, runc and seccomp/AppArmor profiles are the host's
  responsibility. `deployment.md` lists the recommended daemon settings. The
  image cannot enforce them.

### Malicious startup configuration

*Example: an attacker controls an egg variable or the startup string.*

* No shell evaluation, ever. Variable values are substituted after
  tokenization and cannot add operators, quotes, command substitutions or
  globs. Unquoted values can add **arguments** (shell-compatible field
  splitting), which matters only if variable values are untrusted while the
  JVM is trusted. Pterodactyl's validation rules (e.g.
  `regex:/^([\w\d._-]+)(\.jar)$/`) and `CTYL_STRICT_VARIABLES=1` close that
  gap.
* Argument injection cannot escalate: anyone able to set JVM flags could
  already upload a jar.
* The compatibility idioms run no programs. `cat` is restricted to small
  regular files inside the working directory.

### Supply-chain attacks

| Vector | Control |
|---|---|
| Tampered Wolfi packages or index | apko verifies the APKINDEX signature against the vendored Wolfi key and every package checksum against the index; resolutions are locked and attached to the image as SPDX SBOMs. |
| Tampered vendor archive or mirror | OpenPGP signature against a pinned fingerprint, plus vendor sha256 (Temurin), re-checked inside the melange sandbox. Only the vendor's canonical URLs are used. |
| "Fixed" version that isn't | Source-commit ancestry against upstream git (`security-policy.md`). |
| Build tooling (apko, melange, cosign, grype, Go) | Exact versions pinned with sha256 in `scripts/tool-versions.env`; releases younger than 7 days are avoided. |
| GitHub Actions | Pinned by commit SHA; least-privilege `permissions:` per job; credentials are not persisted in checkouts. |
| Compromised build runner | The publish job rebuilds independently on a fresh runner and must reproduce the tested digest bit-for-bit, or nothing is signed. Keyless signing ties signatures to this repository's workflow identity. |
| Registry tampering or tag swaps | Consumers verify cosign signatures and attestations by digest; `ctyl-deploy` cross-checks the release state against the registry and can require `cosign verify`. |
| Long-lived secrets | None. Registry pushes use the job's `GITHUB_TOKEN`, signing uses GitHub OIDC with Fulcio/Rekor, and the local melange signing key is generated per run. |
| Malicious `SOURCE` field in a JDK `release` file | Accepted only as a 7–40 hex commit id; git is invoked with `--end-of-options`. |

### Residual risks

* Wolfi's signing key, and Wolfi's honesty about `SOURCE` in binaries it
  builds, are trusted. A dirty build (`+`) could in principle revert a fix
  via a patch. Wolfi's patches are public in `wolfi-dev/os`, but the gate
  does not audit them.
* Vendor keys are trusted on first pin. Key rotation requires a reviewed
  change.
* The X11/ALSA client libraries pulled in by Wolfi's JRE are present (inert
  in headless servers) and are part of the CVE scan surface.
* Detection runs at most hourly, and GitHub's scheduler can delay it.
* An image update does not reach a running server until it is restarted
  (`deployment.md`).
