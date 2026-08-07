use std::env;
use std::io::{self, Read};
use std::process::ExitCode;

use weave_rust_adapter::{describe, index, AdapterError, PROTOCOL};

fn main() -> ExitCode {
    match run() {
        Ok(()) => ExitCode::SUCCESS,
        Err(error) => {
            eprintln!("weave-rust: {error}");
            ExitCode::FAILURE
        }
    }
}

fn run() -> Result<(), AdapterError> {
    let arguments: Vec<String> = env::args().skip(1).collect();
    if arguments.len() != 3 || arguments[1] != "--protocol" || arguments[2] != PROTOCOL {
        return Err(AdapterError::new(
            "usage: weave-rust <describe|index> --protocol weave.adapter/v0",
        ));
    }
    let analyzer = env::var_os("WEAVE_RUST_ANALYZER").unwrap_or_else(|| "rust-analyzer".into());
    match arguments[0].as_str() {
        "describe" => describe(&analyzer, &mut io::stdout()),
        "index" => {
            let mut input = Vec::new();
            io::stdin()
                .take((4 << 20) + 1)
                .read_to_end(&mut input)
                .map_err(|error| AdapterError::wrap("read index request", error))?;
            if input.len() > 4 << 20 {
                return Err(AdapterError::new("index request exceeds 4 MiB"));
            }
            index(&analyzer, &input, &mut io::stdout())
        }
        _ => Err(AdapterError::new(
            "usage: weave-rust <describe|index> --protocol weave.adapter/v0",
        )),
    }
}
