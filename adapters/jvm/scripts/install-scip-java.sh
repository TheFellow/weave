#!/bin/sh
set -eu

version=0.13.1
checksum=a694cae143c32c5b6226362fb4bd268a8d13d3cd9b482819b3b0029a9a97b8fe

if [ "$#" -ne 1 ]; then
    echo "usage: $0 DESTINATION_DIRECTORY" >&2
    exit 2
fi

destination=$1
archive="scip-java-v${version}"
url="https://github.com/scip-code/scip-java/releases/download/v${version}/${archive}"
target="$destination/scip-java"

mkdir -p "$destination"
if [ -e "$target" ] || [ -L "$target" ]; then
    echo "refusing to overwrite existing $target" >&2
    exit 1
fi
temporary=$(mktemp -d "${TMPDIR:-/tmp}/weave-scip-java.XXXXXX")
trap 'rm -rf "$temporary"' EXIT HUP INT TERM

curl --fail --location --proto '=https' --tlsv1.2 --output "$temporary/$archive" "$url"

if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "$temporary/$archive" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
    actual=$(shasum -a 256 "$temporary/$archive" | awk '{print $1}')
else
    echo "sha256sum or shasum is required" >&2
    exit 1
fi

if [ "$actual" != "$checksum" ]; then
    echo "checksum mismatch for $archive" >&2
    exit 1
fi

chmod 0755 "$temporary/$archive"
mv "$temporary/$archive" "$target"
echo "installed scip-java v$version to $target" >&2
echo "the upstream launcher requires JDK 17 or newer" >&2
