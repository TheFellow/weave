//! `weave.adapter/v0` bridge from rust-analyzer's SCIP export to Weave facts.

use std::collections::{BTreeMap, BTreeSet};
use std::ffi::OsStr;
use std::fmt::{self, Display};
use std::fs;
use std::io::{Read, Write};
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::thread;

use protobuf::{Enum, Message};
use scip::types::{self as scip_types, occurrence};
use serde::{Deserialize, Serialize};
use serde_json::json;
use sha2::{Digest, Sha256};

pub const PROTOCOL: &str = "weave.adapter/v0";
const FACT_ENCODING: &str = "weave.facts/v0";
const PROVIDER: &str = "weave-rust";
const MAX_INDEX_BYTES: u64 = 256 << 20;
const MAX_SOURCE_BYTES: u64 = 16 << 20;
const MAX_TOTAL_SOURCE_BYTES: u64 = 512 << 20;
const MAX_PROBE_BYTES: usize = 16 << 10;
const FACTS_PER_FRAME: usize = 256;

pub type Result<T> = std::result::Result<T, AdapterError>;

#[derive(Debug)]
pub struct AdapterError(String);

impl AdapterError {
    pub fn new(message: impl Into<String>) -> Self {
        Self(message.into())
    }

    pub fn wrap(context: &str, error: impl Display) -> Self {
        Self(format!("{context}: {error}"))
    }
}

impl Display for AdapterError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        self.0.fmt(formatter)
    }
}

impl std::error::Error for AdapterError {}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct IndexRequest {
    protocol: String,
    request_id: String,
    repository_root: String,
    #[serde(default)]
    repository_identity: String,
    #[serde(default)]
    variant: String,
    #[serde(default)]
    changed_paths: Vec<String>,
    #[serde(default)]
    environment: BTreeMap<String, String>,
    permissions: Permissions,
    limits: RequestLimits,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct Permissions {
    network: bool,
    restore: bool,
    build_tool: bool,
    run_generators: bool,
}

#[derive(Debug, Deserialize, Clone, Copy)]
#[serde(deny_unknown_fields)]
struct RequestLimits {
    max_frame_bytes: u64,
    max_total_bytes: u64,
    max_frames: usize,
    max_facts: usize,
}

#[derive(Debug, Clone, Serialize)]
struct Provider<'a> {
    name: &'a str,
    version: &'a str,
}

#[derive(Debug, Clone, Serialize)]
pub struct Unit {
    pub id: String,
    pub provider: String,
    pub provider_version: String,
    pub language: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub variant: String,
    pub input_fingerprint: String,
    pub surface_fingerprint: String,
    pub inventory_digest: String,
}

#[derive(Debug, Clone, Serialize)]
pub struct Document {
    pub id: String,
    pub unit_id: String,
    pub path: String,
    pub language: String,
    pub content_hash: String,
    pub provider: String,
    pub provider_version: String,
}

#[derive(Debug, Clone, Default, Eq, PartialEq, Ord, PartialOrd, Serialize)]
pub struct Position {
    pub line: i32,
    pub column: i32,
    pub byte: i64,
}

#[derive(Debug, Clone, Default, Eq, PartialEq, Ord, PartialOrd, Serialize)]
pub struct SourceRange {
    pub start: Position,
    pub end: Position,
}

#[derive(Debug, Clone, Serialize)]
pub struct Symbol {
    pub id: String,
    pub unit_id: String,
    pub stable_name: String,
    pub display_name: String,
    pub normalized_name: String,
    pub kind: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub document_id: String,
    pub definition: SourceRange,
    pub provider: String,
    pub evidence: &'static str,
}

#[derive(Debug, Clone, Serialize)]
pub struct Occurrence {
    pub id: String,
    pub unit_id: String,
    pub symbol_id: String,
    pub document_id: String,
    pub role: &'static str,
    pub range: SourceRange,
    pub provider: String,
    pub evidence: &'static str,
}

#[derive(Debug, Clone, Serialize)]
pub struct Edge {
    pub id: String,
    pub unit_id: String,
    pub from: String,
    pub to: String,
    pub kind: &'static str,
    pub evidence: &'static str,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub document_id: String,
    pub range: SourceRange,
    pub provider: String,
}

#[derive(Debug, Clone, Serialize)]
pub struct UnitFacts {
    pub unit: Unit,
    pub documents: Vec<Document>,
    pub symbols: Vec<Symbol>,
    pub occurrences: Vec<Occurrence>,
    pub edges: Vec<Edge>,
}

#[derive(Debug)]
struct RawOccurrence {
    symbol: String,
    symbol_id: String,
    role: &'static str,
    range: SourceRange,
}

