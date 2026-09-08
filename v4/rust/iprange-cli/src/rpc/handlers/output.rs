//! Metadata delivery encoding and bounded atomic file publication.

use std::fs::{self, File, OpenOptions};
use std::io::Write;
use std::path::{Path, PathBuf};

use iprange_livedb::publication::PublicationPolicy;
use serde_json::{json, Value};
use sha2::{Digest, Sha256};

use super::super::dispatch::HandlerError;
use super::super::new_handle;

pub fn base64_padded(input: &[u8]) -> String {
    const ALPHABET: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let mut output = String::with_capacity(input.len().div_ceil(3) * 4);
    for chunk in input.chunks(3) {
        let b0 = chunk[0] as u32;
        let b1 = chunk.get(1).copied().unwrap_or(0) as u32;
        let b2 = chunk.get(2).copied().unwrap_or(0) as u32;
        let word = (b0 << 16) | (b1 << 8) | b2;
        output.push(ALPHABET[word as usize >> 18] as char);
        output.push(ALPHABET[(word >> 12) as usize & 63] as char);
        output.push(if chunk.len() > 1 {
            ALPHABET[(word >> 6) as usize & 63] as char
        } else {
            '='
        });
        output.push(if chunk.len() > 2 {
            ALPHABET[word as usize & 63] as char
        } else {
            '='
        });
    }
    output
}

/// Stable file identity of a path at a point in time: device and
/// inode on POSIX, volume serial and file index on Windows.  A reader
/// records this once at open; a later rename keeps the identity, so
/// the output-over-source guard can still recognize the file that
/// backs an open handle even when its pathname moved.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct FileIdentity {
    pub path: PathBuf,
    pub dev: u64,
    pub ino: u64,
}

/// Capture the current file identity of `path`, if it exists.
pub(crate) fn file_identity(path: &Path) -> Option<FileIdentity> {
    let meta = fs::metadata(path).ok()?;
    #[cfg(unix)]
    use std::os::unix::fs::MetadataExt;
    #[cfg(windows)]
    use std::os::windows::fs::MetadataExt;
    #[cfg(unix)]
    return Some(FileIdentity {
        path: path.to_path_buf(),
        dev: meta.dev(),
        ino: meta.ino(),
    });
    #[cfg(windows)]
    return Some(FileIdentity {
        path: path.to_path_buf(),
        dev: meta.volume_serial_number(),
        ino: meta.file_index(),
    });
    #[allow(unreachable_code)]
    None
}

/// Lexical normalization of a canonicalized prefix plus a re-appended
/// missing suffix, mirroring Go `filepath.Clean` on the same
/// components: `.` components drop, `..` pops the previous component
/// (a `..` at the root/prefix is dropped too, like Go), and repeated
/// separators collapse.  The two engines' guards then agree on
/// decorated spellings such as `dest/` and `nosuch/../dest`.
fn lexical_clean_path(path: &Path) -> PathBuf {
    let mut result = PathBuf::new();
    for component in path.components() {
        match component {
            std::path::Component::CurDir => {}
            std::path::Component::ParentDir => {
                result.pop();
            }
            other => result.push(other.as_os_str()),
        }
    }
    result
}

/// Absolute form of `path` with symlinks and `.`/`..` resolved as far
/// as the filesystem allows, without requiring the final component to
/// exist (`fs::canonicalize` fails on a not-yet-created destination,
/// which is the normal state of a `fail_if_exists` output).
///
/// The deepest existing ancestor is canonicalized and the missing
/// suffix is re-appended lexically, so two spellings of the same
/// eventual file (absolute vs relative, `./`/`..` decorations, a
/// symlinked directory) compare equal.
pub(crate) fn canonical_absolute(path: &Path) -> PathBuf {
    let absolute = if path.is_absolute() {
        path.to_path_buf()
    } else {
        match std::env::current_dir() {
            Ok(cwd) => cwd.join(path),
            Err(_) => path.to_path_buf(),
        }
    };
    let mut missing: Vec<&std::ffi::OsStr> = Vec::new();
    let mut probe = absolute.as_path();
    loop {
        match fs::canonicalize(probe) {
            Ok(resolved) => {
                let mut result = resolved;
                for component in missing.iter().rev() {
                    result.push(component);
                }
                return lexical_clean_path(&result);
            }
            Err(_) => match (probe.file_name(), probe.parent()) {
                (Some(name), Some(parent)) => {
                    missing.push(name);
                    probe = parent;
                }
                _ => return lexical_clean_path(&absolute),
            },
        }
    }
}

