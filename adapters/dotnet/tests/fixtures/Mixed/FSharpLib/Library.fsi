namespace Fixture.FSharp

open Fixture.Core

type LoudGreeter =
    new: unit -> LoudGreeter
    interface IGreeter

module Api =
    val greet: greeter: IGreeter -> string