pub fn describe(analyzer: &OsStr, output: &mut dyn Write) -> Result<()> {
    let directory = std::env::current_dir()
        .map_err(|error| AdapterError::wrap("resolve current directory", error))?;
    let toolchain = toolchain_identity(analyzer, &directory)?;
    let version = provider_version(&toolchain);
    serde_json::to_writer(
        &mut *output,
        &json!({
            "protocols": [PROTOCOL],
            "provider": {"name": PROVIDER, "version": version},
            "languages": ["rust"],
            "operations": ["index"],
            "refresh_modes": ["full"],
            "fact_encoding": FACT_ENCODING,
            "position_encodings": ["utf8-byte"],
            "requires": {
                "executables": ["rust-analyzer", "cargo", "rustc"],
                "may_run_build_tool": true
            }
        }),
    )
    .map_err(|error| AdapterError::wrap("encode capabilities", error))?;
    output
        .write_all(b"\n")
        .map_err(|error| AdapterError::wrap("write capabilities", error))
}

pub fn index(analyzer: &OsStr, input: &[u8], output: &mut dyn Write) -> Result<()> {
    let request: IndexRequest = serde_json::from_slice(input)
        .map_err(|error| AdapterError::wrap("decode index request", error))?;
    validate_request(&request)?;
    if !request.permissions.build_tool {
        return Err(AdapterError::new(
            "rust-analyzer SCIP indexing requires permissions.build_tool=true",
        ));
    }

    let root = canonical_root(&request.repository_root)?;
    let toolchain = toolchain_identity(analyzer, &root)?;
    let version = provider_version(&toolchain);
    let temporary = tempfile::Builder::new()
        .prefix("weave-rust-")
        .tempdir()
        .map_err(|error| AdapterError::wrap("create temporary directory", error))?;
    let output_path = temporary.path().join("index.scip");
    let config_path = temporary.path().join("rust-analyzer.json");
    let config = json!({
        "cargo": {"buildScripts": {"enable": request.permissions.run_generators}},
        "procMacro": {"enable": request.permissions.run_generators}
    });
    fs::write(
        &config_path,
        serde_json::to_vec(&config)
            .map_err(|error| AdapterError::wrap("encode rust-analyzer configuration", error))?,
    )
    .map_err(|error| AdapterError::wrap("write rust-analyzer configuration", error))?;

    let mut command = Command::new(analyzer);
    command
        .current_dir(&root)
        // rust-analyzer's hand-written CLI expects the positional project path
        // before options; placing it last currently parses as an empty project.
        .args(["scip", ".", "--config-path"])
        .arg(&config_path)
        .arg("--output")
        .arg(&output_path)
        .stdout(Stdio::null())
        .stderr(Stdio::inherit())
        .env("CARGO_TERM_COLOR", "never")
        .env("RUSTUP_AUTO_INSTALL", "0");
    // Cargo's offline mode is the only stable cross-platform mechanism for
    // preventing dependency downloads in this subordinate toolchain process.
    if !request.permissions.network || !request.permissions.restore {
        command.env("CARGO_NET_OFFLINE", "true");
    }
    let status = command
        .status()
        .map_err(|error| AdapterError::wrap("start rust-analyzer SCIP producer", error))?;
    if !status.success() {
        return Err(AdapterError::new(format!(
            "rust-analyzer SCIP producer exited with {status}"
        )));
    }

    let metadata = fs::metadata(&output_path)
        .map_err(|error| AdapterError::wrap("inspect rust-analyzer SCIP index", error))?;
    if metadata.len() > MAX_INDEX_BYTES {
        return Err(AdapterError::new(format!(
            "rust-analyzer SCIP index exceeds {MAX_INDEX_BYTES} bytes"
        )));
    }
    let encoded = read_file_bounded(
        &output_path,
        MAX_INDEX_BYTES,
        "read rust-analyzer SCIP index",
    )?;
    let index = scip_types::Index::parse_from_bytes(&encoded)
        .map_err(|error| AdapterError::wrap("decode rust-analyzer SCIP index", error))?;
    let identity = if request.repository_identity.is_empty() {
        root.to_string_lossy().replace('\\', "/")
    } else {
        request.repository_identity.clone()
    };
    let units = normalize_index(
        &index,
        &root,
        &identity,
        &version,
        &request.variant,
        request.limits.max_facts,
    )?;
    write_run(
        output,
        &request.request_id,
        &version,
        &units,
        request.limits,
    )
}

fn validate_request(request: &IndexRequest) -> Result<()> {
    if request.protocol != PROTOCOL {
        return Err(AdapterError::new("unsupported protocol"));
    }
    if request.request_id.is_empty() || request.repository_root.is_empty() {
        return Err(AdapterError::new(
            "request_id and repository_root must be nonempty strings",
        ));
    }
    if request.limits.max_frame_bytes == 0
        || request.limits.max_total_bytes == 0
        || request.limits.max_frames == 0
        || request.limits.max_facts == 0
    {
        return Err(AdapterError::new("request limits must be positive"));
    }
    if request
        .changed_paths
        .iter()
        .any(|value| value.contains('\0'))
        || request
            .environment
            .iter()
            .any(|(key, value)| key.contains('\0') || value.contains('\0'))
    {
        return Err(AdapterError::new("request metadata contains NUL"));
    }
    Ok(())
}

