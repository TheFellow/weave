# weave-dotnet release artifact

This is the compiler-native C#/F# companion for the same-version Weave release.
It implements `weave.adapter/v0` and requires the .NET 9 runtime. Put the
`weave-dotnet` executable on `PATH`, or set `WEAVE_DOTNET_ADAPTER` to its full
path. Confirm discovery without indexing:

```sh
weave adapters doctor
```

The adapter may evaluate MSBuild projects but never restores packages or uses
the network during indexing. Restore target repositories separately.
