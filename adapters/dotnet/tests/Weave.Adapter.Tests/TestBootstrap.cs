using System.Runtime.CompilerServices;
using Microsoft.Build.Locator;
using Xunit;

[assembly: CollectionBehavior(DisableTestParallelization = true)]

namespace Weave.Adapter.Tests;

internal static class TestBootstrap
{
    // Registration must happen before a test method that mentions Microsoft.Build
    // is JIT-compiled. Per-test check-then-register calls race under xUnit.
    [ModuleInitializer]
    internal static void InitializeMSBuild()
    {
        if (!MSBuildLocator.IsRegistered) MSBuildLocator.RegisterDefaults();
    }
}
