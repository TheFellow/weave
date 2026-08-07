use std::fs;
use std::io::Write;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};

use protobuf::{Enum, Message, MessageField};
use scip::types::{
    symbol_information, Document, Index, Metadata, Occurrence, PositionEncoding, Relationship,
    SymbolInformation, SymbolRole, ToolInfo,
};
use serde_json::{json, Value};
use tempfile::TempDir;
use weave_rust_adapter::normalize_index;

const TRAIT: &str = "rust-analyzer cargo weave-rust-fixture 0.1.0 Greeter#";
const STRUCT: &str = "rust-analyzer cargo weave-rust-fixture 0.1.0 ConsoleGreeter#";
const FUNCTION: &str = "rust-analyzer cargo weave-rust-fixture 0.1.0 greet().";

#[test]
fn normalizes_compiler_semantics_without_inventing_calls() {
    let root = fixture_root();
    let units = normalize_index(
        &fixture_index(),
        &root,
        "example.com/weave-rust-fixture",
        "test-provider",
        "test",
        1000,
    )
    .unwrap();

    assert_eq!(units.len(), 1);
    let facts = &units[0];
    assert_eq!(facts.documents[0].path, "src/lib.rs");
    assert_eq!(facts.symbols.len(), 4);
    assert!(facts
        .symbols
        .iter()
        .all(|symbol| symbol.normalized_name.is_empty()));
    assert!(facts.symbols.iter().any(|symbol| {
        symbol.display_name == "greet"
            && symbol.kind == "function"
            && symbol.definition.start.line == 12
    }));
    assert_eq!(
        facts
            .occurrences
            .iter()
            .filter(|occurrence| occurrence.role == "definition")
            .count(),
        4
    );
    assert!(facts
        .occurrences
        .iter()
        .any(|occurrence| occurrence.role == "reference"));
    assert_eq!(facts.edges.len(), 1);
    assert_eq!(facts.edges[0].kind, "implements");
    assert!(facts
        .edges
        .iter()
        .all(|edge| edge.kind != "calls" && edge.evidence == "exact"));
    assert!(facts.unit.input_fingerprint.starts_with("sha256:"));
    assert!(facts.unit.surface_fingerprint.starts_with("sha256:"));
    assert!(facts.unit.inventory_digest.starts_with("sha256:"));
}

#[test]
fn process_contract_enforces_permissions_and_frame_bounds() {
    let temporary = TempDir::new().unwrap();
    let analyzer = compile_fake_analyzer(temporary.path());
    let scip_path = temporary.path().join("fixture.scip");
    fs::write(&scip_path, fixture_index().write_to_bytes().unwrap()).unwrap();
    let binary = env!("CARGO_BIN_EXE_weave-rust");

    let described = Command::new(binary)
        .args(["describe", "--protocol", "weave.adapter/v0"])
        .env("WEAVE_RUST_ANALYZER", &analyzer)
        .output()
        .unwrap();
    assert!(described.status.success(), "{:?}", described);
    let capabilities: Value = serde_json::from_slice(&described.stdout).unwrap();
    assert_eq!(capabilities["provider"]["name"], "weave-rust");
    assert_eq!(capabilities["languages"], json!(["rust"]));
    assert_eq!(capabilities["requires"]["may_run_build_tool"], true);

    let denied_request = request(false, 4096);
    let denied = run_index(binary, &analyzer, &scip_path, denied_request);
    assert!(!denied.status.success());
    assert!(denied.stdout.is_empty());
    assert!(String::from_utf8_lossy(&denied.stderr).contains("permissions.build_tool=true"));

    let allowed = run_index(binary, &analyzer, &scip_path, request(true, 1200));
    assert!(
        allowed.status.success(),
        "stdout:\n{}\nstderr:\n{}",
        String::from_utf8_lossy(&allowed.stdout),
        String::from_utf8_lossy(&allowed.stderr)
    );
    let frames: Vec<Value> = allowed
        .stdout
        .split(|byte| *byte == b'\n')
        .filter(|line| !line.is_empty())
        .map(|line| {
            assert!(line.len() < 1200, "oversized frame: {}", line.len() + 1);
            serde_json::from_slice(line).unwrap()
        })
        .collect();
    assert_eq!(frames.first().unwrap()["kind"], "run.begin");
    assert_eq!(frames.last().unwrap()["kind"], "run.end");
    assert!(frames
        .iter()
        .all(|frame| frame["request_id"] == "rust-process-test"));
    assert!(
        frames
            .iter()
            .filter(|frame| frame["kind"] == "facts")
            .count()
            > 4
    );
    let unit_end = frames
        .iter()
        .find(|frame| frame["kind"] == "unit.end")
        .unwrap();
    assert_eq!(unit_end["payload"]["counts"]["documents"], 1);
    assert_eq!(unit_end["payload"]["counts"]["symbols"], 4);
    assert_eq!(unit_end["payload"]["counts"]["occurrences"], 6);
    assert_eq!(unit_end["payload"]["counts"]["edges"], 1);
}

fn fixture_root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("tests/fixtures/sample")
        .canonicalize()
        .unwrap()
}

