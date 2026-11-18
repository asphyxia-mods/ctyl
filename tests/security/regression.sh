#!/usr/bin/env bash
# Security release-gate regression scenarios, run against real upstream
# artifacts (Wolfi packages, vendor archives, OpenJDK git history):
#
#  1. A Wolfi JDK build without the Java4Shell fix (openjdk-17 17.0.21.0.3-r1,
#     built from jdk-17.0.21.0.3-ga) is rejected by the gate.
#  2. With Wolfi "lagging" (pinned to that build), the pipeline substitutes a
#     verified vendor build (Corretto 17.0.21.1.2) that passes the gate.
#  3. Nothing is published or promoted when no candidate passes.
#
# Usage: tests/security/regression.sh   (needs network; ~3 minutes)
source "$(dirname "${BASH_SOURCE[0]}")/../../scripts/lib.sh"
fail() { echo "REGRESSION FAILURE: $*" >&2; exit 1; }
pass() { echo "PASS: $*"; }

archs=${CTYL_REGRESSION_ARCHS:-x86_64}
vulnerable="openjdk-17-jre=17.0.21.0.3-r1 openjdk-17-default-jvm=17.0.21.0.3-r1"

log "scenario 1+3: vulnerable Wolfi build, no fallback allowed"
set +e
CTYL_WOLFI_CONSTRAINTS_17="$vulnerable" scripts/build-line.sh 17 --only wolfi --archs "$archs" > build/regression-1.log 2>&1
rc=$?
set -e
[[ $rc == 3 ]] || fail "expected the gate to reject the vulnerable build (exit 3), got $rc"
gate=build/java_17/wolfi/gate.json
[[ $(jq -r .status "$gate") == vulnerable ]] || fail "status $(jq -r .status "$gate"), want vulnerable"
[[ $(jq -r .promotable "$gate") == false ]] || fail "vulnerable build marked promotable"
jq -e '.architectures[0].findings[] | select(.advisory | startswith("CVE-2026-114938")) | select(.status == "vulnerable" and .blocking)' "$gate" >/dev/null \
  || fail "Java4Shell finding missing or not blocking"
jq -e '.architectures[0].source_commit | startswith("b84391a2df6c")' "$gate" >/dev/null || fail "unexpected source commit"
pass "vulnerable 17.0.21.0.3 build rejected: $(jq -r '.architectures[0].findings[0].evidence[0]' "$gate")"

grep -q '^SELECTED=$' build/java_17/selected.env || fail "a rejected build was selected"
set +e
GITHUB_OUTPUT=/dev/null scripts/publish.sh build/java_17/selected.env localhost:5000/never-published >/dev/null 2>&1
rc=$?
set -e
[[ $rc == 3 ]] || fail "publish.sh accepted a run without a promotable candidate (exit $rc)"
pass "publication refused when no candidate passed"

log "scenario 2: Wolfi lagging, verified vendor substitution"
CTYL_WOLFI_CONSTRAINTS_17="$vulnerable" scripts/build-line.sh 17 --archs "$archs" > build/regression-2.log 2>&1 \
  || fail "fallback build failed (see build/regression-2.log)"
# shellcheck disable=SC1091
source build/java_17/selected.env
[[ $SELECTED == corretto ]] || fail "selected $SELECTED, want corretto"
gate=build/java_17/corretto/gate.json
[[ $(jq -r .promotable "$gate") == true && $(jq -r .status "$gate") == verified ]] || fail "corretto candidate not verified"
[[ $(jq -r .promotable build/java_17/wolfi/gate.json) == false ]] || fail "wolfi candidate should have been rejected first"
jq -e '.signer_fingerprint? // .archives[0].signer_fingerprint | test("^6DC3636DAE534049C8B94623A122542AB04F24E3$")' build/java_17/corretto/vendor/vendor.json >/dev/null \
  || fail "vendor archive signer not recorded"
pass "Corretto $(jq -r .version build/java_17/corretto/vendor/vendor.json) substituted: $(jq -r '.architectures[0].findings[0].evidence[0]' "$gate")"

log "all security regression scenarios passed"
