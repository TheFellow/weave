using System.Text;
using Microsoft.CodeAnalysis;
using Microsoft.CodeAnalysis.CSharp;
using Microsoft.CodeAnalysis.CSharp.Syntax;
using Microsoft.CodeAnalysis.MSBuild;
using Microsoft.CodeAnalysis.Operations;
using Microsoft.CodeAnalysis.Text;
using Weave.Adapter.Model;
using FactDocument = Weave.Adapter.Model.Document;
using FactSymbol = Weave.Adapter.Model.Symbol;

namespace Weave.Adapter;

public sealed class CSharpIndexer(RepositoryPaths paths, IndexRequest request)
{
    private static readonly SymbolDisplayFormat StableFormat = new(
        globalNamespaceStyle: SymbolDisplayGlobalNamespaceStyle.Included,
        typeQualificationStyle: SymbolDisplayTypeQualificationStyle.NameAndContainingTypesAndNamespaces,
        genericsOptions: SymbolDisplayGenericsOptions.IncludeTypeParameters,
        memberOptions: SymbolDisplayMemberOptions.IncludeContainingType |
                       SymbolDisplayMemberOptions.IncludeExplicitInterface |
                       SymbolDisplayMemberOptions.IncludeParameters |
                       SymbolDisplayMemberOptions.IncludeType |
                       SymbolDisplayMemberOptions.IncludeModifiers,
        parameterOptions: SymbolDisplayParameterOptions.IncludeType |
                          SymbolDisplayParameterOptions.IncludeParamsRefOut |
                          SymbolDisplayParameterOptions.IncludeOptionalBrackets,
        miscellaneousOptions: SymbolDisplayMiscellaneousOptions.UseSpecialTypes |
                              SymbolDisplayMiscellaneousOptions.IncludeNullableReferenceTypeModifier);

    public async Task<(List<UnitFacts> Units, List<AdapterDiagnostic> Diagnostics)> IndexAsync(CancellationToken cancellationToken)
    {
        if (!request.Permissions.BuildTool)
            throw new InvalidOperationException("C# project evaluation requires permissions.build_tool=true; no restore is performed");
        if (request.Permissions.Restore)
            throw new InvalidOperationException("weave-dotnet does not perform restore; restore the repository explicitly before indexing");

        var diagnostics = new List<AdapterDiagnostic>();
        var workspaceFailures = new List<string>();
        using var workspace = MSBuildWorkspace.Create(BuildProperties());
        workspace.WorkspaceFailed += (_, args) =>
        {
            var message = "MSBuildWorkspace: " + args.Diagnostic.Message;
            diagnostics.Add(new(args.Diagnostic.Kind == WorkspaceDiagnosticKind.Failure ? "error" : "warning", message));
            if (args.Diagnostic.Kind == WorkspaceDiagnosticKind.Failure) workspaceFailures.Add(message);
        };

        var projects = await LoadProjectsAsync(workspace, cancellationToken);
        if (workspaceFailures.Count != 0)
            throw new InvalidOperationException(string.Join(Environment.NewLine, workspaceFailures));
        var csProjects = projects.Where(p => p.Language == LanguageNames.CSharp)
            .OrderBy(p => p.FilePath, StringComparer.Ordinal).ThenBy(p => p.Name, StringComparer.Ordinal).ToArray();
        var units = new List<UnitFacts>(csProjects.Length);
        foreach (var project in csProjects)
            units.Add(await IndexProjectAsync(project, diagnostics, cancellationToken));
        return (units, diagnostics);
    }

    private Dictionary<string, string> BuildProperties()
    {
        var properties = new Dictionary<string, string>(StringComparer.OrdinalIgnoreCase)
        {
            ["DesignTimeBuild"] = "true",
            ["SkipCompilerExecution"] = "true",
            ["ProvideCommandLineArgs"] = "true",
            ["RestoreIgnoreFailedSources"] = "true",
        };
        if (!string.IsNullOrWhiteSpace(request.Variant)) properties["Configuration"] = request.Variant;
        return properties;
    }

