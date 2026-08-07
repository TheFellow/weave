use std::collections::BTreeMap;
use std::env;
use std::fs;
use std::path::{Path, PathBuf};

use sha2::{Digest, Sha256};

fn main() {
    let manifest = PathBuf::from(env::var_os("CARGO_MANIFEST_DIR").expect("manifest directory"));
    let mut files = BTreeMap::new();
    collect(&manifest.join("src"), &manifest, &mut files);
    for name in ["Cargo.toml", "Cargo.lock", "build.rs"] {
        let path = manifest.join(name);
        println!("cargo:rerun-if-changed={}", path.display());
        files.insert(
            name.to_owned(),
            fs::read(path).expect("read adapter build input"),
        );
    }

    let mut hash = Sha256::new();
    for (name, contents) in files {
        hash.update(name.as_bytes());
        hash.update([0]);
        hash.update(contents);
    }
    println!(
        "cargo:rustc-env=WEAVE_RUST_SOURCE_HASH={:x}",
        hash.finalize()
    );
}

fn collect(directory: &Path, root: &Path, files: &mut BTreeMap<String, Vec<u8>>) {
    let mut entries: Vec<_> = fs::read_dir(directory)
        .expect("read source directory")
        .map(|entry| entry.expect("read source entry"))
        .collect();
    entries.sort_by_key(|entry| entry.path());
    for entry in entries {
        let path = entry.path();
        if path.is_dir() {
            collect(&path, root, files);
        } else if path.extension().and_then(|value| value.to_str()) == Some("rs") {
            println!("cargo:rerun-if-changed={}", path.display());
            files.insert(
                path.strip_prefix(root)
                    .expect("source beneath manifest")
                    .display()
                    .to_string(),
                fs::read(path).expect("read adapter source"),
            );
        }
    }
}
