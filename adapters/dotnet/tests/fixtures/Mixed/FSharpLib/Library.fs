namespace Fixture.FSharp

open Fixture.Core

type LoudGreeter() =
    interface IGreeter with
        member _.Greet<'T>(value: 'T) = "LOUD " + string value

module Api =
    let greet (greeter: IGreeter) = greeter.Greet("weave")
