using System.Text.Json;
using Microsoft.Build.Locator;
using Weave.Adapter;
using Weave.Adapter.FSharp;
using Weave.Adapter.Model;

return await MainAsync(args);

static async Task<int> MainAsync(string[] args)
{
    try
    {
        if (args is ["describe", "--protocol", Constants.Protocol])
        {
            Console.WriteLine(JsonSerializer.Serialize(new
            {
                protocols = new[] { Constants.Protocol },
                provider = new { name = Constants.Provider, version = Constants.Version },
                languages = new[] { "csharp", "fsharp" },
                operations = new[] { "index" },
                refresh_modes = new[] { "full" },
                fact_encoding = Constants.FactEncoding,
                position_encodings = new[] { "utf8-byte" },
                requires = new { executables = new[] { "dotnet" }, may_run_build_tool = true },
            }));
            return 0;
        }
        if (args is not ["index", "--protocol", Constants.Protocol])
            throw new ArgumentException("usage: weave-dotnet (describe|index) --protocol weave.adapter/v0");

        var request = Protocol.ReadRequest(Console.In);
        var paths = new RepositoryPaths(request.RepositoryRoot);
        if (!request.Permissions.BuildTool)
            throw new InvalidOperationException(".NET project evaluation requires permissions.build_tool=true");
        if (request.Permissions.Restore || request.Permissions.Network)
            throw new InvalidOperationException("this adapter never restores or accesses the network; prepare dependencies before indexing");

        MSBuildLocator.RegisterDefaults();
        var (csharpUnits, csharpDiagnostics) = await new CSharpIndexer(paths, request).IndexAsync(CancellationToken.None);
        var units = new List<UnitFacts>(csharpUnits);
        var diagnostics = new List<AdapterDiagnostic>(csharpDiagnostics);
        var fsharpProjects = Directory.EnumerateFiles(paths.Root, "*.fsproj", SearchOption.AllDirectories)
            .Where(path => !path.Split(Path.DirectorySeparatorChar).Any(part => part is "bin" or "obj" or ".git"))
            .Order(StringComparer.Ordinal);
        foreach (var project in fsharpProjects)
        {
            var result = await FSharpIndexer.IndexAsync(project, paths.Root, request.RepositoryIdentity, request.Variant, CancellationToken.None);
            units.AddRange(result.Units);
            diagnostics.AddRange(result.Diagnostics);
        }

        units.Sort((a, b) => StringComparer.Ordinal.Compare(a.Unit.Id, b.Unit.Id));
        var factCount = units.Sum(unit => unit.Documents.Count + unit.Symbols.Count + unit.Occurrences.Count + unit.Edges.Count);
        if (factCount > request.Limits.MaxFacts) throw new InvalidOperationException("semantic facts exceed negotiated limit");

        var writer = new ProtocolWriter(Console.Out, request);
        writer.RunBegin();
        foreach (var diagnostic in diagnostics.OrderBy(d => d.UnitId, StringComparer.Ordinal).ThenBy(d => d.Message, StringComparer.Ordinal))
            writer.Diagnostic(diagnostic);
        foreach (var unit in units) writer.Unit(unit);
        writer.RunEnd(units.Select(unit => unit.Unit.Id));
        return 0;
    }
    catch (Exception exception)
    {
        Console.Error.WriteLine($"weave-dotnet: {exception.Message}");
        return 1;
    }
}
