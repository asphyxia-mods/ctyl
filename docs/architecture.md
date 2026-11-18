# Architecture

```
                 ┌──────────────── discovery (watch.yml, hourly + daily + push + dispatch) ─────────────────┐
                 │  apko lock (fresh Wolfi index, no cache)   git ls-remote/fetch OpenJDK GA tags            │
                 │  release-state/state/java_N.json           registry digest cross-check                    │
                 │                     └──────────── ctyl plan ────────────┘ → lines to rebuild + alerts     │
                 └──────────────────────────────────────────────────────────────────────────────────────────┘
                                                     │
┌──────────────────────────────── pipeline.yml (per line) ───────────────────────────────────────────────────┐
│ build:   melange ctyl-launch ─┐                                                                             │
│          for dist in wolfi, corretto, temurin:                                                              │
│            [vendor-fetch: download + OpenPGP verify + melange repackage + ld.so check]                      │
│            ctyl render → apko lock → apko build (tarball, SBOMs) → ctyl hygiene → ctyl gate ─ pass? → select│
│          grype (fixable criticals block)                                                                     │
│ test:    amd64 + arm64 runners: image tests under Wings/Calagopus settings, real Minecraft servers          │
│ publish: rebuild packages (byte-identical?) → apko publish (digest == tested?) → cosign sign →             │
│          attest SBOMs + gate report + SLSA provenance → verify → promote floating tag                       │
│ record:  release-state branch (state, latency, STATUS.md)   alert: issue on any failure                     │
└────────────────────────────────────────────────────────────────────────────────────────────────────────────┘
                                                     │
                        ctyl-deploy (operator): status → prefetch → graceful stop → start → verify → rollback
```

## Components

| Concern | Code | Notes |
|---|---|---|
| Runtime launcher | `cmd/ctyl-launch`, `internal/startup`, `internal/launch` | standard library only; static; packaged by `melange/ctyl-launch.yaml` |
| Image definition | `images/runtime-base.apko.yaml` + `internal/render` | one shared base; per-line configs are rendered from `policy/java-lines.yaml` |
| Java release discovery and verification | `internal/gitcache`, `internal/evaluate`, `internal/vendor` | upstream GA tags and source-commit ancestry; vendor archive signatures |
| Wolfi resolution | `apko lock` in `watch.yml` and `scripts/build-line.sh` | the lock is the traceable record of every package |
| Security policy | `policy/`, `internal/policy` | lines, distributions, advisories, overrides, gate settings |
| Gate and hygiene | `cmd/ctyl gate`, `cmd/ctyl hygiene`, `internal/ociimage` | read the built tarball directly; no container runtime needed |
| Orchestration | `scripts/*.sh`, `.github/workflows/*` | build → test → publish → record |
| Tests | `internal/**/*_test.go`, `tests/imagetest`, `tests/minecraft`, `tests/security` | `tests/harness` speaks the Docker API like Wings |
| Deployment | `cmd/ctyl-deploy`, `internal/deploy` | Pterodactyl and Calagopus client APIs; Docker on the node |

## Design decisions

* **Wolfi + apko, no Dockerfiles.** The image is a declarative package set.
  Nothing executes at build time (no `RUN`, no install scripts), so the
  absence of a shell costs nothing for updates: a new image is a new
  resolution.
* **The gate inspects the artifact, not the inputs.** Version strings and
  recipes can mislead; the gate reads the JDK `release` file inside the
  built tarball and checks its source commit against upstream git.
* **Fallback is automatic but conservative.** Vendor builds are used only
  when Wolfi fails the gate, or with `--prefer-fresh`. They must pass the same
  gate, the same hygiene checks and the same tests.
* **Stateless planning, auditable state.** The planner compares fresh
  resolutions with the `release-state` branch, so every publication and
  status change is a commit there.
* **One launcher, one base, one test suite** for all lines. Line-specific
  data lives only in `policy/java-lines.yaml`.
* **No services.** The runtime contains no daemons, management endpoints or
  telemetry. Release metadata lives in OCI annotations, attestations and the
  state branch.
