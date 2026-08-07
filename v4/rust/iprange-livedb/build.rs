use std::fs;
use std::path::{Path, PathBuf};

use sha2::{Digest, Sha256};

fn main() {
    println!("cargo:rerun-if-changed=Cargo.toml");
    println!("cargo:rerun-if-changed=src");

    let mut sources = Vec::new();
    collect(Path::new("src"), &mut sources);
    sources.sort();

    let mut hash = Sha256::new();
    hash_file(Path::new("Cargo.toml"), &mut hash);
    for path in sources {
        hash_file(&path, &mut hash);
    }
    println!("cargo:rustc-env=IPRANGE_V4_BUILD_ID={:x}", hash.finalize());
}

fn collect(directory: &Path, output: &mut Vec<PathBuf>) {
    let mut entries = fs::read_dir(directory)
        .unwrap_or_else(|error| panic!("cannot inspect {}: {error}", directory.display()))
        .collect::<Result<Vec<_>, _>>()
        .unwrap_or_else(|error| panic!("cannot inspect {}: {error}", directory.display()));
    entries.sort_by_key(|entry| entry.file_name());
    for entry in entries {
        let path = entry.path();
        if path.is_dir() {
            collect(&path, output);
        } else if path.extension().is_some_and(|extension| extension == "rs") {
            output.push(path);
        }
    }
}

fn hash_file(path: &Path, hash: &mut Sha256) {
    let bytes =
        fs::read(path).unwrap_or_else(|error| panic!("cannot hash {}: {error}", path.display()));
    let name = path.as_os_str().as_encoded_bytes();
    hash.update((name.len() as u64).to_le_bytes());
    hash.update(name);
    hash.update((bytes.len() as u64).to_le_bytes());
    hash.update(bytes);
}
