#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 VERSION OUTPUT_DIRECTORY" >&2
  exit 2
fi

version=$1
output=$2
root=$(git rev-parse --show-toplevel)
project="$root/adapters/dotnet/src/Weave.Adapter/Weave.Adapter.csproj"
protocol=weave.adapter/v0
rids=${WEAVE_DOTNET_RIDS:-linux-x64 linux-arm64 osx-x64 osx-arm64 win-x64 win-arm64}

case "$version" in
  ''|*[!0-9A-Za-z.+-]*)
    echo "invalid package version: $version" >&2
    exit 2
    ;;
esac

mkdir -p "$output"
output=$(cd "$output" && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/weave-dotnet-package.XXXXXX")
trap 'rm -rf "$work"' EXIT

dotnet restore "$project" --locked-mode -p:WeaveReleaseRestore=true

dotnet pack "$project" --no-restore --configuration Release --output "$output" \
  -p:PackageVersion="$version" -p:Version="$version"

for rid in $rids; do
  publish="$work/$rid/publish"
  dotnet publish "$project" --no-restore --configuration Release --runtime "$rid" \
    --self-contained false --output "$publish" -p:Version="$version" \
    -p:PublishSingleFile=true -p:DebugType=None -p:DebugSymbols=false

  built="$publish/Weave.Adapter"
  target="$publish/weave-dotnet"
  if [[ "$rid" == win-* ]]; then
    built="$built.exe"
    target="$target.exe"
  fi
  mv "$built" "$target"

  cp "$root/adapters/dotnet/DISTRIBUTION.md" "$publish/README.md"
  cp "$root/LICENSE" "$publish/LICENSE"
  archive="$output/weave-dotnet_${version}_${rid}"
  if [[ "$rid" == win-* ]]; then
    if command -v zip >/dev/null 2>&1; then
      (cd "$publish" && zip -q -r "$archive.zip" .)
    elif command -v powershell.exe >/dev/null 2>&1 && command -v cygpath >/dev/null 2>&1; then
      WEAVE_PUBLISH=$(cygpath -w "$publish") \
        WEAVE_ARCHIVE=$(cygpath -w "$archive.zip") \
        powershell.exe -NoProfile -NonInteractive -Command \
          'Compress-Archive -Path (Join-Path $env:WEAVE_PUBLISH "*") -DestinationPath $env:WEAVE_ARCHIVE'
    else
      echo "cannot create $archive.zip: install zip (or run from Windows with PowerShell)" >&2
      exit 1
    fi
  else
    tar -C "$publish" -czf "$archive.tar.gz" .
  fi
done

# A host-RID dry run can verify the executable protocol without indexing.
if [[ -n "${WEAVE_DOTNET_VERIFY_RID:-}" ]]; then
  executable="$work/$WEAVE_DOTNET_VERIFY_RID/publish/weave-dotnet"
  [[ "$WEAVE_DOTNET_VERIFY_RID" == win-* ]] && executable="$executable.exe"
  description=$($executable describe --protocol "$protocol")
  grep -Fq '"protocols":["weave.adapter/v0"]' <<<"$description"
  grep -Fq '"version":"'"$version"'"' <<<"$description"
fi
