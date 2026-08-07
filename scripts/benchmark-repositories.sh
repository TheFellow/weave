#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 SOURCE_PARENT OUTPUT_DIRECTORY" >&2
  exit 2
fi

source_parent=$(cd "$1" && pwd)
mkdir -p "$2"
output=$(cd "$2" && pwd)
root=$(git rev-parse --show-toplevel)
work=$(mktemp -d "${TMPDIR:-/tmp}/weave-repository-benchmark.XXXXXX")
trap 'rm -rf "$work"' EXIT
repositories=(go-modular-monolith arch-lint cedar-dotnet fkyeah)
timeout_seconds=${WEAVE_BENCHMARK_TIMEOUT:-300}

for executable in git go python3 jq dotnet; do
  command -v "$executable" >/dev/null || { echo "missing required executable: $executable" >&2; exit 2; }
done

weave="$work/weave"
adapter="$root/adapters/dotnet/src/Weave.Adapter/bin/Release/net9.0/Weave.Adapter"
[[ "$(uname -s)" == MINGW* ]] && adapter="$adapter.exe"
go build -trimpath -o "$weave" ./cmd/weave
dotnet build adapters/dotnet/Weave.Adapter.sln --configuration Release >"$output/adapter-build.log"

{
  echo "timestamp_utc=$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  echo "weave_commit=$(git rev-parse HEAD)"
  echo "os=$(uname -a)"
  echo "cpu=$(sysctl -n machdep.cpu.brand_string 2>/dev/null || grep -m1 'model name' /proc/cpuinfo 2>/dev/null || echo unknown)"
  echo "go=$(go version)"
  echo "dotnet=$(dotnet --version)"
  echo "dotnet_sdks=$(dotnet --list-sdks | tr '\n' ';')"
  echo "timeout_seconds=$timeout_seconds"
  echo "source_parent=$source_parent"
} >"$output/environment.txt"

measure() {
  local repository=$1
  local sample=$2
  shift 2
  python3 "$root/scripts/measure-command.py" \
    --timeout "$timeout_seconds" \
    --stdout "$output/$repository/$sample.stdout" \
    --stderr "$output/$repository/$sample.stderr" \
    --result "$output/$repository/$sample.json" -- "$@"
}

for repository in "${repositories[@]}"; do
  source="$source_parent/$repository"
  [[ -d "$source/.git" ]] || { echo "missing source repository: $source" >&2; exit 2; }
  mkdir -p "$output/$repository"
  clone="$work/$repository"
  git clone --quiet --local --no-hardlinks "$source" "$clone"
  git -C "$clone" checkout --quiet --detach "$(git -C "$source" rev-parse HEAD)"
  git -C "$clone" rev-parse HEAD >"$output/$repository/commit.txt"

  if [[ -f "$clone/go.mod" ]]; then
    if ! (cd "$clone" && GOFLAGS=-mod=readonly go mod download) >"$output/$repository/go-mod-download.log" 2>&1; then
      echo "dependency preparation failed" >"$output/$repository/result.txt"
      continue
    fi
  fi
  if find "$clone" \( -name '*.csproj' -o -name '*.fsproj' \) -print -quit | grep -q .; then
    if ! (cd "$clone" && dotnet restore) >"$output/$repository/restore.log" 2>&1; then
      echo "dependency preparation failed" >"$output/$repository/result.txt"
      continue
    fi
  fi

  (
    cd "$clone"
    export WEAVE_DOTNET_ADAPTER="$adapter"
    if ! measure "$repository" cold-index "$weave" index --json; then
      echo "cold index failed" >"$output/$repository/result.txt"
      continue
    fi
    for run in 1 2 3 4 5; do
      measure "$repository" "warm-query-$run" "$weave" symbols __weave_benchmark_no_match__ --json
    done
    measure "$repository" verify "$weave" verify --json
    measure "$repository" export "$weave" export --json
    git status --porcelain >"$output/$repository/clone-status.txt"
    git rev-parse --git-path weave >"$output/$repository/storage-path.txt"
    database=$(git rev-parse --git-path weave)/index.db
    wc -c <"$database" | tr -d ' ' >"$output/$repository/database-bytes.txt"
    jq '{units:(.facts.units|length),documents:(.facts.documents|length),symbols:(.facts.symbols|length),occurrences:(.facts.occurrences|length),edges:(.facts.edges|length)}' \
      "$output/$repository/export.stdout" >"$output/$repository/facts.json"
  )

  if [[ -n "$(git -C "$source" status --porcelain)" ]]; then
    echo "source repository became dirty: $source" >&2
    exit 1
  fi
done