    private async Task<IReadOnlyList<Project>> LoadProjectsAsync(MSBuildWorkspace workspace, CancellationToken cancellationToken)
    {
        var solutions = Directory.EnumerateFiles(paths.Root, "*.sln", SearchOption.TopDirectoryOnly)
            .Concat(Directory.EnumerateFiles(paths.Root, "*.slnx", SearchOption.TopDirectoryOnly))
            .Order(StringComparer.Ordinal).ToArray();
        if (solutions.Length > 1) throw new InvalidOperationException("multiple solution files found; select a solution with --adapter-arg support when added");
        if (solutions.Length == 1)
            return (await workspace.OpenSolutionAsync(solutions[0], cancellationToken: cancellationToken)).Projects.ToArray();

        var projectFiles = Directory.EnumerateFiles(paths.Root, "*.csproj", SearchOption.AllDirectories)
            .Where(path => !IsBuildOutput(path)).Order(StringComparer.Ordinal).ToArray();
        var projects = new List<Project>();
        foreach (var file in projectFiles) projects.Add(await workspace.OpenProjectAsync(file, cancellationToken: cancellationToken));
        return projects;
    }

    private static bool IsBuildOutput(string path) => path.Split(Path.DirectorySeparatorChar)
        .Any(part => part is "bin" or "obj" or ".git");

