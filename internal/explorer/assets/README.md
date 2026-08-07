# Embedded graph explorer assets

The explorer has no CDN or runtime network dependency. These exact upstream
artifacts are embedded into the Go binary:

| Asset | Version | License | Upstream |
| --- | --- | --- | --- |
| D3 | 7.9.0 | ISC | <https://github.com/d3/d3> |
| d3-graphviz | 5.6.0 | BSD-3-Clause | <https://github.com/magjac/d3-graphviz> |
| @hpcc-js/wasm Graphviz bundle | 2.20.0 (Graphviz 12.1.0) | Apache-2.0 wrapper; EPL-1.0 Graphviz | <https://github.com/hpcc-systems/hpcc-js-wasm> |

The original license texts are retained under `licenses/`. `vendor/SHA256SUMS`
records the bytes compiled into Weave.

To reproduce or deliberately update the vendored artifacts, edit only the
version constants in `update-vendor.sh`, run it from this directory, then review
the package versions, licenses, checksums, and browser tests before committing.
The script uses exact npm package versions and extracts the publishers' built
files without minifying or rewriting them.