fn canonical_root(value: &str) -> Result<PathBuf> {
    let path = Path::new(value);
    if !path.is_absolute() {
        return Err(AdapterError::new(
            "repository_root must be an absolute directory",
        ));
    }
    let root = path
        .canonicalize()
        .map_err(|error| AdapterError::wrap("resolve repository_root", error))?;
    if !root.is_dir() {
        return Err(AdapterError::new(
            "repository_root must be an absolute directory",
        ));
    }
    Ok(root)
}

fn toolchain_identity(analyzer: &OsStr, directory: &Path) -> Result<String> {
    let analyzer = probe(analyzer, &["--version"], directory, "rust-analyzer")?;
    let cargo = probe(OsStr::new("cargo"), &["--version"], directory, "cargo")?;
    let rustc = probe(
        OsStr::new("rustc"),
        &["--version", "--verbose"],
        directory,
        "rustc",
    )?;
    let sysroot = probe(
        OsStr::new("rustc"),
        &["--print", "sysroot"],
        directory,
        "rustc sysroot",
    )?;
    Ok(format!(
        "rust-analyzer\0{analyzer}\0cargo\0{cargo}\0rustc\0{rustc}\0sysroot\0{sysroot}"
    ))
}

fn probe(executable: &OsStr, arguments: &[&str], directory: &Path, name: &str) -> Result<String> {
    let mut command = Command::new(executable);
    command
        .args(arguments)
        .current_dir(directory)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .env("CARGO_NET_OFFLINE", "true")
        .env("RUSTUP_AUTO_INSTALL", "0");
    let mut child = command
        .spawn()
        .map_err(|error| AdapterError::wrap(&format!("run {name} version probe"), error))?;
    let stdout = child
        .stdout
        .take()
        .ok_or_else(|| AdapterError::new(format!("capture {name} stdout")))?;
    let stderr = child
        .stderr
        .take()
        .ok_or_else(|| AdapterError::new(format!("capture {name} stderr")))?;
    let stdout_reader = thread::spawn(move || read_probe(stdout));
    let stderr_reader = thread::spawn(move || read_probe(stderr));
    let status = child
        .wait()
        .map_err(|error| AdapterError::wrap(&format!("wait for {name} version probe"), error))?;
    let stdout = stdout_reader
        .join()
        .map_err(|_| AdapterError::new(format!("{name} stdout reader panicked")))??;
    let stderr = stderr_reader
        .join()
        .map_err(|_| AdapterError::new(format!("{name} stderr reader panicked")))??;
    if !status.success() {
        return Err(AdapterError::new(format!(
            "{name} version probe exited with {status}: {}",
            String::from_utf8_lossy(&stderr.bytes).trim()
        )));
    }
    if stdout.exceeded || stderr.exceeded {
        return Err(AdapterError::new(format!(
            "{name} version output exceeds {MAX_PROBE_BYTES} bytes"
        )));
    }
    let stdout = String::from_utf8(stdout.bytes)
        .map_err(|error| AdapterError::wrap(&format!("{name} version is not UTF-8"), error))?;
    let stderr = String::from_utf8(stderr.bytes)
        .map_err(|error| AdapterError::wrap(&format!("{name} diagnostics are not UTF-8"), error))?;
    let identity = format!("{}\n{}", stdout.trim(), stderr.trim());
    if identity.trim().is_empty() {
        return Err(AdapterError::new(format!("{name} version is empty")));
    }
    Ok(identity)
}

struct ProbeOutput {
    bytes: Vec<u8>,
    exceeded: bool,
}

fn read_probe(mut reader: impl Read) -> Result<ProbeOutput> {
    let mut bytes = Vec::new();
    reader
        .by_ref()
        .take((MAX_PROBE_BYTES + 1) as u64)
        .read_to_end(&mut bytes)
        .map_err(|error| AdapterError::wrap("read version probe", error))?;
    let exceeded = bytes.len() > MAX_PROBE_BYTES;
    if exceeded {
        bytes.truncate(MAX_PROBE_BYTES);
        std::io::copy(&mut reader, &mut std::io::sink())
            .map_err(|error| AdapterError::wrap("drain version probe", error))?;
    }
    Ok(ProbeOutput { bytes, exceeded })
}

