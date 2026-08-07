#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: install-scip-clang.sh TARGET_DIRECTORY" >&2
  exit 2
fi

target_directory=$1
version=v0.4.0
system=$(uname -s)
architecture=$(uname -m)

case "$system/$architecture" in
  Linux/x86_64)
    asset=scip-clang-x86_64-linux
    checksum=06fd18c576f979a726c651594644ec4a35db4f471f2160b3f72eb89fa6001784
    ;;
  Darwin/arm64)
    asset=scip-clang-arm64-darwin
    checksum=ff042fbc8a029f09f4b69fc7692e290e21c52923593207ee52d4e7439473ec64
    ;;
  *)
    echo "scip-clang $version has no upstream binary for $system/$architecture" >&2
    exit 1
    ;;
esac

temporary=$(mktemp -d "${TMPDIR:-/tmp}/weave-scip-clang.XXXXXX")
trap 'rm -rf -- "$temporary"' EXIT
download="$temporary/$asset"
curl --fail --location --silent --show-error \
  "https://github.com/sourcegraph/scip-clang/releases/download/$version/$asset" \
  --output "$download"

if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$download" | awk '{print $1}')
else
  actual=$(shasum -a 256 "$download" | awk '{print $1}')
fi
if [[ "$actual" != "$checksum" ]]; then
  echo "scip-clang checksum mismatch: got $actual, want $checksum" >&2
  exit 1
fi

mkdir -p "$target_directory"
install -m 0755 "$download" "$target_directory/scip-clang"
"$target_directory/scip-clang" --version
