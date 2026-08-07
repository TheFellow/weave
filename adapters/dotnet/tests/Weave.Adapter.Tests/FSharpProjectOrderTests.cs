using Weave.Adapter;
using Xunit;

namespace Weave.Adapter.Tests;

public sealed class FSharpProjectOrderTests
{
    [Fact]
    public void DependentsPrecedeDependenciesUsingEvaluatedProjectReferences()
    {
        var root = Path.Combine(Path.GetTempPath(), "weave-project-order-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(root);
        try
        {
            var dependency = Path.Combine(root, "A.fsproj");
            var dependent = Path.Combine(root, "Z.fsproj");
            File.WriteAllText(dependency, "<Project Sdk=\"Microsoft.NET.Sdk\"><PropertyGroup><TargetFramework>net10.0</TargetFramework></PropertyGroup></Project>");
            File.WriteAllText(dependent, "<Project Sdk=\"Microsoft.NET.Sdk\"><PropertyGroup><TargetFramework>net10.0</TargetFramework></PropertyGroup><ItemGroup><ProjectReference Include=\"A.fsproj\" /></ItemGroup></Project>");

            var ordered = FSharpProjectOrder.DependentsFirst([dependency, dependent]);

            Assert.Equal([dependent, dependency], ordered);
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }
}
