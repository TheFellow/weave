namespace Weave.Adapter;

public sealed class RepositoryPaths
{
    public string Root { get; }
    private readonly string rootPrefix;
    private readonly string resolvedRoot;
    private readonly string resolvedRootPrefix;
    private readonly StringComparison comparison = OperatingSystem.IsWindows() ? StringComparison.OrdinalIgnoreCase : StringComparison.Ordinal;

    public RepositoryPaths(string root)
    {
        Root = Path.GetFullPath(root);
        if (!Directory.Exists(Root)) throw new DirectoryNotFoundException($"repository root does not exist: {Root}");
        rootPrefix = Root.TrimEnd(Path.DirectorySeparatorChar, Path.AltDirectorySeparatorChar) + Path.DirectorySeparatorChar;
        resolvedRoot = Path.GetFullPath(new DirectoryInfo(Root).ResolveLinkTarget(true)?.FullName ?? Root);
        resolvedRootPrefix = resolvedRoot.TrimEnd(Path.DirectorySeparatorChar, Path.AltDirectorySeparatorChar) + Path.DirectorySeparatorChar;
    }

    public string RelativeFile(string path)
    {
        var full = Path.GetFullPath(path);
        if (!full.StartsWith(rootPrefix, comparison)) throw new InvalidDataException($"source path escapes repository root: {path}");
        var relative = Path.GetRelativePath(Root, full).Replace(Path.DirectorySeparatorChar, '/');
        if (relative == ".." || relative.StartsWith("../", StringComparison.Ordinal)) throw new InvalidDataException($"source path escapes repository root: {path}");
        EnsureLinksStayInside(relative, path);
        return relative;
    }

    private void EnsureLinksStayInside(string relative, string original)
    {
        var current = Root;
        foreach (var part in relative.Split('/'))
        {
            current = Path.Combine(current, part);
            FileSystemInfo info = Directory.Exists(current) ? new DirectoryInfo(current) : new FileInfo(current);
            if (info.LinkTarget is null) continue;
            var target = info.ResolveLinkTarget(true) ?? throw new InvalidDataException($"broken source symlink: {original}");
            var resolved = Path.GetFullPath(target.FullName);
            if (!resolved.Equals(resolvedRoot, comparison) && !resolved.StartsWith(resolvedRootPrefix, comparison))
                throw new InvalidDataException($"source symlink escapes repository root: {original}");
        }
    }
}
