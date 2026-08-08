#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 2 ]]; then
  echo "usage: $0 SOURCE_PARENT OUTPUT_DIRECTORY [REPOSITORY ...]" >&2
  exit 2
fi

source_parent=$(cd "$1" && pwd)
mkdir -p "$2"
output=$(cd "$2" && pwd)
root=$(git rev-parse --show-toplevel)
work=$(mktemp -d "${TMPDIR:-/tmp}/weave-repository-benchmark.XXXXXX")
trap 'rm -rf "$work"' EXIT
repositories=(go-modular-monolith arch-lint cedar-dotnet fkyeah)
if [[ $# -gt 2 ]]; then
  repositories=("${@:3}")
fi
timeout_seconds=${WEAVE_BENCHMARK_TIMEOUT:-300}
failures=0

for repository in "${repositories[@]}"; do
  [[ "$repository" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] || { echo "invalid repository name: $repository" >&2; exit 2; }
done

for executable in git go python3 jq dotnet; do
  command -v "$executable" >/dev/null || { echo "missing required executable: $executable" >&2; exit 2; }
done

mkdir -p "$work/bin"
weave="$work/bin/weave"
adapter_project="$root/adapters/dotnet/src/Weave.Adapter/Weave.Adapter.csproj"
go build -trimpath -o "$weave" ./cmd/weave
dotnet build adapters/dotnet/Weave.Adapter.sln --configuration Release >"$output/adapter-build.log"
adapter_directory=$(dotnet msbuild "$adapter_project" -nologo -getProperty:TargetDir -property:Configuration=Release | tail -n 1 | tr -d '\r')
dotnet_root=$(dotnet msbuild "$adapter_project" -nologo -getProperty:NetCoreRoot -property:Configuration=Release | tail -n 1 | tr -d '\r')
adapter="$adapter_directory/Weave.Adapter"
[[ "$(uname -s)" == MINGW* ]] && adapter="$adapter.exe"
[[ -x "$adapter" ]] || { echo "built adapter executable is unavailable: $adapter" >&2; exit 2; }

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
  echo "repositories=$(IFS=,; echo "${repositories[*]}")"
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
  echo "benchmarking $repository"
  source="$source_parent/$repository"
  [[ -d "$source/.git" ]] || { echo "missing source repository: $source" >&2; exit 2; }
  mkdir -p "$output/$repository"
  git -C "$source" status --porcelain >"$output/$repository/source-status-before.txt"
  clone="$work/$repository"
  git clone --quiet --local --no-hardlinks "$source" "$clone"
  git -C "$clone" checkout --quiet --detach "$(git -C "$source" rev-parse HEAD)"
  git -C "$clone" rev-parse HEAD >"$output/$repository/commit.txt"

  if [[ -f "$clone/go.mod" ]]; then
    # Benchmark clones may start with an incomplete go.sum. Prepare every
    # module checksum once so the timed, read-only index does not need network
    # access or mutate the source repository.
    if ! (cd "$clone" && go mod download all) >"$output/$repository/go-mod-download.log" 2>&1; then
      echo "dependency preparation failed" >"$output/$repository/result.txt"
      failures=1
      continue
    fi
  fi
  if find "$clone" \( -name '*.csproj' -o -name '*.fsproj' \) -print -quit | grep -q .; then
    dotnet_targets=()
    while IFS= read -r target; do dotnet_targets+=("$target"); done < <(
      find "$clone" -type f \( -name '*.csproj' -o -name '*.fsproj' \) -not -path '*/bin/*' -not -path '*/obj/*' | sed "s#^$clone/##" | sort
    )
    if ! (cd "$clone"; for target in "${dotnet_targets[@]}"; do dotnet restore "$target"; done) >"$output/$repository/restore.log" 2>&1; then
      echo "dependency preparation failed" >"$output/$repository/result.txt"
      failures=1
      continue
    fi
    if find "$clone" -name '*.fsproj' -print -quit | grep -q .; then
      fsharp_targets=()
      while IFS= read -r target; do fsharp_targets+=("$target"); done < <(
        find "$clone" -type f -name '*.fsproj' -not -path '*/bin/*' -not -path '*/obj/*' | sed "s#^$clone/##" | sort
      )
      if ! (cd "$clone"; for target in "${fsharp_targets[@]}"; do dotnet build "$target" --no-restore; done) >"$output/$repository/build.log" 2>&1; then
        echo "dependency preparation failed" >"$output/$repository/result.txt"
        failures=1
        continue
      fi
    fi
  fi

  (
    cd "$clone"
    export WEAVE_DOTNET_ADAPTER="$adapter"
    export DOTNET_ROOT="$dotnet_root"
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

  git -C "$source" status --porcelain >"$output/$repository/source-status-after.txt"
  if ! cmp -s "$output/$repository/source-status-before.txt" "$output/$repository/source-status-after.txt"; then
    echo "source repository status changed: $source" >&2
    exit 1
  fi
  if [[ -f "$output/$repository/result.txt" ]]; then
    failures=1
  fi
done

exit "$failures"