/// Refuse an output destination that resolves to the same file as the
/// source database.
///
/// The v1 contract never modifies its input files: publishing output
/// over the source pathname would atomically replace the database
/// with output text and report success. This is a caller error,
/// refused before any output temporary or destination exists, with
/// `invalid_argument`/`not_started` as the canonical Rust semantics
/// (the Go engine mirrors this exact code/outcome/message).
pub(crate) fn refuse_output_over_source(
    destination: &Path,
    source: &FileIdentity,
) -> Result<(), HandlerError> {
    // Pathname identity: two spellings of the same eventual file
    // (absolute vs relative, symlinked parents, `.`/`..` decorations).
    let same_pathname =
        canonical_absolute(destination) == canonical_absolute(&source.path);
    // File identity: the destination is the same FILE that backs the
    // source even after the source pathname was renamed or hard-linked
    // (a reader keeps the identity captured at open).
    let same_file = file_identity(destination)
        .map(|dest| dest.dev == source.dev && dest.ino == source.ino)
        .unwrap_or(false);
    if same_pathname || same_file {
        return Err(HandlerError::new(
            "invalid_argument",
            "not_started",
            "destination must differ from the source database",
        ));
    }
    Ok(())
}

pub fn metadata_output(
    path: &Path,
    bytes: &[u8],
    policy: PublicationPolicy,
    max_output_bytes: u64,
    max_open_files: u32,
) -> Result<Value, HandlerError> {
    if max_open_files < 1 {
        return Err(HandlerError::new(
            "invalid_argument",
            "not_started",
            "metadata file delivery requires at least one open file",
        ));
    }
    if bytes.len() as u64 > max_output_bytes {
        // The metadata has already been read; the refusal is an
        // output-limit failure of a read-only operation.
        return Err(HandlerError::new(
            "output_limit",
            "read_only_failure",
            format!(
                "metadata output is {} bytes, limit is {max_output_bytes}",
                bytes.len()
            ),
        ));
    }
    let sha256 = Sha256::digest(bytes);
    let sha256 = sha256
        .iter()
        .map(|byte| format!("{byte:02x}"))
        .collect::<String>();
    publish(path, bytes, policy)?;
    // OUTPUT_FACTS is one generic schema for every file result. A metadata
    // delivery publishes exactly one opaque blob, so its row count is "1";
    // `bytes` remains the exact byte count, not an encoded length.
    Ok(json!({
        "path": path.to_string_lossy(),
        "sha256": sha256,
        "bytes": bytes.len().to_string(),
        "rows": "1",
    }))
}

fn publish(path: &Path, bytes: &[u8], policy: PublicationPolicy) -> Result<(), HandlerError> {
    let parent = path
        .parent()
        .filter(|value| !value.as_os_str().is_empty())
        .unwrap_or_else(|| Path::new("."));
    let mut temporary = PathBuf::from(parent);
    temporary.push(format!(".{}.metadata.tmp", new_handle()?));
    let file = OpenOptions::new()
        .write(true)
        .create_new(true)
        .open(&temporary)
        .map_err(|error| file_error(error, "create metadata output"))?;
    if let Err(error) = write_and_publish(file, &temporary, path, bytes, policy) {
        let _ = fs::remove_file(&temporary);
        return Err(error);
    }
    sync_directory(parent)
}

fn write_and_publish(
    mut file: File,
    temporary: &Path,
    destination: &Path,
    bytes: &[u8],
    policy: PublicationPolicy,
) -> Result<(), HandlerError> {
    file.write_all(bytes)
        .and_then(|()| file.sync_all())
        .map_err(|error| file_error(error, "write metadata output"))?;
    match policy {
        PublicationPolicy::FailIfExists => {
            // A hard-link publication is the portable no-replacement atom:
            // destination creation succeeds only while the name is absent.
            fs::hard_link(temporary, destination)
                .map_err(|error| file_error(error, "publish metadata output"))?;
            fs::remove_file(temporary)
                .map_err(|error| file_error(error, "remove metadata temporary"))?;
        }
        PublicationPolicy::ReplaceExisting | PublicationPolicy::ReplaceExistingNoRollback => {
            // Rust's rename maps to rename(2) and MoveFileExW(REPLACE_EXISTING),
            // so both supported platforms replace the destination atomically.
            fs::rename(temporary, destination)
                .map_err(|error| file_error(error, "publish metadata output"))?;
        }
    }
    Ok(())
}

fn sync_directory(parent: &Path) -> Result<(), HandlerError> {
    #[cfg(unix)]
    {
        File::open(parent)
            .and_then(|directory| directory.sync_all())
            .map_err(|error| file_error(error, "sync metadata output directory"))?;
    }
    #[cfg(not(unix))]
    let _ = parent;
    Ok(())
}

