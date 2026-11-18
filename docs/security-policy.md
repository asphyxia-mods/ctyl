# Security policy and release gate

Every candidate image is evaluated **as built** (the apko tarball that would
be published) against the policy in `policy/`. A floating tag such as
`java_21` only ever moves to a digest that passed the gate, was tested,
reproduced bit-for-bit on a second runner, signed, attested and verified.

## Where JDK binaries come from

`policy/distributions.yaml`, in order of preference:

1. **Wolfi** `openjdk-N-jre` packages, resolved and signature-checked by apko
   against the vendored Wolfi key (`keys/wolfi-signing.rsa.pub`) and locked
   (`apko lock`) for every build.
2. **Amazon Corretto** archives, verified against the pinned Corretto OpenPGP
   key `6DC3 636D AE53 4049 C8B9 4623 A122 542A B04F 24E3`.
3. **Eclipse Temurin** JRE archives, verified against the pinned Adoptium key
   `3B04 D753 C905 0D9A 5D34 3F39 843C 48A5 65F8 F04B` and Adoptium's sha256.

Vendor archives are repackaged with melange (`ctyl vendor-fetch` renders the
recipe; the sha256 is re-checked inside the build sandbox). The JDK modules
are kept, and only command-line tools other than `java`, `keytool` and `jfr`
are removed. A melange test then requires every shared object in the JDK to
resolve against Wolfi.

Vendor builds are used **only** when the Wolfi candidate fails the gate. With
`--prefer-fresh`, they are also used when Wolfi is behind the newest upstream
GA and the vendor build is not. Unverified mirrors, SDKMAN, Docker Hub tags
and downgrades are never used. Oracle JDK and Oracle GraalVM are excluded for
licensing reasons (see `java-distributions.md`).

## Evidence, in order of strength

A version string is never accepted as proof on its own: vendors renumber
builds and backport fixes. For each advisory that applies to the line, the
gate looks for evidence in this order:

1. **Reviewed manual verification** (`kind: verify` override) for this exact
   distribution and `JAVA_RUNTIME_VERSION`.
2. **Source ancestry.** The JDK's `release` file records the commit it was
   built from (`SOURCE=".:git:10ebaa9a96d1+"`). The gate resolves that commit
   in the distribution's source repositories (treeless git clones) and checks
   with `git merge-base --is-ancestor` that every fix commit listed in the
   advisory is contained.
   * Contained → **fixed**. A trailing `+` means the distribution applied
     patches on top; this is reported.
   * Not contained, and the source was found in the fix commit's own
     upstream repository → **vulnerable** (confirmed).
   * Not contained in a vendor fork → inconclusive, because the vendor may
     carry the fix under another commit. Fall through to step 3.
3. **Vendor-declared versions.** In the advisory, `fixed_versions` (≥ →
   fixed) and `affected_through` (≤ → vulnerable) give vendor-scheme versions
   with cited sources.
4. Otherwise → **awaiting verification**.

Lines can be `not-affected` with a reason and evidence (for Java4Shell: no
Base64 decode intrinsic exists in jdk8u or jdk11u).

## Statuses

| Status | Meaning |
|---|---|
| `verified` | every applicable advisory is verified fixed or verified not affected |
| `awaiting-verification` | potentially affected, no conclusive evidence yet |
| `vulnerable` | evidence shows a required fix is missing |
| `unsupported` | the line is past `security_support_until` in `java-lines.yaml` |

Findings at `blocking_severities` (default: critical, high) that are
`vulnerable` or `awaiting-verification` make a candidate **not promotable**,
as does an unsupported line or architectures that carry different JDK builds.
A new advisory without fix information therefore blocks promotion until
evidence is added. That is intentional.

The gate also reports whether the build contains the newest upstream GA tag
(`behind_upstream`). Every OpenJDK update release is a Critical Patch Update,
so the planner treats a lag longer than `upstream_lag_fallback_hours` as a
reason to prefer a fresher vendor build, and alerts once the lag exceeds
`publication_target_hours`.

## When no candidate passes

Nothing is published or promoted. The floating tag keeps pointing at the last
published digest, but that line's record on the `release-state` branch is
updated to the current status (e.g. `vulnerable`). `STATUS.md` shows it, and
the "Release pipeline failure" or "Security status alerts" issue is opened or
updated. The watch workflow retries every hour, because a non-verified
published status is itself a rebuild reason.

## Overrides (`policy/overrides/`)

Overrides are reviewed, expiring files. Each must have `reason`, `evidence`
URLs, `approved_by` and `expires`; anything else is a policy load error.
Expired overrides are ignored and reported.

* `baseline`: an emergency baseline. The line's builds must contain
  `require_commit`. Use this when a fix is public before an advisory exists.
* `verify`: vouch for one exact build when automatic evidence is
  unavailable, e.g. a distribution patched the fix in without the upstream
  commit.
* `accept-mitigated`: temporarily allow promotion of an **unfixed** build of
  an advisory that declares a JVM mitigation. The mitigation is then baked
  into the image as `JDK_JAVA_OPTIONS`, the gate checks it is present, and
  the finding stays `vulnerable` with "NOT FIXED" in its evidence. Nothing is
  mitigated without such an override, so no workaround becomes permanent by
  accident.

## Adding an advisory

1. Create `policy/advisories/<CVE>.yaml` with `severity`, `references` and,
   per line, `status: affected` plus `fix_commits` (full 40-hex commits in the
   line's `upstream_repo`, found by comparing the GA tags of the fixing
   release). Add `fixed_versions` / `affected_through` per vendor where they
   have published them. Lines with no fix yet are simply `status: affected`.
2. Merge. The policy digest changes, so the watch workflow rebuilds and
   re-gates every line. Lines that are not fixed are marked and alerted;
   fixed ones are re-promoted (to the same digest if nothing else changed).

## Java4Shell (CVE-2026-114938) as the regression scenario

* Fix: JDK-8393212 "Base64 optimization improvements", a 4-line change to
  `src/hotspot/cpu/x86/stubGenerator_x86_64.cpp`, in `jdk-17.0.21.1-ga`
  (`8ea73158afc9`), `jdk-21.0.13.1-ga` (`8e115fb5c7c5`), `jdk-25.0.5.1-ga`
  (`25bb7d35dcc1`) and `jdk-27.0.1.1-ga` (`564fc7de7eff`).
* State on 2026-11-18. Wolfi published `openjdk-{17,21,25,27}` x.1 builds on
  2026-11-17. Their release files record `10ebaa9a96d1`, `7a7090bd432d`,
  `d748fac9683a` and `03d8fb0da157`, the peeled commits of those GA tags, so
  they are verified by ancestry. (Wolfi's public `wolfi-dev/os` recipe still
  showed 17.0.21.0.3 when the signed 17.0.21.1 binary was already in the
  index. The gate verifies the binary itself, not the recipe.) Corretto
  17.0.21.1.2 is verified by ancestry through `corretto/corretto-17`.
  Temurin 17.0.21+9 is vulnerable (Orca's vendor table, `affected_through`).
* `tests/security/regression.sh` builds Wolfi's real pre-fix package
  (`openjdk-17 17.0.21.0.3-r1`) and requires that the gate rejects it
  (source `b84391a2df6c` lacks `8ea73158afc9`) and that nothing can be
  published. With that package pinned to simulate a lagging Wolfi, the
  pipeline must select verified Corretto 17.0.21.1.2 instead.
* `internal/evaluate` unit tests cover the remaining cases with recorded
  release files: misleading version strings, unknown sources, malicious
  `SOURCE` values, vendor forks, overrides, expiry, mitigations and EOL.