fn provider_version(toolchain_identity: &str) -> String {
    let digest = sha256_hex(
        format!(
            "{}\0{}\0{}",
            env!("CARGO_PKG_VERSION"),
            env!("WEAVE_RUST_SOURCE_HASH"),
            toolchain_identity
        )
        .as_bytes(),
    );
    format!(
        "{}+source.{}.rust-analyzer.{}",
        env!("CARGO_PKG_VERSION"),
        env!("WEAVE_RUST_SOURCE_HASH"),
        &digest[..12]
    )
}

pub fn normalize_index(
    index: &scip_types::Index,
    root: &Path,
    identity: &str,
    provider_version: &str,
    variant: &str,
    max_facts: usize,
) -> Result<Vec<UnitFacts>> {
    let tool = index
        .metadata
        .as_ref()
        .and_then(|metadata| metadata.tool_info.as_ref())
        .ok_or_else(|| AdapterError::new("SCIP metadata tool information is required"))?;
    if tool.name != "rust-analyzer" || tool.version.is_empty() {
        return Err(AdapterError::new(
            "SCIP index must identify a versioned rust-analyzer producer",
        ));
    }
    let canonical_root = root
        .canonicalize()
        .map_err(|error| AdapterError::wrap("resolve repository root", error))?;
    let mut paths = BTreeSet::new();
    let mut units = Vec::with_capacity(index.documents.len());
    let mut total_source = 0_u64;
    let mut total_facts = 0_usize;
    for (number, document) in index.documents.iter().enumerate() {
        let path = safe_document_path(&document.relative_path)
            .map_err(|error| AdapterError::new(format!("SCIP document {number}: {error}")))?;
        if !paths.insert(path.clone()) {
            return Err(AdapterError::new(format!(
                "duplicate SCIP document path {path:?}"
            )));
        }
        let source = document_source(&canonical_root, &path, &document.text)?;
        total_source += source.len() as u64;
        if total_source > MAX_TOTAL_SOURCE_BYTES {
            return Err(AdapterError::new(format!(
                "Rust sources exceed {MAX_TOTAL_SOURCE_BYTES} bytes"
            )));
        }
        let facts = normalize_document(
            document,
            &source,
            identity,
            &path,
            provider_version,
            variant,
        )?;
        total_facts += 1 + facts.symbols.len() + facts.occurrences.len() + facts.edges.len();
        if total_facts > max_facts {
            return Err(AdapterError::new("semantic facts exceed max_facts"));
        }
        units.push(facts);
    }
    units.sort_by(|left, right| left.unit.id.cmp(&right.unit.id));
    validate_global_ids(&units)?;
    Ok(units)
}

fn safe_document_path(value: &str) -> Result<String> {
    if value.is_empty()
        || value.starts_with('/')
        || value.contains('\\')
        || value.contains('\0')
        || value
            .split('/')
            .any(|part| part.is_empty() || part == "." || part == "..")
    {
        return Err(AdapterError::new(format!(
            "document path {value:?} is not a canonical repository-relative slash path"
        )));
    }
    #[cfg(windows)]
    if value.contains(':') {
        return Err(AdapterError::new(format!(
            "document path {value:?} is not local"
        )));
    }
    Ok(value.to_owned())
}

fn document_source(root: &Path, relative: &str, embedded: &str) -> Result<Vec<u8>> {
    let target = root.join(relative.replace('/', std::path::MAIN_SEPARATOR_STR));
    let metadata = match fs::symlink_metadata(&target) {
        Ok(metadata) => {
            if metadata.file_type().is_symlink() || !metadata.is_file() {
                return Err(AdapterError::new(format!(
                    "document {relative:?} is not a regular non-symlink file"
                )));
            }
            let resolved = target.canonicalize().map_err(|error| {
                AdapterError::wrap(&format!("resolve document {relative:?}"), error)
            })?;
            if !resolved.starts_with(root) {
                return Err(AdapterError::new(format!(
                    "document {relative:?} escapes the repository"
                )));
            }
            Some((metadata, resolved))
        }
        Err(error) if error.kind() == std::io::ErrorKind::NotFound && !embedded.is_empty() => None,
        Err(error) => {
            return Err(AdapterError::wrap(
                &format!("inspect document {relative:?}"),
                error,
            ))
        }
    };
    if !embedded.is_empty() {
        if embedded.len() as u64 > MAX_SOURCE_BYTES {
            return Err(AdapterError::new("embedded source exceeds 16 MiB"));
        }
        return Ok(embedded.as_bytes().to_vec());
    }
    let (metadata, resolved) = metadata.expect("source without embedded text was inspected");
    if metadata.len() > MAX_SOURCE_BYTES {
        return Err(AdapterError::new(format!(
            "document {relative:?} exceeds {MAX_SOURCE_BYTES} bytes"
        )));
    }
    read_file_bounded(
        &resolved,
        MAX_SOURCE_BYTES,
        &format!("read document {relative:?}"),
    )
}

