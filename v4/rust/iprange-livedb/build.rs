use std::fs;
use std::path::{Path, PathBuf};

use sha2::{Digest, Sha256};

fn main() {
    println!("cargo:rerun-if-changed=Cargo.toml");
    println!("cargo:rerun-if-changed=src");

    assert_separator_invariance();

    let mut sources = Vec::new();
    collect(Path::new("src"), &mut sources);
    sources.sort_by_key(|path| logical_name(path));

    let mut hash = Sha256::new();
    hash_file(Path::new("Cargo.toml"), &mut hash);
    for path in sources {
        hash_file(&path, &mut hash);
    }
    println!("cargo:rustc-env=IPRANGE_V4_BUILD_ID={:x}", hash.finalize());
}

/// The hashed name of one source file: its path components joined by `/`.
///
/// A POSIX checkout spells these paths with `/` and a Windows checkout
/// with `\`, so hashing the host's own path string would make
/// `IPRANGE_V4_BUILD_ID` differ between build hosts for identical
/// content. Splitting on both separators keeps the identity a property of
/// the source tree, which is what the worker version handshake compares.
/// A POSIX file name that contains `\` is therefore read as a nested
/// path; no such name exists in this package.
fn logical_name(path: &Path) -> String {
    path.to_string_lossy()
        .split(|separator| separator == '/' || separator == '\\')
        .filter(|component| !component.is_empty() && *component != ".")
        .collect::<Vec<_>>()
        .join("/")
}

/// Fail the build when the separator split above is weakened: the two
/// spellings of one path must produce one name on every host.
fn assert_separator_invariance() {
    let expected = "src/worker/control.rs";
    assert_eq!(
        logical_name(Path::new("src/worker/control.rs")),
        expected,
        "a POSIX path must hash as a `/`-joined name"
    );
    assert_eq!(
        logical_name(Path::new("src\\worker\\control.rs")),
        expected,
        "a Windows path must hash as the same `/`-joined name"
    );
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
    let name = logical_name(path);
    let name = name.as_bytes();
    hash.update((name.len() as u64).to_le_bytes());
    hash.update(name);
    hash.update((bytes.len() as u64).to_le_bytes());
    hash.update(bytes);
}
