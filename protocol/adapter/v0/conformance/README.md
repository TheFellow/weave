# Executable conformance corpus

This directory is language-neutral test data for `weave adapters conformance`.
An implementation does not import Go code: it implements the documented
process/JSON contract and is tested as an opaque executable.

`repository/` is a genuine tiny fixture for the included Python reference
fixture adapter. Third-party adapters should copy `cases.json`, add a minimal
repository in their own language, and pass that directory to the conformance
command. The runner checks describe negotiation, wrong-protocol and malformed
request rejection, a successful index, deterministic replay, bounded host
output, stderr separation, and exact process success/failure semantics.

The Python fixture is intentionally a protocol implementation, not an SDK. Run:

```text
weave adapters conformance python --adapter-arg fixture-adapter.py \
  --fixture repository --json
```