fn read_file_bounded(path: &Path, maximum: u64, context: &str) -> Result<Vec<u8>> {
    let mut file = fs::File::open(path).map_err(|error| AdapterError::wrap(context, error))?;
    let mut value = Vec::new();
    Read::by_ref(&mut file)
        .take(maximum + 1)
        .read_to_end(&mut value)
        .map_err(|error| AdapterError::wrap(context, error))?;
    if value.len() as u64 > maximum {
        return Err(AdapterError::new(format!(
            "{context}: input exceeds {maximum} bytes"
        )));
    }
    Ok(value)
}

fn normalize_document(
    document: &scip_types::Document,
    source: &[u8],
    identity: &str,
    path: &str,
    provider_version: &str,
    variant: &str,
) -> Result<UnitFacts> {
    let unit_id = stable_id("scip-unit:", &[identity, path, PROVIDER, provider_version]);
    let document_id = stable_id("scip-document:", &[identity, PROVIDER, path]);
    let content_hash = format!("sha256:{}", sha256_hex(source));
    let mut facts = UnitFacts {
        unit: Unit {
            id: unit_id.clone(),
            provider: PROVIDER.to_owned(),
            provider_version: provider_version.to_owned(),
            language: if document.language.is_empty() {
                "rust".to_owned()
            } else {
                document.language.clone()
            },
            variant: variant.to_owned(),
            input_fingerprint: content_hash.clone(),
            surface_fingerprint: String::new(),
            inventory_digest: String::new(),
        },
        documents: vec![Document {
            id: document_id.clone(),
            unit_id: unit_id.clone(),
            path: path.to_owned(),
            language: if document.language.is_empty() {
                "rust".to_owned()
            } else {
                document.language.clone()
            },
            content_hash,
            provider: PROVIDER.to_owned(),
            provider_version: provider_version.to_owned(),
        }],
        symbols: Vec::new(),
        occurrences: Vec::new(),
        edges: Vec::new(),
    };

    let encoding = document.position_encoding.enum_value().map_err(|value| {
        AdapterError::new(format!("unsupported SCIP position encoding {value}"))
    })?;
    let mut definitions = BTreeMap::<String, SourceRange>::new();
    let mut occurrences = Vec::new();
    for (number, occurrence) in document.occurrences.iter().enumerate() {
        if occurrence.symbol.is_empty() {
            continue;
        }
        validate_symbol(&occurrence.symbol)
            .map_err(|error| AdapterError::new(format!("occurrence {number}: {error}")))?;
        let raw_range = occurrence_range(occurrence)
            .map_err(|error| AdapterError::new(format!("occurrence {number}: {error}")))?;
        let range = convert_range(source, encoding, raw_range)?;
        let definition = has_role(occurrence, scip_types::SymbolRole::Definition)
            || has_role(occurrence, scip_types::SymbolRole::ForwardDefinition);
        let role = if definition {
            "definition"
        } else {
            "reference"
        };
        if definition {
            definitions
                .entry(occurrence.symbol.clone())
                .or_insert_with(|| range.clone());
        }
        occurrences.push(RawOccurrence {
            symbol: occurrence.symbol.clone(),
            symbol_id: symbol_id(identity, path, &occurrence.symbol),
            role,
            range,
        });
    }

    for (number, information) in document.symbols.iter().enumerate() {
        if information.symbol.is_empty() {
            return Err(AdapterError::new(format!(
                "symbol information {number} has no symbol"
            )));
        }
        validate_symbol(&information.symbol)
            .map_err(|error| AdapterError::new(format!("symbol information {number}: {error}")))?;
        let display_name = if information.display_name.is_empty() {
            information.symbol.clone()
        } else {
            information.display_name.clone()
        };
        let definition = definitions.get(&information.symbol).cloned();
        let id = symbol_id(identity, path, &information.symbol);
        facts.symbols.push(Symbol {
            id: id.clone(),
            unit_id: unit_id.clone(),
            stable_name: information.symbol.clone(),
            display_name: display_name.clone(),
            // The Go host owns graph.NormalizeName and fills an empty spelling
            // during validation. Duplicating Unicode case folding here would
            // couple query behavior to the adapter's Rust toolchain version.
            normalized_name: String::new(),
            kind: symbol_kind(information),
            document_id: definition
                .as_ref()
                .map(|_| document_id.clone())
                .unwrap_or_default(),
            definition: definition.unwrap_or_default(),
            provider: PROVIDER.to_owned(),
            evidence: "exact",
        });
        for relationship in &information.relationships {
            validate_symbol(&relationship.symbol).map_err(|error| {
                AdapterError::new(format!("symbol information {number} relationship: {error}"))
            })?;
            let target = symbol_id(identity, path, &relationship.symbol);
            if relationship.is_implementation {
                facts
                    .edges
                    .push(semantic_edge(&unit_id, &id, &target, "implements"));
            }
            if relationship.is_reference
                || relationship.is_definition
                || relationship.is_type_definition
            {
                facts
                    .edges
                    .push(semantic_edge(&unit_id, &id, &target, "references"));
            }
        }
    }
    for occurrence in occurrences {
        let id = stable_id(
            "scip-occurrence:",
            &[
                &unit_id,
                &occurrence.symbol,
                occurrence.role,
                &range_key(&occurrence.range),
            ],
        );
        facts.occurrences.push(Occurrence {
            id,
            unit_id: unit_id.clone(),
            symbol_id: occurrence.symbol_id,
            document_id: document_id.clone(),
            role: occurrence.role,
            range: occurrence.range,
            provider: PROVIDER.to_owned(),
            evidence: "exact",
        });
    }

    facts.symbols.sort_by(|left, right| left.id.cmp(&right.id));
    facts
        .occurrences
        .sort_by(|left, right| left.id.cmp(&right.id));
    facts.edges.sort_by(|left, right| {
        (&left.from, left.kind, &left.to, left.evidence, &left.id).cmp(&(
            &right.from,
            right.kind,
            &right.to,
            right.evidence,
            &right.id,
        ))
    });
    facts.unit.surface_fingerprint = digest_json(&json!({
        "symbols": facts.symbols,
        "edges": facts.edges,
    }))?;
    facts.unit.inventory_digest = digest_json(&facts)?;
    Ok(facts)
}

