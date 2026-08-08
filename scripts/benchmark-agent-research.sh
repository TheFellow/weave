#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 3 || $# -gt 4 ]]; then
  echo "usage: $0 REPOSITORY CASE_DIRECTORY OUTPUT_DIRECTORY [RUNS]" >&2
  exit 2
fi

repository=$(cd "$1" && pwd)
case_directory=$(cd "$2" && pwd)
mkdir -p "$3"
output=$(cd "$3" && pwd)
runs=${4:-1}
root=$(git rev-parse --show-toplevel)
timeout_seconds=${WEAVE_AGENT_BENCHMARK_TIMEOUT:-600}
codex_executable=${WEAVE_AGENT_BENCHMARK_CODEX:-$(command -v codex || true)}

[[ -d "$repository/.git" ]] || { echo "repository is not a Git worktree: $repository" >&2; exit 2; }
[[ -f "$case_directory/prompt.md" ]] || { echo "missing $case_directory/prompt.md" >&2; exit 2; }
[[ -f "$case_directory/rubric.json" ]] || { echo "missing $case_directory/rubric.json" >&2; exit 2; }
[[ "$runs" =~ ^[1-9][0-9]*$ ]] || { echo "RUNS must be a positive integer" >&2; exit 2; }
[[ -n "$codex_executable" ]] || { echo "codex executable is unavailable" >&2; exit 2; }
for executable in git go python3 jq; do
  command -v "$executable" >/dev/null || { echo "missing required executable: $executable" >&2; exit 2; }
done
[[ -z "$(git -C "$repository" status --porcelain)" ]] || { echo "source repository must be clean" >&2; exit 2; }

work=$(mktemp -d "${TMPDIR:-/tmp}/weave-agent-benchmark.XXXXXX")
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/with-bin" "$work/without-bin"
go build -trimpath -o "$work/with-bin/weave" ./cmd/weave
ln -s "$root/scripts/weave-benchmark-blocked" "$work/without-bin/weave"

{
  echo "timestamp_utc=$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  echo "weave_commit=$(git rev-parse HEAD)"
  echo "repository_commit=$(git -C "$repository" rev-parse HEAD)"
  echo "weave_dirty=$([[ -n "$(git status --porcelain)" ]] && echo true || echo false)"
  echo "codex=$($codex_executable --version)"
  echo "go=$(go version)"
  echo "runs=$runs"
  echo "timeout_seconds=$timeout_seconds"
  echo "agent_sandbox=workspace-write"
  echo "index_access=exact-clone-.git/weave"
  echo "source_worktree_audit=git-status-porcelain"
} >"$output/environment.txt"

run_arm() {
  local run=$1
  local arm=$2
  local sample_directory="$output/run-$run/$arm"
  local clone="$work/run-$run-$arm"
  local arm_path="$work/without-bin:$PATH"
  local guidance="Weave is intentionally unavailable. Use ordinary repository search and source-reading tools. Do not invoke or install weave."
  mkdir -p "$sample_directory"
  git clone --quiet --local --no-hardlinks "$repository" "$clone"
  git -C "$clone" checkout --quiet --detach "$(git -C "$repository" rev-parse HEAD)"

  if [[ "$arm" == "with-weave" ]]; then
    arm_path="$work/with-bin:$PATH"
    guidance="A current local semantic index is available as the weave CLI. Start with one weave explore using the research question and treat its current source excerpts as already-read evidence. Use targeted queries or file reads only for specific evidence that dossier does not contain."
    (cd "$clone" && PATH="$arm_path" weave index --json >"$sample_directory/index.json")
  fi

  {
    echo "$guidance"
    echo
    echo "Work read-only. Answer accurately with repository-relative file:line evidence. Do not edit the repository."
    echo
    cat "$case_directory/prompt.md"
  } >"$sample_directory/prompt.md"

  : >"$sample_directory/blocked-weave.log"
  local codex_args=(
    exec --ephemeral --ignore-user-config --json --sandbox workspace-write
    --add-dir "$clone/.git/weave"
    --config shell_environment_policy.inherit=all
    --cd "$clone" --output-last-message "$sample_directory/answer.md"
  )
  if [[ -n "${WEAVE_AGENT_BENCHMARK_MODEL:-}" ]]; then
    codex_args+=(--model "$WEAVE_AGENT_BENCHMARK_MODEL")
  fi
  codex_args+=(-)
  set +e
  PATH="$arm_path" WEAVE_BENCHMARK_BLOCK_LOG="$sample_directory/blocked-weave.log" \
    python3 "$root/scripts/measure-command.py" \
      --timeout "$timeout_seconds" \
      --stdout "$sample_directory/events.jsonl" \
      --stderr "$sample_directory/stderr.txt" \
      --result "$sample_directory/process.json" -- \
      "$codex_executable" "${codex_args[@]}" <"$sample_directory/prompt.md"
  local exit_code=$?
  set -e

  git -C "$clone" status --porcelain >"$sample_directory/clone-status.txt"
  if [[ -s "$sample_directory/clone-status.txt" ]]; then
    echo "agent changed the disposable source worktree" >>"$sample_directory/stderr.txt"
    exit_code=1
  fi
  python3 "$root/scripts/summarize-agent-benchmark.py" \
    --arm "$arm" --events "$sample_directory/events.jsonl" \
    --answer "$sample_directory/answer.md" --sample "$sample_directory/process.json" \
    --rubric "$case_directory/rubric.json" --block-log "$sample_directory/blocked-weave.log" \
    --output "$sample_directory/summary.json"
  return "$exit_code"
}

for run in $(seq 1 "$runs"); do
  run_arm "$run" with-weave || true
  run_arm "$run" without-weave || true
done

jq -s \
  '{schema:"weave.agent-research-benchmark/v1",samples:.,all_successful:all(.[];.exit_code == 0 and (.timed_out|not))}' \
  "$output"/run-*/with-weave/summary.json "$output"/run-*/without-weave/summary.json \
  >"$output/summary.json"

[[ -z "$(git -C "$repository" status --porcelain)" ]] || { echo "source repository became dirty" >&2; exit 1; }
cat "$output/summary.json"
