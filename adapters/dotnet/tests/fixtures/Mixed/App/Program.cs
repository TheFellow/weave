using Fixture.Core;
using Fixture.FSharp;

IGreeter greeter = new Greeter();
Console.WriteLine(greeter.Greet(42));
Console.WriteLine(Api.greet(greeter));