fn validate_symbol(value: &str) -> Result<()> {
    if value.len() > 1 << 20 {
        return Err(AdapterError::new("SCIP symbol exceeds 1 MiB"));
    }
    scip::symbol::parse_symbol(value)
        .map(|_| ())
        .map_err(|error| AdapterError::new(format!("invalid SCIP symbol: {error:?}")))
}

fn occurrence_range(value: &scip_types::Occurrence) -> Result<(i32, i32, i32, i32)> {
    match &value.typed_range {
        Some(occurrence::Typed_range::SingleLineRange(range)) => Ok((
            range.line,
            range.start_character,
            range.line,
            range.end_character,
        )),
        Some(occurrence::Typed_range::MultiLineRange(range)) => Ok((
            range.start_line,
            range.start_character,
            range.end_line,
            range.end_character,
        )),
        Some(_) => Err(AdapterError::new(
            "SCIP occurrence uses an unsupported typed range",
        )),
        None => match value.range.as_slice() {
            [line, start, end] => Ok((*line, *start, *line, *end)),
            [start_line, start, end_line, end] => Ok((*start_line, *start, *end_line, *end)),
            _ => Err(AdapterError::new(
                "SCIP occurrence has no valid source range",
            )),
        },
    }
}

fn convert_range(
    source: &[u8],
    encoding: scip_types::PositionEncoding,
    value: (i32, i32, i32, i32),
) -> Result<SourceRange> {
    let start = convert_position(source, encoding, value.0, value.1)?;
    let end = convert_position(source, encoding, value.2, value.3)?;
    if end < start {
        return Err(AdapterError::new("SCIP range end precedes start"));
    }
    Ok(SourceRange { start, end })
}

fn convert_position(
    source: &[u8],
    encoding: scip_types::PositionEncoding,
    line: i32,
    character: i32,
) -> Result<Position> {
    if line < 0 || character < 0 {
        return Err(AdapterError::new("negative source coordinate"));
    }
    let (line_bytes, offset) = source_line(source, line as usize)
        .ok_or_else(|| AdapterError::new(format!("line {line} exceeds source")))?;
    let column = byte_column(line_bytes, encoding, character as usize)?;
    Ok(Position {
        line,
        column: column as i32,
        byte: (offset + column) as i64,
    })
}

fn source_line(source: &[u8], target: usize) -> Option<(&[u8], usize)> {
    let mut start = 0;
    let mut line = 0;
    for (index, value) in source.iter().enumerate() {
        if *value != b'\n' {
            continue;
        }
        if line == target {
            let end = if index > start && source[index - 1] == b'\r' {
                index - 1
            } else {
                index
            };
            return Some((&source[start..end], start));
        }
        line += 1;
        start = index + 1;
    }
    if line == target {
        let end = if source.len() > start && source[source.len() - 1] == b'\r' {
            source.len() - 1
        } else {
            source.len()
        };
        Some((&source[start..end], start))
    } else {
        None
    }
}

