# Security and performance hardening prior art

Research date: 2026-08-06.

- The [Go fuzzing guide](https://go.dev/doc/security/fuzz/) recommends small,
  deterministic, stateless fuzz targets and persists minimized failures as
  regression corpus entries. We target Weave's two untrusted serialization
  boundaries: framed adapter JSON and SCIP protobuf input.
- Go's [`testing` benchmarks](https://pkg.go.dev/testing#hdr-Benchmarks) provide
  allocation and latency measurements without adding a benchmark framework. A
  2,000-symbol fixture measures bounded prefix lookup, adjacency lookup, and the
  intentionally full-materialization export path.
- The [Go security guidance](https://go.dev/doc/security/best-practices)
  reinforces dependency review, input validation, and fuzzing at parsers. The
  existing implementation bounds adapter bytes/frames/facts/stderr/time,
  protobuf bytes/depth/documents/facts/strings/source, graph strings, traversal
  depth/nodes/edges, and CLI results.

These tests supplement, rather than replace, genuine compiler fixtures and
cross-platform CI. Long-running fuzzing and repository-scale benchmarks are
operator/CI jobs; ordinary `go test ./...` always runs their seed corpora.
