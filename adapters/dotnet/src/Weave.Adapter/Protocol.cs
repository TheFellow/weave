using System.Text.Json;
using System.Text.Json.Serialization;
using Weave.Adapter.Model;

namespace Weave.Adapter;

public sealed record Permissions(
    [property: JsonPropertyName("network")] bool Network,
    [property: JsonPropertyName("restore")] bool Restore,
    [property: JsonPropertyName("build_tool")] bool BuildTool,
    [property: JsonPropertyName("run_generators")] bool RunGenerators);

public sealed record RequestLimits(
    [property: JsonPropertyName("max_frame_bytes")] long MaxFrameBytes,
    [property: JsonPropertyName("max_total_bytes")] long MaxTotalBytes,
    [property: JsonPropertyName("max_frames")] int MaxFrames,
    [property: JsonPropertyName("max_facts")] int MaxFacts);

public sealed record IndexRequest
{
    [JsonPropertyName("protocol")] public required string Protocol { get; init; }
    [JsonPropertyName("request_id")] public required string RequestId { get; init; }
    [JsonPropertyName("repository_root")] public required string RepositoryRoot { get; init; }
    [JsonPropertyName("repository_identity")] public string RepositoryIdentity { get; init; } = "";
    [JsonPropertyName("variant")] public string Variant { get; init; } = "";
    [JsonPropertyName("changed_paths")] public string[] ChangedPaths { get; init; } = [];
    [JsonPropertyName("input_paths")] public string[] InputPaths { get; init; } = [];
    [JsonPropertyName("environment")] public Dictionary<string, string> Environment { get; init; } = [];
    [JsonPropertyName("permissions")] public required Permissions Permissions { get; init; }
    [JsonPropertyName("limits")] public required RequestLimits Limits { get; init; }
}

public sealed class ProtocolWriter(TextWriter output, IndexRequest request)
{
    private static readonly JsonSerializerOptions Json = new()
    {
        DefaultIgnoreCondition = JsonIgnoreCondition.WhenWritingNull,
        PropertyNamingPolicy = JsonNamingPolicy.SnakeCaseLower,
    };
    private long totalBytes;
    private int frames;

    public void RunBegin() => Write("run.begin", new
    {
        provider = new { name = Constants.Provider, version = Constants.Version },
        fact_encoding = Constants.FactEncoding,
    });

    public void Diagnostic(AdapterDiagnostic value) => Write("diagnostic", new
    {
        severity = value.Severity,
        message = value.Message,
        unit_id = value.UnitId,
    });

    public void Unit(UnitFacts facts)
    {
        Write("unit.begin", new { unit = facts.Unit });
        foreach (var batch in Batches(facts)) Write("facts", batch);
        Write("unit.end", new
        {
            status = "complete",
            counts = new
            {
                documents = facts.Documents.Count,
                symbols = facts.Symbols.Count,
                occurrences = facts.Occurrences.Count,
                edges = facts.Edges.Count,
            },
        });
    }

    public void RunEnd(IEnumerable<string> units) => Write("run.end", new
    {
        status = "complete",
        units = units.Order(StringComparer.Ordinal).ToArray(),
    });

    private IEnumerable<object> Batches(UnitFacts facts)
    {
        const int batchSize = 250;
        for (var i = 0; i < facts.Documents.Count; i += batchSize)
            yield return new { documents = facts.Documents.Skip(i).Take(batchSize).ToArray() };
        for (var i = 0; i < facts.Symbols.Count; i += batchSize)
            yield return new { symbols = facts.Symbols.Skip(i).Take(batchSize).ToArray() };
        for (var i = 0; i < facts.Occurrences.Count; i += batchSize)
            yield return new { occurrences = facts.Occurrences.Skip(i).Take(batchSize).ToArray() };
        for (var i = 0; i < facts.Edges.Count; i += batchSize)
            yield return new { edges = facts.Edges.Skip(i).Take(batchSize).ToArray() };
    }

    private void Write(string kind, object payload)
    {
        var line = JsonSerializer.Serialize(new
        {
            protocol = Constants.Protocol,
            request_id = request.RequestId,
            kind,
            payload,
        }, Json);
        var bytes = System.Text.Encoding.UTF8.GetByteCount(line) + System.Text.Encoding.UTF8.GetByteCount(output.NewLine);
        if (bytes > request.Limits.MaxFrameBytes) throw new InvalidOperationException($"{kind} frame exceeds negotiated limit");
        totalBytes += bytes;
        if (totalBytes > request.Limits.MaxTotalBytes) throw new InvalidOperationException("output exceeds negotiated byte limit");
        if (++frames > request.Limits.MaxFrames) throw new InvalidOperationException("output exceeds negotiated frame limit");
        output.WriteLine(line);
    }
}

public static class Protocol
{
    private static readonly JsonSerializerOptions StrictJson = new()
    {
        UnmappedMemberHandling = JsonUnmappedMemberHandling.Disallow,
        PropertyNameCaseInsensitive = false,
    };

    public static IndexRequest ReadRequest(TextReader input)
    {
        var body = input.ReadToEnd();
        if (body.Length == 0 || body.Length > 4 * 1024 * 1024) throw new InvalidDataException("exactly one bounded JSON request is required");
        var request = JsonSerializer.Deserialize<IndexRequest>(body, StrictJson) ?? throw new InvalidDataException("request is null");
        if (request.Protocol != Constants.Protocol || string.IsNullOrWhiteSpace(request.RequestId)) throw new InvalidDataException("unsupported protocol or empty request ID");
        if (request.Limits.MaxFrameBytes <= 0 || request.Limits.MaxTotalBytes <= 0 || request.Limits.MaxFrames <= 0 || request.Limits.MaxFacts <= 0)
            throw new InvalidDataException("request limits must be positive");
        return request;
    }
}