fn byte_column(
    line: &[u8],
    encoding: scip_types::PositionEncoding,
    target: usize,
) -> Result<usize> {
    let text = std::str::from_utf8(line)
        .map_err(|error| AdapterError::wrap("source line is not valid UTF-8", error))?;
    match encoding {
        scip_types::PositionEncoding::UTF8CodeUnitOffsetFromLineStart => {
            if target > line.len() || (target < line.len() && !text.is_char_boundary(target)) {
                Err(AdapterError::new(
                    "UTF-8 column is outside source or splits a code point",
                ))
            } else {
                Ok(target)
            }
        }
        scip_types::PositionEncoding::UTF16CodeUnitOffsetFromLineStart => {
            encoded_column(text, target, |value| value.len_utf16(), "UTF-16")
        }
        scip_types::PositionEncoding::UTF32CodeUnitOffsetFromLineStart => {
            encoded_column(text, target, |_| 1, "UTF-32")
        }
        scip_types::PositionEncoding::UnspecifiedPositionEncoding => Err(AdapterError::new(
            "SCIP document has ambiguous unspecified position encoding",
        )),
    }
}

fn encoded_column(
    text: &str,
    target: usize,
    width: impl Fn(char) -> usize,
    name: &str,
) -> Result<usize> {
    let mut units = 0;
    for (byte, value) in text.char_indices() {
        if units == target {
            return Ok(byte);
        }
        units += width(value);
        if units > target {
            return Err(AdapterError::new(format!(
                "{name} column splits a code point"
            )));
        }
    }
    if units == target {
        Ok(text.len())
    } else {
        Err(AdapterError::new(format!(
            "{name} column exceeds source line"
        )))
    }
}

fn has_role(value: &scip_types::Occurrence, role: scip_types::SymbolRole) -> bool {
    value.symbol_roles & role.value() != 0
}

fn symbol_kind(value: &scip_types::SymbolInformation) -> String {
    value
        .kind
        .enum_value()
        .map(|kind| format!("{kind:?}").to_lowercase())
        .unwrap_or_else(|_| "unknown".to_owned())
}

fn symbol_id(identity: &str, path: &str, symbol: &str) -> String {
    if scip::symbol::is_local_symbol(symbol) {
        stable_id("scip-symbol:", &[identity, PROVIDER, path, symbol])
    } else {
        stable_id("scip-symbol:", &[identity, PROVIDER, symbol])
    }
}

fn semantic_edge(unit_id: &str, from: &str, to: &str, kind: &'static str) -> Edge {
    Edge {
        id: stable_id("scip-edge:", &[unit_id, from, kind, to]),
        unit_id: unit_id.to_owned(),
        from: from.to_owned(),
        to: to.to_owned(),
        kind,
        evidence: "exact",
        document_id: String::new(),
        range: SourceRange::default(),
        provider: PROVIDER.to_owned(),
    }
}

fn stable_id(prefix: &str, parts: &[&str]) -> String {
    let mut digest = Sha256::new();
    for part in parts {
        digest.update([0]);
        digest.update(part.as_bytes());
    }
    format!("{prefix}{:x}", digest.finalize())
}

fn sha256_hex(value: &[u8]) -> String {
    format!("{:x}", Sha256::digest(value))
}

fn digest_json(value: &impl Serialize) -> Result<String> {
    let encoded = serde_json::to_vec(value)
        .map_err(|error| AdapterError::wrap("encode semantic fingerprint", error))?;
    Ok(format!("sha256:{}", sha256_hex(&encoded)))
}

fn range_key(value: &SourceRange) -> String {
    format!(
        "{}:{}:{}:{}",
        value.start.line, value.start.column, value.end.line, value.end.column
    )
}

fn validate_global_ids(units: &[UnitFacts]) -> Result<()> {
    let mut seen = BTreeMap::<&str, &str>::new();
    for unit in units {
        check_id(&mut seen, &unit.unit.id, "unit")?;
        for document in &unit.documents {
            check_id(&mut seen, &document.id, "document")?;
        }
        for symbol in &unit.symbols {
            check_id(&mut seen, &symbol.id, "symbol")?;
        }
        for occurrence in &unit.occurrences {
            check_id(&mut seen, &occurrence.id, "occurrence")?;
        }
        for edge in &unit.edges {
            check_id(&mut seen, &edge.id, "edge")?;
        }
    }
    Ok(())
}

fn check_id<'a>(
    seen: &mut BTreeMap<&'a str, &'static str>,
    id: &'a str,
    kind: &'static str,
) -> Result<()> {
    if let Some(previous) = seen.insert(id, kind) {
        return Err(AdapterError::new(format!(
            "duplicate semantic fact ID {id:?} used by {previous} and {kind}"
        )));
    }
    Ok(())
}

