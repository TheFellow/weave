namespace Weave.Adapter.FSharp

open System
open System.Collections.Generic
open System.IO
open System.Text
open System.Threading
open System.Threading.Tasks
open Buildalyzer
open Buildalyzer.IO
open FSharp.Compiler.CodeAnalysis
open FSharp.Compiler.Symbols
open FSharp.Compiler.Text
open Weave.Adapter.Model

/// Compiler-backed F# indexing. Calls are intentionally omitted until an FCS
/// typed-expression visitor is covered by the mixed-language fixtures.
type FSharpIndexer private () =
    static let checker = FSharpChecker.Create(keepAssemblyContents = true, keepAllBackgroundSymbolUses = true)

    static member IndexAsync
        (projectPath: string, repositoryRoot: string, repositoryIdentity: string, variant: string, cancellationToken: CancellationToken)
        : Task<IndexBatch> =
        task {
            let root = Path.GetFullPath repositoryRoot
            let comparison = if OperatingSystem.IsWindows() then StringComparison.OrdinalIgnoreCase else StringComparison.Ordinal
            let rootPrefix = root.TrimEnd(Path.DirectorySeparatorChar, Path.AltDirectorySeparatorChar) + string Path.DirectorySeparatorChar
            let resolvedRoot =
                match DirectoryInfo(root).ResolveLinkTarget(true) with
                | null -> root
                | target -> Path.GetFullPath target.FullName
            let resolvedRootPrefix = resolvedRoot.TrimEnd(Path.DirectorySeparatorChar, Path.AltDirectorySeparatorChar) + string Path.DirectorySeparatorChar

            let relativePath path =
                let full = Path.GetFullPath path
                if not (full.StartsWith(rootPrefix, comparison)) then
                    invalidArg "path" ("source path escapes repository root: " + path)
                let info = FileInfo full
                if not (isNull info.LinkTarget) then
                    match info.ResolveLinkTarget(true) with
                    | null -> invalidArg "path" ("broken source symlink: " + path)
                    | target ->
                        let resolved = Path.GetFullPath target.FullName
                        if not (resolved.Equals(resolvedRoot, comparison)) && not (resolved.StartsWith(resolvedRootPrefix, comparison)) then
                            invalidArg "path" ("source symlink escapes repository root: " + path)
                Path.GetRelativePath(root, full).Replace(Path.DirectorySeparatorChar, '/')

            let diagnostics = List<AdapterDiagnostic>()
            use log = new StringWriter()
            let manager = new AnalyzerManager(AnalyzerManagerOptions(LogWriter = log))
            if not (String.IsNullOrWhiteSpace variant) then manager.SetGlobalProperty("Configuration", variant)
            let analyzer =
                manager.GetProject(IOPath.Parse projectPath)
                |> Option.ofObj
                |> Option.defaultWith (fun () -> failwith ("F# project not found: " + projectPath))
            let results = analyzer.Build() |> Seq.sortBy (fun r -> r.TargetFramework) |> Seq.toArray
            let units = List<UnitFacts>()
            let projectDirectory =
                match Path.GetDirectoryName(Path.GetFullPath projectPath) with
                | null -> invalidArg "projectPath" ("project path has no directory: " + projectPath)
                | directory -> directory
            let absoluteProjectPath (path: string) =
                if Path.IsPathFullyQualified path then Path.GetFullPath path
                else Path.GetFullPath(path, projectDirectory)

            for result in results do
                cancellationToken.ThrowIfCancellationRequested()
                if not result.Succeeded then
                    failwith ("F# design-time build failed for " + projectPath + ": " + log.ToString())
                // Fsc arguments are relative to the project directory, while an adapter can
                // be launched from anywhere. FCS otherwise resolves source paths against the
                // adapter process working directory and silently checks an empty project.
                let sourceArguments = HashSet<string>(result.SourceFiles, StringComparer.Ordinal)
                let compilerArguments =
                    result.CompilerArguments
                    |> Array.map (fun argument ->
                        if sourceArguments.Contains argument && not (Path.IsPathFullyQualified argument) then
                            Path.GetFullPath(argument, projectDirectory)
                        else argument)
                let sourceFiles = result.SourceFiles |> Array.map absoluteProjectPath
                let options = checker.GetProjectOptionsFromCommandLineArgs(projectPath, compilerArguments)
                let! checkedProject = Async.StartAsTask(checker.ParseAndCheckProject(options), cancellationToken = cancellationToken)

                let projectRelative = relativePath projectPath
                let actualVariant = String.Join(";", [| result.TargetFramework; variant |])
                let unitId = "dotnet:fsharp:unit:" + Identity.Hash(repositoryIdentity, projectRelative, actualVariant)
                let projectName =
                    match Path.GetFileNameWithoutExtension projectPath with
                    | null -> invalidArg "projectPath" ("project path has no file name: " + projectPath)
                    | name -> name
                let rootSymbolId = "dotnet:fsharp:project:" + Identity.Hash(repositoryIdentity, projectRelative, projectName, actualVariant)
                let unit = Unit(Id = unitId, Language = "fsharp", Variant = actualVariant)
                let facts = UnitFacts(Unit = unit)
                facts.Symbols.Add(Symbol(
                    Id = rootSymbolId, UnitId = unitId, StableName = projectRelative,
                    DisplayName = projectName,
                    NormalizedName = Identity.NormalizeName projectName, Kind = "project"))

                let documents = Dictionary<string, Document * string>(if OperatingSystem.IsWindows() then StringComparer.OrdinalIgnoreCase else StringComparer.Ordinal)
                for sourceFile in sourceFiles do
                    let relative = relativePath sourceFile
                    let pathParts = relative.Split('/')
                    if pathParts |> Array.exists (fun part -> part = "bin" || part = "obj" || part = ".git") then
                        diagnostics.Add(AdapterDiagnostic("info", "generated/build-output document omitted: " + relative, unitId))
                    else
                        let text = File.ReadAllText sourceFile
                        let document = Document(
                            Id = "dotnet:document:" + Identity.Hash(unitId, relative),
                            UnitId = unitId, Path = relative, Language = "fsharp", ContentHash = Identity.Hash text)
                        documents.[Path.GetFullPath sourceFile] <- (document, text)
                        facts.Documents.Add document

                let rangeOf (range: range) =
                    let file = absoluteProjectPath range.FileName
                    let document, text = documents.[file]
                    let lineStarts = ResizeArray<int>()
                    lineStarts.Add 0
                    for index = 0 to text.Length - 1 do
                        if text.[index] = '\n' then lineStarts.Add(index + 1)
                        elif text.[index] = '\r' && (index + 1 = text.Length || text.[index + 1] <> '\n') then lineStarts.Add(index + 1)
                    let utf8Column line column =
                        if line < 0 || line >= lineStarts.Count then 0
                        else
                            let start = lineStarts.[line]
                            let mutable finish = start
                            while finish < text.Length && text.[finish] <> '\r' && text.[finish] <> '\n' do finish <- finish + 1
                            Encoding.UTF8.GetByteCount(text.AsSpan(start, min column (finish - start)))
                    let byteOffset line column =
                        if line < 0 || line >= lineStarts.Count then 0L
                        else
                            let start = lineStarts.[line]
                            let mutable finish = start
                            while finish < text.Length && text.[finish] <> '\r' && text.[finish] <> '\n' do finish <- finish + 1
                            let characterOffset = start + min column (finish - start)
                            int64 (Encoding.UTF8.GetByteCount(text.AsSpan(0, characterOffset)))
                    let startLine = max 0 (range.StartLine - 1)
                    let endLine = max 0 (range.EndLine - 1)
                    document,
                    SourceRange(
                        Position(startLine, utf8Column startLine range.StartColumn, byteOffset startLine range.StartColumn),
                        Position(endLine, utf8Column endLine range.EndColumn, byteOffset endLine range.EndColumn))

                let stableName (symbol: FSharpSymbol) =
                    symbol.GetType().Name + " " + symbol.DisplayName + " " + symbol.ToString()

                let symbolId (symbol: FSharpSymbol) =
                    let localDiscriminator =
                        match symbol.DeclarationLocation with
                        | Some location -> relativePath (absoluteProjectPath location.FileName) + ":" + string location.StartLine + ":" + string location.StartColumn
                        | None -> "external"
                    "dotnet:fsharp:symbol:" + Identity.Hash(repositoryIdentity, projectRelative, actualVariant, stableName symbol, localDiscriminator)

                let seenSymbols = HashSet<string>(StringComparer.Ordinal)
                let definedSymbols = HashSet<string>(StringComparer.Ordinal)
                let seenOccurrences = HashSet<string>(StringComparer.Ordinal)
                let seenEdges = HashSet<string>(StringComparer.Ordinal)
                let uses = checkedProject.GetAllUsesOfAllSymbols(cancellationToken = cancellationToken) |> Array.sortBy (fun (symbolUse: FSharpSymbolUse) -> symbolUse.Range.FileName, symbolUse.Range.StartLine, symbolUse.Range.StartColumn)
                for symbolUse in uses do
                    let fullFile = absoluteProjectPath symbolUse.Range.FileName
                    match documents.TryGetValue fullFile with
                    | false, _ -> ()
                    | true, _ ->
                        let symbol = symbolUse.Symbol
                        let id = symbolId symbol
                        if symbolUse.IsFromDefinition then definedSymbols.Add id |> ignore
                        let document, sourceRange = rangeOf symbolUse.Range
                        if seenSymbols.Add id then
                            let definitionDocument, definitionRange =
                                match symbol.DeclarationLocation with
                                | Some declaration when documents.ContainsKey(absoluteProjectPath declaration.FileName) -> rangeOf declaration
                                | _ -> document, SourceRange.Empty
                            facts.Symbols.Add(Symbol(
                                Id = id, UnitId = unitId, StableName = stableName symbol,
                                DisplayName = symbol.DisplayName, NormalizedName = Identity.NormalizeName symbol.DisplayName,
                                Kind = symbol.GetType().Name.Replace("FSharp", "").ToLowerInvariant(),
                                DocumentId = (if definitionRange = SourceRange.Empty then "" else definitionDocument.Id),
                                Definition = definitionRange))
                        let role = if symbolUse.IsFromDefinition then "definition" else "reference"
                        let occurrenceKey = String.Join("\000", [| role; id; document.Id; string sourceRange.Start.Byte; string sourceRange.End.Byte |])
                        if seenOccurrences.Add occurrenceKey then
                            facts.Occurrences.Add(Occurrence(
                                Id = "dotnet:occurrence:" + Identity.Hash(unitId, occurrenceKey), UnitId = unitId,
                                SymbolId = id, DocumentId = document.Id, Role = role, Range = sourceRange))
                        let edgeKind = if symbolUse.IsFromDefinition then "defines" else "references"
                        let edgeKey = String.Join("\000", [| edgeKind; rootSymbolId; id; document.Id; string sourceRange.Start.Byte |])
                        if seenEdges.Add edgeKey then
                            facts.Edges.Add(Edge(
                                Id = "dotnet:edge:" + Identity.Hash(unitId, edgeKey), UnitId = unitId,
                                From = rootSymbolId, To = id, Kind = edgeKind, DocumentId = document.Id, Range = sourceRange))

                for dependency in result.ProjectReferences |> Seq.sort do
                    let dependencyRelative = relativePath dependency
                    let language = if String.Equals(Path.GetExtension dependency, ".fsproj", StringComparison.OrdinalIgnoreCase) then "fsharp" else "csharp"
                    let dependencyId = "dotnet:" + language + ":project:" + Identity.Hash(repositoryIdentity, dependencyRelative, Path.GetFileNameWithoutExtension dependency)
                    let key = "depends-on\000" + rootSymbolId + "\000" + dependencyId
                    if seenEdges.Add key then
                        facts.Edges.Add(Edge(
                            Id = "dotnet:edge:" + Identity.Hash(unitId, key), UnitId = unitId,
                            From = rootSymbolId, To = dependencyId, Kind = "depends-on"))

                for diagnostic in checkedProject.Diagnostics |> Seq.truncate 100 do
                    let severity = if diagnostic.Severity.ToString() = "Error" then "error" else "warning"
                    diagnostics.Add(AdapterDiagnostic(severity, string diagnostic, unitId))

                let sortById (values: List<'T>) (id: 'T -> string) = values.Sort(Comparison<'T>(fun a b -> StringComparer.Ordinal.Compare(id a, id b)))
                facts.Documents.Sort(Comparison<Document>(fun a b -> StringComparer.Ordinal.Compare(a.Path, b.Path)))
                sortById facts.Symbols (fun value -> value.Id)
                sortById facts.Occurrences (fun value -> value.Id)
                sortById facts.Edges (fun value -> value.Id)
                unit.InputFingerprint <- Identity.Hash(
                    "weave-dotnet-fsharp-input/v1", projectRelative, actualVariant,
                    String.Join("\n", sourceFiles), String.Join("\n", compilerArguments),
                    String.Join("\n", facts.Documents |> Seq.map (fun d -> d.Path + "\000" + d.ContentHash)))
                unit.SurfaceFingerprint <- Identity.Hash(
                    "weave-dotnet-fsharp-surface/v1",
                    String.Join("\n", facts.Symbols |> Seq.filter (fun symbol -> definedSymbols.Contains symbol.Id) |> Seq.map (fun symbol -> symbol.StableName) |> Seq.sort))
                unit.InventoryDigest <- Identity.Hash(
                    "weave-dotnet-inventory/v1",
                    String.Join("\n", facts.Symbols |> Seq.map (fun value -> value.Id)),
                    String.Join("\n", facts.Occurrences |> Seq.map (fun value -> value.Id)),
                    String.Join("\n", facts.Edges |> Seq.map (fun value -> value.Id)))
                units.Add facts

            diagnostics.Add(AdapterDiagnostic("info", "F# static call edges are not emitted by the initial FCS slice"))
            return IndexBatch(units, diagnostics)
        }
