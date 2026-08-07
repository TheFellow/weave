# weave-dotnet release artifact

This is the compiler-native C#/F# companion for the same-version Weave release.
It implements `weave.adapter/v0` and requires the .NET 10 runtime. Install the
single-file release executable into Weave's managed adapter store, or set
`WEAVE_DOTNET_ADAPTER` to its full path. Confirm activation without indexing:

```sh
weave adapters install /path/to/weave-dotnet --allow-build-tool
weave adapters doctor
```

The adapter may evaluate MSBuild projects but never restores packages or uses
the network during indexing. Restore target repositories separately.
