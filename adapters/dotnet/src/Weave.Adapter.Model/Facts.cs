using System.Text.Json.Serialization;

namespace Weave.Adapter.Model;

public sealed record Position(
    [property: JsonPropertyName("line")] int Line,
    [property: JsonPropertyName("column")] int Column,
    [property: JsonPropertyName("byte")] long Byte = -1);

public sealed record SourceRange(
    [property: JsonPropertyName("start")] Position Start,
    [property: JsonPropertyName("end")] Position End)
{
    public static SourceRange Empty { get; } = new(new(0, 0), new(0, 0));
}

public sealed record Unit
{
    [JsonPropertyName("id")] public required string Id { get; init; }
    [JsonPropertyName("provider")] public string Provider { get; init; } = Constants.Provider;
    [JsonPropertyName("provider_version")] public string ProviderVersion { get; init; } = Constants.Version;
    [JsonPropertyName("language")] public required string Language { get; init; }
    [JsonPropertyName("variant")] public string Variant { get; init; } = "default";
    [JsonPropertyName("input_fingerprint")] public string InputFingerprint { get; set; } = "";
    [JsonPropertyName("surface_fingerprint")] public string SurfaceFingerprint { get; set; } = "";
    [JsonPropertyName("inventory_digest")] public string InventoryDigest { get; set; } = "";
}

public sealed record Document
{
    [JsonPropertyName("id")] public required string Id { get; init; }
    [JsonPropertyName("unit_id")] public required string UnitId { get; init; }
    [JsonPropertyName("path")] public required string Path { get; init; }
    [JsonPropertyName("language")] public required string Language { get; init; }
    [JsonPropertyName("content_hash")] public string ContentHash { get; init; } = "";
    [JsonPropertyName("provider")] public string Provider { get; init; } = Constants.Provider;
    [JsonPropertyName("provider_version")] public string ProviderVersion { get; init; } = Constants.Version;
}

public sealed record Symbol
{
    [JsonPropertyName("id")] public required string Id { get; init; }
    [JsonPropertyName("unit_id")] public required string UnitId { get; init; }
    [JsonPropertyName("stable_name")] public required string StableName { get; init; }
    [JsonPropertyName("display_name")] public required string DisplayName { get; init; }
    [JsonPropertyName("normalized_name")] public required string NormalizedName { get; init; }
    [JsonPropertyName("kind")] public required string Kind { get; init; }
    [JsonPropertyName("document_id")] public string DocumentId { get; init; } = "";
    [JsonPropertyName("definition")] public SourceRange Definition { get; init; } = SourceRange.Empty;
    [JsonPropertyName("provider")] public string Provider { get; init; } = Constants.Provider;
    [JsonPropertyName("evidence")] public string Evidence { get; init; } = "exact";
}

public sealed record Occurrence
{
    [JsonPropertyName("id")] public required string Id { get; init; }
    [JsonPropertyName("unit_id")] public required string UnitId { get; init; }
    [JsonPropertyName("symbol_id")] public required string SymbolId { get; init; }
    [JsonPropertyName("document_id")] public required string DocumentId { get; init; }
    [JsonPropertyName("role")] public required string Role { get; init; }
    [JsonPropertyName("range")] public required SourceRange Range { get; init; }
    [JsonPropertyName("provider")] public string Provider { get; init; } = Constants.Provider;
    [JsonPropertyName("evidence")] public string Evidence { get; init; } = "exact";
}

public sealed record Edge
{
    [JsonPropertyName("id")] public required string Id { get; init; }
    [JsonPropertyName("unit_id")] public required string UnitId { get; init; }
    [JsonPropertyName("from")] public required string From { get; init; }
    [JsonPropertyName("to")] public required string To { get; init; }
    [JsonPropertyName("kind")] public required string Kind { get; init; }
    [JsonPropertyName("evidence")] public string Evidence { get; init; } = "exact";
    [JsonPropertyName("document_id")] public string DocumentId { get; init; } = "";
    [JsonPropertyName("range")] public SourceRange Range { get; init; } = SourceRange.Empty;
    [JsonPropertyName("provider")] public string Provider { get; init; } = Constants.Provider;
}

public sealed record UnitFacts
{
    [JsonPropertyName("unit")] public required Unit Unit { get; init; }
    [JsonPropertyName("documents")] public List<Document> Documents { get; init; } = [];
    [JsonPropertyName("symbols")] public List<Symbol> Symbols { get; init; } = [];
    [JsonPropertyName("occurrences")] public List<Occurrence> Occurrences { get; init; } = [];
    [JsonPropertyName("edges")] public List<Edge> Edges { get; init; } = [];
}

public sealed record AdapterDiagnostic(string Severity, string Message, string? UnitId = null);

public sealed record IndexBatch(List<UnitFacts> Units, List<AdapterDiagnostic> Diagnostics);

public static class Constants
{
    public const string Protocol = "weave.adapter/v0";
    public const string FactEncoding = "weave.facts/v0";
    public const string Provider = "weave-dotnet";
    public const string Version = "0.1.0";
}