fn fixture_index() -> Index {
    let occurrences = vec![
        occurrence(0, 10, 17, TRAIT, true),
        occurrence(4, 11, 25, STRUCT, true),
        occurrence(6, 5, 12, TRAIT, false),
        occurrence(6, 17, 31, STRUCT, false),
        occurrence(12, 7, 12, FUNCTION, true),
        occurrence(12, 13, 17, "local 0", true),
    ];
    let symbols = vec![
        symbol(TRAIT, "Greeter", symbol_information::Kind::Trait, vec![]),
        symbol(
            STRUCT,
            "ConsoleGreeter",
            symbol_information::Kind::Struct,
            vec![Relationship {
                symbol: TRAIT.to_owned(),
                is_implementation: true,
                ..Default::default()
            }],
        ),
        symbol(
            FUNCTION,
            "greet",
            symbol_information::Kind::Function,
            vec![],
        ),
        symbol(
            "local 0",
            "name",
            symbol_information::Kind::Parameter,
            vec![],
        ),
    ];
    Index {
        metadata: MessageField::some(Metadata {
            tool_info: MessageField::some(ToolInfo {
                name: "rust-analyzer".to_owned(),
                version: "fixture-ra".to_owned(),
                ..Default::default()
            }),
            project_root: "file:///fixture".to_owned(),
            ..Default::default()
        }),
        documents: vec![Document {
            language: "rust".to_owned(),
            relative_path: "src/lib.rs".to_owned(),
            occurrences,
            symbols,
            position_encoding: PositionEncoding::UTF8CodeUnitOffsetFromLineStart.into(),
            ..Default::default()
        }],
        ..Default::default()
    }
}

fn occurrence(line: i32, start: i32, end: i32, symbol: &str, definition: bool) -> Occurrence {
    Occurrence {
        range: vec![line, start, end],
        symbol: symbol.to_owned(),
        symbol_roles: if definition {
            SymbolRole::Definition.value()
        } else {
            0
        },
        ..Default::default()
    }
}

fn symbol(
    name: &str,
    display_name: &str,
    kind: symbol_information::Kind,
    relationships: Vec<Relationship>,
) -> SymbolInformation {
    SymbolInformation {
        symbol: name.to_owned(),
        display_name: display_name.to_owned(),
        kind: kind.into(),
        relationships,
        ..Default::default()
    }
}

fn request(build_tool: bool, max_frame_bytes: usize) -> Value {
    json!({
        "protocol": "weave.adapter/v0",
        "request_id": "rust-process-test",
        "repository_root": fixture_root(),
        "repository_identity": "example.com/weave-rust-fixture",
        "variant": "test",
        "changed_paths": [],
        "environment": {},
        "permissions": {
            "network": false,
            "restore": false,
            "build_tool": build_tool,
            "run_generators": false
        },
        "limits": {
            "max_frame_bytes": max_frame_bytes,
            "max_total_bytes": 4 << 20,
            "max_frames": 1000,
            "max_facts": 1000
        }
    })
}

fn run_index(
    binary: &str,
    analyzer: &Path,
    scip_path: &Path,
    request: Value,
) -> std::process::Output {
    let mut process = Command::new(binary)
        .args(["index", "--protocol", "weave.adapter/v0"])
        .env("WEAVE_RUST_ANALYZER", analyzer)
        .env("WEAVE_RUST_TEST_SCIP", scip_path)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .unwrap();
    process
        .stdin
        .take()
        .unwrap()
        .write_all(serde_json::to_string(&request).unwrap().as_bytes())
        .unwrap();
    process.wait_with_output().unwrap()
}

fn compile_fake_analyzer(directory: &Path) -> PathBuf {
    let source = directory.join("fake_rust_analyzer.rs");
    let executable = directory.join(if cfg!(windows) {
        "fake-rust-analyzer.exe"
    } else {
        "fake-rust-analyzer"
    });
    fs::write(
        &source,
        r#"
use std::{env, fs, process};

fn main() {
    let args: Vec<String> = env::args().skip(1).collect();
    if args == ["--version"] {
        println!("rust-analyzer fixture 0.1");
        return;
    }
    if args.first().map(String::as_str) != Some("scip") {
        eprintln!("expected scip command: {args:?}");
        process::exit(2);
    }
    if env::var("CARGO_NET_OFFLINE").as_deref() != Ok("true") {
        eprintln!("offline policy was not applied");
        process::exit(3);
    }
    let config_at = args.iter().position(|arg| arg == "--config-path").unwrap();
    let config = fs::read_to_string(&args[config_at + 1]).unwrap();
    if config.matches("\"enable\":false").count() != 2 {
        eprintln!("generators were not disabled: {config}");
        process::exit(4);
    }
    let output_at = args.iter().position(|arg| arg == "--output").unwrap();
    fs::copy(env::var_os("WEAVE_RUST_TEST_SCIP").unwrap(), &args[output_at + 1]).unwrap();
}
"#,
    )
    .unwrap();
    let status = Command::new("rustc")
        .arg(&source)
        .arg("--edition=2021")
        .arg("-o")
        .arg(&executable)
        .status()
        .unwrap();
    assert!(status.success());
    executable
}
