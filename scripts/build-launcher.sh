#!/usr/bin/env bash
# Builds the ctyl-launch apk for the requested architectures with melange.
# Usage: scripts/build-launcher.sh [x86_64,aarch64]
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
archs=${1:-x86_64,aarch64}
stamp=build/packages/ctyl-launch.inputs
mapfile -t files < <(find cmd/ctyl-launch internal/startup internal/launch -type f -not -path '*/testdata/fuzz/*' | sort)
sources=$(sha256sum melange/ctyl-launch.yaml go.mod "${files[@]}" | sha256sum | cut -d' ' -f1)
missing=0
for a in ${archs//,/ }; do
  compgen -G "build/packages/$a/ctyl-launch-*.apk" >/dev/null || missing=1
done
if [[ $missing == 0 && -f $stamp && $(cat "$stamp") == "$sources $archs" ]]; then
  log "launcher packages up to date ($archs)"
  exit 0
fi
log "building ctyl-launch for $archs"
# melange copies the source directory into its sandbox: give it exactly the
# launcher's sources, not the repository (build outputs, caches).
src=build/launcher-src
rm -rf "$src" && mkdir -p "$src"
cp go.mod go.sum "$src/"
for f in "${files[@]}"; do
  mkdir -p "$src/$(dirname "$f")" && cp "$f" "$src/$f"
done
melange_build melange/ctyl-launch.yaml "$src" "$archs"
echo "$sources $archs" > "$stamp"
