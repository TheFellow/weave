#!/bin/sh
set -eu

D3_VERSION=7.9.0
D3_GRAPHVIZ_VERSION=5.6.0
HPCC_WASM_VERSION=2.20.0
GRAPHVIZ_VERSION=12.1.0

asset_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
scratch=$(mktemp -d "${TMPDIR:-/tmp}/weave-explorer-vendor.XXXXXX")
trap 'rm -rf "$scratch"' EXIT HUP INT TERM
mkdir -p "$asset_dir/vendor" "$asset_dir/licenses"

npm pack --silent --pack-destination "$scratch" "d3@$D3_VERSION" >/dev/null
npm pack --silent --pack-destination "$scratch" "d3-graphviz@$D3_GRAPHVIZ_VERSION" >/dev/null
npm pack --silent --pack-destination "$scratch" "@hpcc-js/wasm@$HPCC_WASM_VERSION" >/dev/null

tar -xOf "$scratch/d3-$D3_VERSION.tgz" package/dist/d3.min.js >"$scratch/d3.min.js"
tar -xOf "$scratch/d3-graphviz-$D3_GRAPHVIZ_VERSION.tgz" package/build/d3-graphviz.min.js >"$scratch/d3-graphviz.min.js"
tar -xOf "$scratch/hpcc-js-wasm-$HPCC_WASM_VERSION.tgz" package/dist/graphviz.umd.js >"$scratch/graphviz.umd.js"
tar -xOf "$scratch/d3-$D3_VERSION.tgz" package/LICENSE >"$scratch/D3-ISC.txt"
tar -xOf "$scratch/d3-graphviz-$D3_GRAPHVIZ_VERSION.tgz" package/LICENSE >"$scratch/d3-graphviz-BSD-3-Clause.txt"
tar -xOf "$scratch/hpcc-js-wasm-$HPCC_WASM_VERSION.tgz" package/LICENSE >"$scratch/hpcc-js-wasm-Apache-2.0.txt"
curl --fail --silent --show-error --location \
  "https://gitlab.com/graphviz/graphviz/-/raw/$GRAPHVIZ_VERSION/LICENSE" \
  >"$scratch/Graphviz-EPL-1.0.txt"

install -m 0644 "$scratch/d3.min.js" "$asset_dir/vendor/d3.min.js"
install -m 0644 "$scratch/d3-graphviz.min.js" "$asset_dir/vendor/d3-graphviz.min.js"
install -m 0644 "$scratch/graphviz.umd.js" "$asset_dir/vendor/graphviz.umd.js"
install -m 0644 "$scratch/D3-ISC.txt" "$asset_dir/licenses/D3-ISC.txt"
install -m 0644 "$scratch/d3-graphviz-BSD-3-Clause.txt" "$asset_dir/licenses/d3-graphviz-BSD-3-Clause.txt"
install -m 0644 "$scratch/hpcc-js-wasm-Apache-2.0.txt" "$asset_dir/licenses/hpcc-js-wasm-Apache-2.0.txt"
install -m 0644 "$scratch/Graphviz-EPL-1.0.txt" "$asset_dir/licenses/Graphviz-EPL-1.0.txt"

(cd "$asset_dir/vendor" && shasum -a 256 d3.min.js d3-graphviz.min.js graphviz.umd.js >SHA256SUMS)