    private async Task<UnitFacts> IndexProjectAsync(Project project, List<AdapterDiagnostic> diagnostics, CancellationToken cancellationToken)
    {
        if (!request.Permissions.RunGenerators)
            project = project.WithAnalyzerReferences([]);
        var compilation = await project.GetCompilationAsync(cancellationToken) as CSharpCompilation
            ?? throw new InvalidOperationException($"C# compilation unavailable for {project.Name}");
        var projectPath = project.FilePath is null ? project.Name : paths.RelativeFile(project.FilePath);
        var variant = Variant(project, compilation);
        var unitId = "dotnet:csharp:unit:" + Identity.Hash(request.RepositoryIdentity, projectPath, project.AssemblyName, variant);
        var symbolScope = projectPath + "\0" + variant;
        var rootSymbolId = "dotnet:csharp:project:" + Identity.Hash(request.RepositoryIdentity, projectPath, project.AssemblyName, variant);
        var facts = new UnitFacts
        {
            Unit = new Unit { Id = unitId, Language = "csharp", Variant = variant },
        };
        facts.Symbols.Add(new FactSymbol
        {
            Id = rootSymbolId, UnitId = unitId, StableName = projectPath,
            DisplayName = project.Name, NormalizedName = Identity.NormalizeName(project.Name), Kind = "project",
        });

        var documents = new Dictionary<SyntaxTree, (FactDocument Fact, SourceText Text)>();
        foreach (var document in project.Documents.OrderBy(d => d.FilePath, StringComparer.Ordinal))
        {
            if (document.FilePath is null) continue;
            if (IsBuildOutput(document.FilePath))
            {
                diagnostics.Add(new("info", "generated/build-output document omitted: " + document.FilePath, unitId));
                continue;
            }
            var relative = paths.RelativeFile(document.FilePath);
            var text = await document.GetTextAsync(cancellationToken);
            var tree = await document.GetSyntaxTreeAsync(cancellationToken);
            if (tree is null) continue;
            var fact = new FactDocument
            {
                Id = "dotnet:document:" + Identity.Hash(unitId, relative),
                UnitId = unitId, Path = relative, Language = "csharp",
                ContentHash = Identity.Hash(text.ToString()),
            };
            documents[tree] = (fact, text);
            facts.Documents.Add(fact);
        }

        var symbolIds = new Dictionary<ISymbol, string>(SymbolEqualityComparer.Default);
        var declarationKeys = new HashSet<string>(StringComparer.Ordinal);
        foreach (var symbol in SourceSymbols(compilation.Assembly.GlobalNamespace))
        {
            var location = symbol.Locations.FirstOrDefault(l => l.IsInSource && l.SourceTree is not null && documents.ContainsKey(l.SourceTree));
            if (location is null || location.SourceTree is null) continue;
            var id = SymbolId(symbol, symbolScope);
            symbolIds[symbol] = id;
            var document = documents[location.SourceTree];
            var range = ToRange(document.Text, location.SourceSpan);
            facts.Symbols.Add(new FactSymbol
            {
                Id = id, UnitId = unitId, StableName = StableName(symbol), DisplayName = symbol.Name,
                NormalizedName = Identity.NormalizeName(symbol.Name), Kind = Kind(symbol),
                DocumentId = document.Fact.Id, Definition = range,
            });
            AddOccurrence(facts, id, document.Fact.Id, "definition", range, declarationKeys);
            AddEdge(facts, rootSymbolId, id, "defines", document.Fact.Id, range);
        }

        foreach (var (tree, document) in documents.OrderBy(pair => pair.Value.Fact.Path, StringComparer.Ordinal))
        {
            var model = compilation.GetSemanticModel(tree, ignoreAccessibility: true);
            var root = await tree.GetRootAsync(cancellationToken);
            foreach (var node in root.DescendantNodes())
            {
                if (IsReferenceNode(node))
                {
                    var target = model.GetSymbolInfo(node, cancellationToken).Symbol;
                    if (target is not null && !IsDeclarationSpan(target, tree, node.Span))
                    {
                        var targetId = symbolIds.TryGetValue(target.OriginalDefinition, out var local) ? local : SymbolId(target, symbolScope);
                        var range = ToRange(document.Text, node.Span);
                        AddOccurrence(facts, targetId, document.Fact.Id, "reference", range, declarationKeys);
                        var owner = model.GetEnclosingSymbol(node.SpanStart, cancellationToken);
                        AddEdge(facts, owner is null ? rootSymbolId : SymbolId(owner, symbolScope), targetId, "references", document.Fact.Id, range);
                    }
                }

                if (node is InvocationExpressionSyntax or ObjectCreationExpressionSyntax)
                {
                    var operation = model.GetOperation(node, cancellationToken);
                    var target = operation switch
                    {
                        IInvocationOperation invocation => invocation.TargetMethod,
                        IObjectCreationOperation creation => creation.Constructor,
                        _ => null,
                    };
                    var owner = model.GetEnclosingSymbol(node.SpanStart, cancellationToken);
                    if (target is not null && owner is not null)
                        AddEdge(facts, SymbolId(owner, symbolScope), SymbolId(target, symbolScope), "calls", document.Fact.Id, ToRange(document.Text, node.Span));
                }

                if (node is UsingDirectiveSyntax usingDirective && usingDirective.Name is not null)
                {
                    var target = model.GetSymbolInfo(usingDirective.Name, cancellationToken).Symbol;
                    if (target is not null)
                        AddEdge(facts, rootSymbolId, SymbolId(target, symbolScope), "imports", document.Fact.Id, ToRange(document.Text, usingDirective.Name.Span));
                }
            }
        }

        foreach (var type in SourceSymbols(compilation.Assembly.GlobalNamespace).OfType<INamedTypeSymbol>())
        {
            if (type.BaseType is { SpecialType: not SpecialType.System_Object } baseType)
                AddEdge(facts, SymbolId(type, symbolScope), SymbolId(baseType, symbolScope), "extends");
            foreach (var iface in type.Interfaces)
                AddEdge(facts, SymbolId(type, symbolScope), SymbolId(iface, symbolScope), "implements");
        }

        foreach (var reference in project.ProjectReferences.OrderBy(r => r.ProjectId.Id))
        {
            var dependency = project.Solution.GetProject(reference.ProjectId);
            if (dependency is null) continue;
            var dependencyPath = dependency.FilePath is null ? dependency.Name : paths.RelativeFile(dependency.FilePath);
            var language = dependency.Language == LanguageNames.CSharp ? "csharp" : dependency.Language.ToLowerInvariant();
            var dependencyVariant = "external";
            if (dependency.Language == LanguageNames.CSharp && await dependency.GetCompilationAsync(cancellationToken) is CSharpCompilation dependencyCompilation)
                dependencyVariant = Variant(dependency, dependencyCompilation);
            var dependencyId = $"dotnet:{language}:project:" + Identity.Hash(request.RepositoryIdentity, dependencyPath, dependency.AssemblyName, dependencyVariant);
            AddEdge(facts, rootSymbolId, dependencyId, "depends-on");
        }

        foreach (var diagnostic in compilation.GetDiagnostics(cancellationToken).Where(d => d.Severity >= DiagnosticSeverity.Warning).Take(100))
            diagnostics.Add(new(diagnostic.Severity == DiagnosticSeverity.Error ? "error" : "warning", diagnostic.ToString(), unitId));

        Sort(facts);
        facts.Unit.InputFingerprint = InputFingerprint(project, compilation, facts.Documents);
        facts.Unit.SurfaceFingerprint = SurfaceFingerprint(compilation);
        facts.Unit.InventoryDigest = InventoryFingerprint(facts);
        return facts;
    }

