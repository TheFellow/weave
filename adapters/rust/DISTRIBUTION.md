# Rust adapter distribution

`weave-rust` is a platform-native companion binary. Build reproducibly from the
checked-in lockfile:

```console
cargo build --locked --release --manifest-path adapters/rust/Cargo.toml
cargo package --locked --manifest-path adapters/rust/Cargo.toml
```

The release artifact is `weave-rust` (`weave-rust.exe` on Windows). It does not
embed a Rust project toolchain: a compatible `rust-analyzer`, `cargo`, and
`rustc` must remain on `PATH`, or `WEAVE_RUST_ANALYZER` must point to the desired
analyzer. `describe` reports these requirements and binds their active versions
to the provider identity.

Initial CI validates native builds on Linux, macOS, and Windows. A future shared
release change may attach target archives or publish the crate after release
policy decides signing, supported target triples, and rust-analyzer acquisition.
Those shared release files are intentionally outside this adapter increment.
