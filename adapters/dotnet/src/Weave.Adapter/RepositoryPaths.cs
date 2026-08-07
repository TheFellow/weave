namespace Weave.Adapter;

public sealed class RepositoryPaths
{
    public string Root { get; }
    private readonly string rootPrefix;
    private readonly StringComparison comparison = OperatingSystem.IsWindows() ? StringComparison.OrdinalIgnoreCase : StringComparison.Ordinal;

    public RepositoryPaths(string root)
    {
        Root = Path.GetFullPath(root);
        if (!Directory.Exists(Root)) throw new DirectoryNotFoundException($"repository root does not exist: {Root}");
        rootPrefix = Root.TrimEnd(Path.DirectorySeparatorChar, Path.AltDirectorySeparatorChar) + Path.DirectorySeparatorChar;
    }

    public string RelativeFile(string path)
    {
        var full = Path.GetFullPath(path);
        if (!full.StartsWith(rootPrefix, comparison)) throw new InvalidDataException($"source path escapes repository root: {path}");
        var relative = Path.GetRelativePath(Root, full).Replace(Path.DirectorySeparatorChar, '/');
        if (relative == ".." || relative.StartsWith("../", StringComparison.Ordinal)) throw new InvalidDataException($"source path escapes repository root: {path}");
        return relative;
    }
}
