using System.Security.Cryptography;
using System.Text;

namespace Weave.Adapter.Model;

public static class Identity
{
    public static string Hash(params string?[] values)
    {
        using var hash = IncrementalHash.CreateHash(HashAlgorithmName.SHA256);
        foreach (var value in values)
        {
            var bytes = Encoding.UTF8.GetBytes(value ?? "");
            hash.AppendData(BitConverter.GetBytes(bytes.Length));
            hash.AppendData(bytes);
        }
        return Convert.ToHexString(hash.GetHashAndReset()).ToLowerInvariant();
    }

    public static string NormalizeName(string value) => value.Normalize(NormalizationForm.FormKC).ToUpperInvariant();
}
