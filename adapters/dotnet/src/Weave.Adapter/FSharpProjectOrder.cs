using Microsoft.Build.Evaluation;

namespace Weave.Adapter;

/// <summary>Orders F# projects so a design-time clean cannot remove a dependency
/// output before its dependents have consumed it.</summary>
public static class FSharpProjectOrder
{
    public static IReadOnlyList<string> DependentsFirst(IEnumerable<string> projectPaths, string? configuration = null)
    {
        var comparer = OperatingSystem.IsWindows() ? StringComparer.OrdinalIgnoreCase : StringComparer.Ordinal;
        var projects = projectPaths.Select(Path.GetFullPath).Distinct(comparer).Order(comparer).ToArray();
        var known = projects.ToHashSet(comparer);
        var dependencies = projects.ToDictionary(path => path, _ => new HashSet<string>(comparer), comparer);
        var incoming = projects.ToDictionary(path => path, _ => 0, comparer);
        var properties = new Dictionary<string, string>(StringComparer.OrdinalIgnoreCase);
        if (!string.IsNullOrWhiteSpace(configuration)) properties["Configuration"] = configuration;

        using var collection = new ProjectCollection(properties);
        foreach (var path in projects)
        {
            var project = collection.LoadProject(path);
            var directory = Path.GetDirectoryName(path) ?? throw new InvalidOperationException($"project path has no directory: {path}");
            foreach (var item in project.GetItems("ProjectReference"))
            {
                var dependency = Path.GetFullPath(item.EvaluatedInclude, directory);
                if (!known.Contains(dependency) || !dependencies[path].Add(dependency)) continue;
                incoming[dependency]++;
            }
        }

        var ready = new SortedSet<string>(projects.Where(path => incoming[path] == 0), comparer);
        var ordered = new List<string>(projects.Length);
        while (ready.Count != 0)
        {
            var path = ready.Min!;
            ready.Remove(path);
            ordered.Add(path);
            foreach (var dependency in dependencies[path].Order(comparer))
                if (--incoming[dependency] == 0) ready.Add(dependency);
        }
        if (ordered.Count != projects.Length)
            throw new InvalidOperationException("F# project references contain a cycle");
        return ordered;
    }
}
