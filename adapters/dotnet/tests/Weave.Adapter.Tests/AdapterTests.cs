using System.Text.Json;
using Weave.Adapter.FSharp;
using Weave.Adapter.Model;
using Xunit;

namespace Weave.Adapter.Tests;

public sealed class AdapterTests
{
    private static readonly string Fixtures = Path.GetFullPath(Path.Combine(AppContext.BaseDirectory, "../../../../fixtures"));

    [Fact]
    public void IdentityIsDomainDelimitedAndDeterministic()
    {
        Assert.Equal(Identity.Hash("a", "b"), Identity.Hash("a", "b"));
        Assert.NotEqual(Identity.Hash("a", "bc"), Identity.Hash("ab", "c"));
    }

    [Fact]
    public void RepositoryPathsRejectEscapes()
    {
        var paths = new RepositoryPaths(Path.Combine(Fixtures, "Mixed"));
        Assert.Equal("Core/Greeter.cs", paths.RelativeFile(Path.Combine(Fixtures, "Mixed", "Core", "Greeter.cs")));
        Assert.True(paths.TryRelativeFile(Path.Combine(Fixtures, "Mixed", "Core", "Greeter.cs"), out var relative));
        Assert.Equal("Core/Greeter.cs", relative);
        Assert.False(paths.TryRelativeFile(Path.Combine(Fixtures, "outside.cs"), out _));
        Assert.Throws<InvalidDataException>(() => paths.RelativeFile(Path.Combine(Fixtures, "outside.cs")));
    }

    [Fact]
    public void ProtocolWriterEmitsStrictLifecycleAndCounts()
    {
        var request = Request(Path.Combine(Fixtures, "Mixed"));
        var output = new StringWriter();
        var facts = new UnitFacts { Unit = new Unit { Id = "unit", Language = "csharp" } };
        var writer = new ProtocolWriter(output, request);
        writer.RunBegin();
        writer.Unit(facts);
        writer.RunEnd(["unit"]);
        var frames = output.ToString().Split(Environment.NewLine, StringSplitOptions.RemoveEmptyEntries)
            .Select(line => JsonDocument.Parse(line)).ToArray();
        Assert.Equal(new[] { "run.begin", "unit.begin", "unit.end", "run.end" },
            frames.Select(frame => frame.RootElement.GetProperty("kind").GetString()));
        Assert.Equal(0, frames[2].RootElement.GetProperty("payload").GetProperty("counts").GetProperty("symbols").GetInt32());
    }

    [Fact]
    public void ProtocolRejectsUnknownRequestFields()
    {
        Assert.Throws<JsonException>(() => Protocol.ReadRequest(new StringReader("""
            {"protocol":"weave.adapter/v0","request_id":"x","repository_root":".","permissions":{"network":false,"restore":false,"build_tool":false,"run_generators":false},"limits":{"max_frame_bytes":1,"max_total_bytes":1,"max_frames":1,"max_facts":1},"surprise":true}
            """)));
    }

    [Fact]
    public async Task MixedSolutionProducesCompilerResolvedFactsDeterministically()
    {
        var root = Path.Combine(Fixtures, "Mixed");
        var request = Request(root);
        var paths = new RepositoryPaths(root);
        var first = await new CSharpIndexer(paths, request).IndexAsync(CancellationToken.None);
        var second = await new CSharpIndexer(paths, request).IndexAsync(CancellationToken.None);

        Assert.Contains(first.Units.SelectMany(unit => unit.Edges), edge => edge.Kind == "calls");
        Assert.Contains(first.Units.SelectMany(unit => unit.Edges), edge => edge.Kind == "implements");
        Assert.Contains(first.Units.SelectMany(unit => unit.Edges), edge => edge.Kind == "depends-on");
        Assert.Equal(first.Units.Select(unit => unit.Unit.InventoryDigest), second.Units.Select(unit => unit.Unit.InventoryDigest));
        var allIds = first.Units.SelectMany(unit => unit.Documents.Select(value => value.Id)
            .Concat(unit.Symbols.Select(value => value.Id)).Concat(unit.Occurrences.Select(value => value.Id)).Concat(unit.Edges.Select(value => value.Id))).ToArray();
        Assert.Equal(allIds.Length, allIds.Distinct(StringComparer.Ordinal).Count());

        var fsharp = await FSharpIndexer.IndexAsync(Path.Combine(root, "FSharpLib", "FSharpLib.fsproj"), root, "fixture", "Debug", CancellationToken.None);
        var fsharpReport = string.Join(Environment.NewLine,
            fsharp.Diagnostics.Select(diagnostic => diagnostic.Severity + ": " + diagnostic.Message)
                .Append("documents: " + string.Join(", ", fsharp.Units.SelectMany(unit => unit.Documents).Select(document => document.Path)))
                .Append("symbols: " + string.Join(", ", fsharp.Units.SelectMany(unit => unit.Symbols).Select(symbol => symbol.DisplayName))));
        Assert.True(fsharp.Units.SelectMany(unit => unit.Symbols).Any(symbol => symbol.DisplayName == "greet"), fsharpReport);
        Assert.True(fsharp.Units.SelectMany(unit => unit.Occurrences).Any(occurrence => occurrence.Role == "reference"), fsharpReport);
        Assert.True(fsharp.Units.SelectMany(unit => unit.Edges).Any(edge => edge.Kind == "depends-on"), fsharpReport);
    }

    [Fact]
    public async Task MalformedProjectFailsWithoutPublishingFacts()
    {
        var root = Path.Combine(Fixtures, "Malformed");
        await Assert.ThrowsAnyAsync<Exception>(() => new CSharpIndexer(new RepositoryPaths(root), Request(root)).IndexAsync(CancellationToken.None));
    }

    [Fact]
    public void NearestNestedSolutionDefinesTheProjectBoundary()
    {
        var root = Path.Combine(Path.GetTempPath(), "weave-dotnet-solutions-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(Path.Combine(root, "adapter"));
        Directory.CreateDirectory(Path.Combine(root, "tests", "fixtures"));
        try
        {
            var primary = Path.Combine(root, "adapter", "Primary.sln");
            File.WriteAllText(primary, "");
            File.WriteAllText(Path.Combine(root, "tests", "fixtures", "Fixture.sln"), "");
            Assert.Equal([primary], CSharpIndexer.FindSolutions(root));
        }
        finally
        {
            Directory.Delete(root, recursive: true);
        }
    }

    private static IndexRequest Request(string root) => new()
    {
        Protocol = Constants.Protocol,
        RequestId = "test",
        RepositoryRoot = root,
        RepositoryIdentity = "fixture",
        Variant = "Debug",
        Permissions = new(false, false, true, false),
        Limits = new(1 << 20, 32 << 20, 10_000, 1_000_000),
    };
}
