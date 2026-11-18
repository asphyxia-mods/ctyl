#!/usr/bin/env bash
# Installs the pinned toolchain into .tools/ after verifying sha256 pins.
# Usage: scripts/install-tools.sh [tool...]   (default: all)
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
# shellcheck source=tool-versions.env
source "$repo_root/scripts/tool-versions.env"

dest=${CTYL_TOOLS_DIR:-$repo_root/.tools}
mkdir -p "$dest/bin" "$dest/dl"

case "$(uname -m)" in
  x86_64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "unsupported host architecture: $(uname -m)" >&2; exit 1 ;;
esac

fetch_verified() {
  local url=$1 expected=$2 out=$3
  if [[ ! -f $out ]] || ! echo "$expected  $out" | sha256sum -c --status; then
    curl -sSfL --retry 3 -o "$out.partial" "$url"
    echo "$expected  $out.partial" | sha256sum -c --status || {
      echo "checksum mismatch for $url" >&2; rm -f "$out.partial"; exit 1; }
    mv "$out.partial" "$out"
  fi
}

sha_for() { local var="${1}_SHA256_${arch}"; echo "${!var}"; }

install_go() {
  local f="$dest/dl/go$GO_VERSION.$arch.tar.gz"
  fetch_verified "https://go.dev/dl/go$GO_VERSION.linux-$arch.tar.gz" "$(sha_for GO)" "$f"
  rm -rf "$dest/go" && tar -C "$dest" -xzf "$f"
  ln -sf ../go/bin/go "$dest/bin/go"; ln -sf ../go/bin/gofmt "$dest/bin/gofmt"
}

install_tarball_tool() {
  local name=$1 version=$2 url=$3 member=$4
  local f="$dest/dl/$name-$version.$arch.tar.gz"
  fetch_verified "$url" "$(sha_for "${name^^}")" "$f"
  tar -C "$dest/bin" -xzf "$f" "$member"
  [[ $member == "$name" ]] || mv "$dest/bin/$member" "$dest/bin/$name"
  chmod 0755 "$dest/bin/$name"
}

install_apko() {
  install_tarball_tool apko "$APKO_VERSION" \
    "https://github.com/chainguard-dev/apko/releases/download/v$APKO_VERSION/apko_${APKO_VERSION}_linux_$arch.tar.gz" \
    "apko_${APKO_VERSION}_linux_$arch/apko"
  rmdir "$dest/bin/apko_${APKO_VERSION}_linux_$arch" 2>/dev/null || true
}

install_melange() {
  install_tarball_tool melange "$MELANGE_VERSION" \
    "https://github.com/chainguard-dev/melange/releases/download/v$MELANGE_VERSION/melange_${MELANGE_VERSION}_linux_$arch.tar.gz" \
    "melange_${MELANGE_VERSION}_linux_$arch/melange"
  rmdir "$dest/bin/melange_${MELANGE_VERSION}_linux_$arch" 2>/dev/null || true
}

install_grype() {
  install_tarball_tool grype "$GRYPE_VERSION" \
    "https://github.com/anchore/grype/releases/download/v$GRYPE_VERSION/grype_${GRYPE_VERSION}_linux_$arch.tar.gz" \
    grype
}

install_cosign() {
  fetch_verified "https://github.com/sigstore/cosign/releases/download/v$COSIGN_VERSION/cosign-linux-$arch" \
    "$(sha_for COSIGN)" "$dest/bin/cosign"
  chmod 0755 "$dest/bin/cosign"
}

tools=("$@")
[[ ${#tools[@]} -gt 0 ]] || tools=(go apko melange cosign grype)
for t in "${tools[@]}"; do "install_$t"; done
echo "installed into $dest/bin: ${tools[*]}"