fn write_run(
    output: &mut dyn Write,
    request_id: &str,
    provider_version: &str,
    units: &[UnitFacts],
    limits: RequestLimits,
) -> Result<()> {
    let mut writer = ProtocolWriter {
        output,
        request_id,
        limits,
        total_bytes: 0,
        frames: 0,
    };
    writer.frame(
        "run.begin",
        &json!({
            "provider": Provider {name: PROVIDER, version: provider_version},
            "fact_encoding": FACT_ENCODING,
        }),
    )?;
    for facts in units {
        writer.frame("unit.begin", &json!({"unit": facts.unit}))?;
        writer.fact_batches("documents", &facts.documents)?;
        writer.fact_batches("symbols", &facts.symbols)?;
        writer.fact_batches("occurrences", &facts.occurrences)?;
        writer.fact_batches("edges", &facts.edges)?;
        writer.frame(
            "unit.end",
            &json!({
                "status": "complete",
                "counts": {
                    "documents": facts.documents.len(),
                    "symbols": facts.symbols.len(),
                    "occurrences": facts.occurrences.len(),
                    "edges": facts.edges.len(),
                }
            }),
        )?;
    }
    writer.frame(
        "run.end",
        &json!({
            "status": "complete",
            "units": units.iter().map(|facts| &facts.unit.id).collect::<Vec<_>>(),
        }),
    )
}

struct ProtocolWriter<'a> {
    output: &'a mut dyn Write,
    request_id: &'a str,
    limits: RequestLimits,
    total_bytes: u64,
    frames: usize,
}

impl ProtocolWriter<'_> {
    fn frame(&mut self, kind: &str, payload: &impl Serialize) -> Result<()> {
        let encoded = self.encode_frame(kind, payload)?;
        if encoded.len() as u64 > self.limits.max_frame_bytes {
            return Err(AdapterError::new("protocol frame exceeds max_frame_bytes"));
        }
        self.write_encoded(encoded)
    }

    fn fact_batches<T: Serialize>(&mut self, field: &'static str, values: &[T]) -> Result<()> {
        let mut start = 0;
        while start < values.len() {
            let mut selected = None;
            let maximum = values.len().min(start + FACTS_PER_FRAME);
            for end in (start + 1)..=maximum {
                let payload = BTreeMap::from([(field, &values[start..end])]);
                let encoded = self.encode_frame("facts", &payload)?;
                if encoded.len() as u64 > self.limits.max_frame_bytes {
                    break;
                }
                selected = Some((end, encoded));
            }
            let (end, encoded) = selected.ok_or_else(|| {
                AdapterError::new(format!("one {field} fact exceeds max_frame_bytes"))
            })?;
            self.write_encoded(encoded)?;
            start = end;
        }
        Ok(())
    }

    fn encode_frame(&self, kind: &str, payload: &impl Serialize) -> Result<Vec<u8>> {
        let mut encoded = serde_json::to_vec(&json!({
            "protocol": PROTOCOL,
            "request_id": self.request_id,
            "kind": kind,
            "payload": payload,
        }))
        .map_err(|error| AdapterError::wrap("encode protocol frame", error))?;
        encoded.push(b'\n');
        Ok(encoded)
    }

    fn write_encoded(&mut self, encoded: Vec<u8>) -> Result<()> {
        self.frames += 1;
        self.total_bytes += encoded.len() as u64;
        if self.frames > self.limits.max_frames {
            return Err(AdapterError::new("protocol output exceeds max_frames"));
        }
        if self.total_bytes > self.limits.max_total_bytes {
            return Err(AdapterError::new("protocol output exceeds max_total_bytes"));
        }
        self.output
            .write_all(&encoded)
            .map_err(|error| AdapterError::wrap("write protocol frame", error))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn utf_columns_are_converted_to_bytes() {
        let line = "🚀Name".as_bytes();
        assert_eq!(
            byte_column(
                line,
                scip_types::PositionEncoding::UTF8CodeUnitOffsetFromLineStart,
                4
            )
            .unwrap(),
            4
        );
        assert_eq!(
            byte_column(
                line,
                scip_types::PositionEncoding::UTF16CodeUnitOffsetFromLineStart,
                2
            )
            .unwrap(),
            4
        );
        assert_eq!(
            byte_column(
                line,
                scip_types::PositionEncoding::UTF32CodeUnitOffsetFromLineStart,
                1
            )
            .unwrap(),
            4
        );
        assert!(byte_column(
            line,
            scip_types::PositionEncoding::UTF16CodeUnitOffsetFromLineStart,
            1
        )
        .is_err());
    }

    #[test]
    fn unsafe_paths_are_rejected() {
        for path in ["", "/abs.rs", "../out.rs", "src/../out.rs", "src\\out.rs"] {
            assert!(safe_document_path(path).is_err(), "accepted {path:?}");
        }
        assert_eq!(safe_document_path("src/lib.rs").unwrap(), "src/lib.rs");
    }

    #[test]
    fn provider_version_changes_with_the_active_toolchain() {
        let first = provider_version("ra 1\0cargo 1\0rustc 1\0sysroot/a");
        let second = provider_version("ra 1\0cargo 1\0rustc 2\0sysroot/b");
        assert_ne!(first, second);
        assert_eq!(first, provider_version("ra 1\0cargo 1\0rustc 1\0sysroot/a"));
    }
}