    private string SymbolId(ISymbol symbol, string projectScope)
    {
        symbol = symbol.OriginalDefinition;
        var assembly = symbol.ContainingAssembly?.Identity.ToString() ?? projectScope;
        var sourceScope = symbol.Locations.Any(location => location.IsInSource) ? projectScope : "metadata";
        var locality = symbol.Kind is SymbolKind.Local or SymbolKind.Label or SymbolKind.RangeVariable or SymbolKind.Parameter
            ? symbol.Locations.FirstOrDefault(l => l.IsInSource)?.SourceSpan.Start.ToString() ?? "unknown"
            : "global";
        return "dotnet:csharp:symbol:" + Identity.Hash(request.RepositoryIdentity, assembly, sourceScope, StableName(symbol), locality);
    }

    private static string StableName(ISymbol symbol) => symbol.ToDisplayString(StableFormat);
    private static string Kind(ISymbol symbol) => symbol.Kind switch
    {
        SymbolKind.NamedType => ((INamedTypeSymbol)symbol).TypeKind.ToString().ToLowerInvariant(),
        SymbolKind.Method => "method", SymbolKind.Property => "property", SymbolKind.Field => "field",
        SymbolKind.Event => "event", SymbolKind.Namespace => "namespace", SymbolKind.Parameter => "parameter",
        SymbolKind.Local => "variable", SymbolKind.TypeParameter => "type-parameter", _ => symbol.Kind.ToString().ToLowerInvariant(),
    };

    private static IEnumerable<ISymbol> SourceSymbols(INamespaceSymbol root)
    {
        foreach (var member in root.GetMembers().OrderBy(StableName, StringComparer.Ordinal))
        {
            if (member is INamespaceSymbol ns)
            {
                if (ns.Locations.Any(l => l.IsInSource)) yield return ns;
                foreach (var nested in SourceSymbols(ns)) yield return nested;
            }
            else if (member is INamedTypeSymbol type)
            {
                foreach (var nested in SourceSymbols(type)) yield return nested;
            }
        }
    }

    private static IEnumerable<ISymbol> SourceSymbols(INamedTypeSymbol type)
    {
        yield return type;
        foreach (var member in type.GetMembers().Where(m => m.Locations.Any(l => l.IsInSource)).OrderBy(StableName, StringComparer.Ordinal))
        {
            if (member is INamedTypeSymbol nested)
                foreach (var child in SourceSymbols(nested)) yield return child;
            else yield return member;
        }
    }

    private static bool IsReferenceNode(SyntaxNode node) => node is IdentifierNameSyntax or GenericNameSyntax or ThisExpressionSyntax or BaseExpressionSyntax;
    private static bool IsDeclarationSpan(ISymbol symbol, SyntaxTree tree, TextSpan span) => symbol.Locations.Any(l => l.SourceTree == tree && l.SourceSpan == span);

    private static SourceRange ToRange(SourceText text, TextSpan span)
    {
        var lineSpan = text.Lines.GetLinePositionSpan(span);
        return new(
            new(lineSpan.Start.Line, Utf8Column(text, lineSpan.Start), span.Start),
            new(lineSpan.End.Line, Utf8Column(text, lineSpan.End), span.End));
    }

