#!/bin/sh
set -eu

script_directory=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
toolchain_directory=$(CDPATH= cd -- "$script_directory/../toolchain" && pwd)

npm ci \
  --prefix "$toolchain_directory" \
  --ignore-scripts \
  --no-audit \
  --no-fund

printf '%s\n' "$toolchain_directory/node_modules/.bin/scip-typescript"