fn file_error(error: std::io::Error, operation: &str) -> HandlerError {
    let message = format!("{operation}: {error}");
    // Metadata delivery is a read-only operation: every file-I/O failure
    // after the metadata read began reports read_only_failure (the spec
    // reserves not_started for refusals before an SDK attempt).
    if error.kind() == std::io::ErrorKind::AlreadyExists {
        HandlerError::new("name_exists", "read_only_failure", message)
    } else {
        HandlerError::new("io", "read_only_failure", message)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::{SystemTime, UNIX_EPOCH};

    fn temporary_path(label: &str) -> PathBuf {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        std::env::temp_dir().join(format!(
            "iprange-metadata-{label}-{}-{unique}",
            std::process::id()
        ))
    }

    #[test]
    fn base64_is_standard_and_padded() {
        assert_eq!(base64_padded(b""), "");
        assert_eq!(base64_padded(b"f"), "Zg==");
        assert_eq!(base64_padded(b"fo"), "Zm8=");
        assert_eq!(base64_padded(b"foo"), "Zm9v");
        assert_eq!(base64_padded(b"foobar"), "Zm9vYmFy");
    }

    #[test]
    fn file_delivery_writes_exact_digest_and_replaces_atomically() {
        let path = temporary_path("output");
        let first =
            metadata_output(&path, b"first", PublicationPolicy::FailIfExists, 100, 1).unwrap();
        assert_eq!(first["bytes"], "5");
        assert_eq!(first["rows"], "1");
        assert_eq!(
            first["sha256"],
            Sha256::digest(b"first")
                .iter()
                .map(|b| format!("{b:02x}"))
                .collect::<String>()
        );
        let second =
            metadata_output(&path, b"second", PublicationPolicy::ReplaceExisting, 100, 1).unwrap();
        assert_eq!(second["bytes"], "6");
        assert_eq!(fs::read(&path).unwrap(), b"second");
        assert!(metadata_output(&path, b"third", PublicationPolicy::FailIfExists, 100, 1).is_err());
        fs::remove_file(path).unwrap();
    }
}

    #[test]
    fn canonical_absolute_normalizes_decorated_spellings() {
        // Wave 19.4 parity with the Go engine: a trailing separator
        // and a non-existent-ancestor ".." spelling resolve to the
        // same canonical identity as the plain path, so both engines
        // refuse those destinations preflight instead of failing late
        // at the output rename.
        let dir = std::env::temp_dir().join(format!(
            "iprange-canon-w19-{}",
            std::process::id()
        ));
        fs::create_dir_all(&dir).unwrap();
        let source = dir.join("db.iprange");
        fs::write(&source, b"source").unwrap();
        for spelling in [
            source.to_string_lossy().to_string() + "/",
            dir.join("nosuch").join("..").join("db.iprange")
                .to_string_lossy()
                .to_string(),
        ] {
            assert_eq!(
                canonical_absolute(Path::new(&spelling)),
                canonical_absolute(&source),
                "spelling {spelling}"
            );
        }
        let other = dir.join("other.iprange");
        assert_ne!(
            canonical_absolute(&dir.join("nosuch").join("..").join("other.iprange")),
            canonical_absolute(&source)
        );
        assert_eq!(
            canonical_absolute(&dir.join("nosuch").join("..").join("other.iprange")),
            canonical_absolute(&other)
        );
        let _ = fs::remove_dir_all(&dir);
    }

    #[test]
    fn refuse_output_over_source_uses_file_identity_after_rename() {
        // The wave-19.4 P1 repair: the guard must recognize the same
        // FILE through a rename (the identity captured at open), not
        // only the same pathname.
        let dir = std::env::temp_dir().join(format!(
            "iprange-refuse-w19-{}",
            std::process::id()
        ));
        fs::create_dir_all(&dir).unwrap();
        let source = dir.join("db.iprange");
        fs::write(&source, b"source").unwrap();
        let identity = file_identity(&source).expect("identity");
        let renamed = dir.join("db.bak");
        fs::rename(&source, &renamed).unwrap();
        let error = refuse_output_over_source(&renamed, &identity).unwrap_err();
        assert_eq!(error.code, "invalid_argument");
        assert_eq!(error.outcome, "not_started");
        assert_eq!(
            error.message,
            "destination must differ from the source database"
        );
        // A hard-link alias of the same file is refused too.
        fs::rename(&renamed, &source).unwrap();
        let alias = dir.join("db-alias.iprange");
        if fs::hard_link(&source, &alias).is_ok() {
            assert!(refuse_output_over_source(&alias, &identity).is_err());
            let _ = fs::remove_file(&alias);
        }
        let other = dir.join("other.iprange");
        fs::write(&other, b"other").unwrap();
        assert!(refuse_output_over_source(&other, &identity).is_ok());
        let _ = fs::remove_dir_all(&dir);
    }