    private static int Utf8Column(SourceText text, LinePosition position)
    {
        var line = text.Lines[position.Line];
        return Encoding.UTF8.GetByteCount(text.ToString(TextSpan.FromBounds(line.Start, line.Start + position.Character)));
    }

    private static void AddOccurrence(UnitFacts facts, string symbol, string document, string role, SourceRange range, HashSet<string> seen)
    {
        var key = $"{role}\0{symbol}\0{document}\0{range.Start.Byte}\0{range.End.Byte}";
        if (!seen.Add(key)) return;
        facts.Occurrences.Add(new Occurrence
        {
            Id = "dotnet:occurrence:" + Identity.Hash(facts.Unit.Id, key), UnitId = facts.Unit.Id,
            SymbolId = symbol, DocumentId = document, Role = role, Range = range,
        });
    }

    private static void AddEdge(UnitFacts facts, string from, string to, string kind, string document = "", SourceRange? range = null)
    {
        var actualRange = range ?? SourceRange.Empty;
        var key = $"{kind}\0{from}\0{to}\0{document}\0{actualRange.Start.Byte}\0{actualRange.End.Byte}";
        if (facts.Edges.Any(edge => edge.Id == "dotnet:edge:" + Identity.Hash(facts.Unit.Id, key))) return;
        facts.Edges.Add(new Edge
        {
            Id = "dotnet:edge:" + Identity.Hash(facts.Unit.Id, key), UnitId = facts.Unit.Id,
            From = from, To = to, Kind = kind, DocumentId = document, Range = actualRange,
        });
    }

    private static string Variant(Project project, CSharpCompilation compilation) => string.Join(";", new[]
    {
        project.Name, project.AssemblyName ?? "", compilation.Options.OutputKind.ToString(),
        ((CSharpParseOptions?)project.ParseOptions)?.LanguageVersion.ToString() ?? "",
        ((CSharpCompilationOptions?)project.CompilationOptions)?.NullableContextOptions.ToString() ?? "",
    });

    private static string InputFingerprint(Project project, Compilation compilation, IEnumerable<FactDocument> documents) => Identity.Hash(
        "weave-dotnet-csharp-input/v1", project.FilePath, project.AssemblyName,
        project.ParseOptions?.ToString(), project.CompilationOptions?.ToString(),
        string.Join("\n", documents.OrderBy(d => d.Path).Select(d => d.Path + "\0" + d.ContentHash)),
        string.Join("\n", compilation.References.Select(r => r.Display ?? "").Order(StringComparer.Ordinal)));

    private static string SurfaceFingerprint(Compilation compilation) => Identity.Hash(
        "weave-dotnet-csharp-surface/v1",
        string.Join("\n", SourceSymbols(compilation.Assembly.GlobalNamespace)
            .Where(s => s.DeclaredAccessibility is Accessibility.Public or Accessibility.Protected or Accessibility.ProtectedOrInternal)
            .Select(StableName).Order(StringComparer.Ordinal)));

    private static string InventoryFingerprint(UnitFacts facts) => Identity.Hash(
        "weave-dotnet-inventory/v1",
        string.Join("\n", facts.Documents.Select(d => d.Id + "\0" + d.ContentHash)),
        string.Join("\n", facts.Symbols.Select(s => s.Id)),
        string.Join("\n", facts.Occurrences.Select(o => o.Id)),
        string.Join("\n", facts.Edges.Select(e => e.Id)));

    private static void Sort(UnitFacts facts)
    {
        facts.Documents.Sort((a, b) => StringComparer.Ordinal.Compare(a.Path, b.Path));
        facts.Symbols.Sort((a, b) => StringComparer.Ordinal.Compare(a.Id, b.Id));
        facts.Occurrences.Sort((a, b) => StringComparer.Ordinal.Compare(a.Id, b.Id));
        facts.Edges.Sort((a, b) => StringComparer.Ordinal.Compare(a.Id, b.Id));
    }
}
