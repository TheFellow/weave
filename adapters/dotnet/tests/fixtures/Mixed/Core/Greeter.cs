namespace Fixture.Core;

public interface IGreeter
{
    string Greet<T>(T value);
}

public abstract class GreeterBase
{
    protected static string Format(object? value) => $"hello {value}";
}

public sealed class Greeter : GreeterBase, IGreeter
{
    public string Greet<T>(T value) => Format(value);
}
