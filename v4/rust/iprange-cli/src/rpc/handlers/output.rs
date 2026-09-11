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

/// Capture the current file identity of `path`, if it exists.  The
/// device/inode pair comes from the SDK's platform identity helper
/// (windows-sys on Windows, std metadata on POSIX), which also owns
/// the platform cfg split.
pub(crate) fn file_identity(path: &Path) -> Option<FileIdentity> {
    iprange_livedb::identity(path).map(|(dev, ino)| FileIdentity {
        path: path.to_path_buf(),
        dev,
        ino,
    })
}

/// The two file identities a same-source guard needs: the main
/// database and its reader-coordination sidecar (`<main>.readers`).
/// Handle-backed readers capture both at open; ephemeral preflights
/// stat them at request time.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct SourceIdentities {
    pub main: FileIdentity,
    pub sidecar: Option<FileIdentity>,
}

/// Capture the current file identity of a source's sidecar twin, when
/// it exists (the sidecar is derived lexically like Go; a reserved
/// main name still derives a sidecar component).
pub(crate) fn sidecar_identity(main: &Path) -> Option<FileIdentity> {
    iprange_livedb::sidecar_path(main)
        .ok()
        .and_then(|sidecar| {
            iprange_livedb::identity(&sidecar)
                .map(|(dev, ino)| FileIdentity { path: sidecar, dev, ino })
        })
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
            Err(_) => {
                let ends_in_parentdir =
                    probe.components().next_back() == Some(std::path::Component::ParentDir);
                match (probe.file_name(), probe.parent()) {
                    (Some(name), Some(parent)) => {
                        missing.push(name);
                        probe = parent;
                    }
                    // A not-yet-existing ancestor that terminates in
                    // `..` has no file name (Path::file_name returns
                    // None): pop the ParentDir component explicitly
                    // and keep walking, so `..` components that sit
                    // BEFORE the missing suffix still resolve with
                    // symlink semantics (wave 19 round 19.10; a
                    // lexical clean of the whole path would fold them
                    // against the symlink name).
                    (None, Some(parent)) if ends_in_parentdir => {
                        missing.push(std::ffi::OsStr::new(".."));
                        probe = parent;
                    }
                    _ => return lexical_clean_path(&absolute),
                }
            }
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
/// Same-file pathname identity under the destination filesystem's
/// filename-equivalence rules.  On Windows an absent sidecar (or any
/// not-yet-existing destination) has no file identity for the device/
/// inode arm, so the pathname arm must apply the platform's name
/// equivalence: Win32 strips trailing dots and spaces from the final
/// component at create, and the volume's case table equates name
/// spellings.  The fold below is the Unicode full lowercase shared
/// with Go `sameCanonical` (Go maps the one BMP character whose full
/// lowercase expands, U+0130, to its two-rune form so both engines
/// fold byte-identically); it is a practical approximation of the
/// per-volume upcase table for ABSENT names — existing files stay
/// protected by the OS file-identity arm (wave 19 round 19.12
/// security finding).  POSIX names are case-sensitive and compare
/// exactly.
fn same_canonical(a: &Path, b: &Path) -> bool {
    if a == b {
        return true;
    }
    #[cfg(windows)]
    {
        return windows_fold_path(&a.to_string_lossy())
            == windows_fold_path(&b.to_string_lossy());
    }
    #[cfg(not(windows))]
    {
        false
    }
}

/// Windows-equivalence fold of a canonical pathname: trailing
/// dots/spaces of the final component trimmed (the Win32 create
/// normalization), then Unicode lowercase per rune (Rust's full
/// `char::to_lowercase`, which expands U+0130 exactly like the Go
/// fold's explicit mapping).
#[cfg(windows)]
fn windows_fold_path(path: &str) -> String {
    // Trim the Win32 create-normalization characters (trailing dots
    // and spaces) from the FINAL component only; the special "." and
    // ".." components are never trimmed.
    let (head, comp) = match path.rfind(|c| c == '\\' || c == '/') {
        Some(i) => (&path[..i + 1], &path[i + 1..]),
        None => ("", path),
    };
    let trimmed_comp;
    let trimmed: &str = if comp == "." || comp == ".." {
        comp
    } else {
        trimmed_comp = comp.trim_end_matches(['.', ' ']);
        if trimmed_comp.is_empty() || trimmed_comp.len() == comp.len() {
            comp
        } else {
            trimmed_comp
        }
    };
    let mut out = String::with_capacity(head.len() + trimmed.len());
    out.push_str(head);
    out.push_str(trimmed);
    out.to_lowercase()
}

pub(crate) fn refuse_output_over_source(
    destination: &Path,
    source: &FileIdentity,
    sidecar: Option<&FileIdentity>,
) -> Result<(), HandlerError> {
    // Pathname identity: two spellings of the same eventual file
    // (absolute vs relative, symlinked parents, `.`/`..` decorations).
    let same_pathname =
        same_canonical(&canonical_absolute(destination), &canonical_absolute(&source.path));
    let destination_identity = file_identity(destination);
    // File identity: the destination is the same FILE that backs the
    // source even after the source pathname was renamed or hard-linked
    // (a reader keeps the identity captured at open).
    let same_file = destination_identity
        .as_ref()
        .map(|dest| dest.dev == source.dev && dest.ino == source.ino)
        .unwrap_or(false);
    // Sidecar identity: the live database's reader-coordination
    // sidecar (<main>.readers) is a distinct file that records reader
    // state; publishing output over it destroys the source's
    // readability, so it is refused exactly like the main database
    // (pathname and file-identity arms).  The file-identity arm uses
    // the sidecar identity captured at reader open when one exists (a
    // renamed sidecar keeps its identity, mirroring the main-file arm;
    // tester role wave-19.6) and falls back to a fresh stat for
    // ephemeral preflights; the pathname arm derives the sidecar
    // component lexically like the Go guard.
    let sidecar_same = iprange_livedb::sidecar_path(&source.path)
        .map(|sidecar_path| {
            let by_pathname = same_canonical(
                &canonical_absolute(destination),
                &canonical_absolute(&sidecar_path),
            );
            // The file-identity arm uses the sidecar identity captured
            // at reader open when one exists (a renamed sidecar keeps
            // its identity) and falls back to a fresh stat at the
            // sidecar PATH for ephemeral preflights: `sidecar_path` is
            // already the derived `<main>.readers` component, so the
            // fallback stats that exact path (a second `.readers`
            // suffix would derive `<main>.readers.readers` and wrongly
            // refuse a distinct destination; wave 19 round 19.8).
            let twin = sidecar.cloned().or_else(|| file_identity(&sidecar_path));
            let by_file = destination_identity
                .as_ref()
                .zip(twin.as_ref())
                .map_or(false, |(dest, twin)| {
                    dest.dev == twin.dev && dest.ino == twin.ino
                });
            by_pathname || by_file
        })
        .unwrap_or(false);
    if same_pathname || same_file || sidecar_same {
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
// Symlink creation is platform-specific; tests that need it skip on
// hosts without symlink permission.  Defined at file scope because
// the same-file guard tests live at top level (the pre-existing
// organization of this test module).
fn make_symlink(target: &std::path::Path, link: &std::path::Path) -> std::io::Result<()> {
    #[cfg(unix)]
    {
        std::os::unix::fs::symlink(target, link)
    }
    #[cfg(not(unix))]
    {
        let _ = (target, link);
        Err(std::io::Error::new(
            std::io::ErrorKind::Unsupported,
            "symlinks unsupported on this platform",
        ))
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

    // Wave 19.11 astra P1 regressions.  On Windows the guard must
    // refuse every spelling of the absent reader sidecar of the
    // source database -- drive-relative ("C:db.readers", resolved by
    // the OS against the per-drive working directory), its case
    // variant, the absolute case variant, and the
    // rooted-without-volume spelling ("\dir\db.readers") -- and
    // keep accepting distinct destinations.  The serial mutex keeps
    // the process cwd stable against the parallel test harness; every
    // path used here is absolute except the deliberately
    // drive-relative spellings under test.
    #[cfg(windows)]
    #[test]
    fn refuse_output_over_source_windows_sidecar_spellings() {
        static SERIAL: std::sync::Mutex<()> = std::sync::Mutex::new(());
        let _guard = SERIAL.lock().unwrap();

        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let dir = std::env::temp_dir().join(format!(
            "iprange-guard-windows-{}-{unique}",
            std::process::id()
        ));
        fs::create_dir_all(&dir).unwrap();
        let previous = std::env::current_dir().unwrap();

        std::env::set_current_dir(&dir).unwrap();
        let result = (|| -> Result<(), String> {
            let source = dir.join("db.bin");
            fs::write(&source, b"source").map_err(|e| e.to_string())?;

            let drive = match std::env::current_dir()
                .map_err(|e| e.to_string())?
                .components()
                .next()
            {
                Some(std::path::Component::Prefix(prefix)) => match prefix.kind() {
                    std::path::Prefix::Disk(letter) => letter,
                    _ => return Err("test requires a drive-letter prefix".into()),
                },
                _ => return Err("test requires a drive-letter prefix".into()),
            };
            let identity = FileIdentity {
                path: source.clone(),
                dev: 0,
                ino: 0,
            };
            let sidecar = source.with_file_name("db.bin.readers");

            // Rooted-without-volume spelling of the sidecar: the
            // absolute sidecar path minus its "C:" volume prefix.
            let root_relative = sidecar
                .strip_prefix(Path::new(&format!("{}:", char::from(drive))))
                .unwrap();

            // Every spelling that names the absent sidecar must be
            // refused: drive-relative, drive-relative case variant,
            // absolute case variant, and rooted-without-volume.
            let refusals: Vec<PathBuf> = vec![
                PathBuf::from(format!("{}:db.bin.readers", char::from(drive))),
                PathBuf::from(format!("{}:DB.BIN.READERS", char::from(drive))),
                dir.join("DB.BIN.READERS"),
                root_relative.to_path_buf(),
                // Win32 strips trailing dots and spaces at create, so
                // these spellings denote the absent sidecar (wave 19
                // round 19.12 security finding).
                dir.join("db.bin.readers."),
                dir.join("db.bin.readers "),
            ];
            for destination in refusals {
                if refuse_output_over_source(&destination, &identity, None).is_ok() {
                    return Err(format!(
                        "destination {:?} naming the absent sidecar accepted",
                        destination
                    ));
                }
            }

            // Non-ASCII case equivalence through the shared full
            // lowercase fold (wave 19 round 19.12 security finding).
            let non_ascii_source = dir.join("db_\u{00e4}.bin");
            fs::write(&non_ascii_source, b"source").map_err(|e| e.to_string())?;
            let non_ascii_identity = FileIdentity {
                path: non_ascii_source.clone(),
                dev: 0,
                ino: 0,
            };
            for destination in [
                dir.join("DB_\u{00c4}.BIN.READERS"),
                dir.join("DB_\u{00c4}.BIN.READERS "),
            ] {
                if refuse_output_over_source(&destination, &non_ascii_identity, None).is_ok() {
                    return Err(format!(
                        "non-ASCII variant {:?} naming the absent sidecar accepted",
                        destination
                    ));
                }
            }

            // Distinct destinations stay allowed: an absolute file in
            // the directory and a rooted-without-volume name on the
            // drive root that is not the sidecar.
            for destination in [
                dir.join("other.bin"),
                dir.join("other.bin."),
                PathBuf::from("\\unrelated.bin"),
            ] {
                if refuse_output_over_source(&destination, &identity, None).is_err() {
                    return Err(format!(
                        "distinct destination {:?} refused",
                        destination
                    ));
                }
            }
            Ok(())
        })();
        std::env::set_current_dir(&previous).unwrap();
        let _ = fs::remove_dir_all(&dir);
        result.unwrap();
    }

    #[cfg(windows)]
    #[test]
    fn same_canonical_folds_windows_case() {
        assert!(same_canonical(
            Path::new(r"C:\review\db.iprange.readers"),
            Path::new(r"c:\review\DB.IPRANGE.READERS")
        ));
        assert!(!same_canonical(
            Path::new(r"C:\review\db.iprange.readers"),
            Path::new(r"C:\review\other.readers")
        ));
    }

    #[cfg(windows)]
    #[test]
    fn same_canonical_folds_windows_unicode16() {
        // Parity with the Go fold's explicit Unicode-16 mappings
        // (wave 19 round 19.13 fold-parity finding): rustc 1.97.1
        // applies these lowercase mappings while the go1.26.5
        // Windows Go toolchain does not.  Pinning the same 55 pairs
        // on the Rust side means a future Rust toolchain table
        // change cannot silently break the byte-identical fold.
        let single: &[(char, char)] = &[
            ('\u{1C89}', '\u{1C8A}'),
            ('\u{A7CB}', '\u{0264}'),
            ('\u{A7CC}', '\u{A7CD}'),
            ('\u{A7CE}', '\u{A7CF}'),
            ('\u{A7D2}', '\u{A7D3}'),
            ('\u{A7D4}', '\u{A7D5}'),
            ('\u{A7DA}', '\u{A7DB}'),
            ('\u{A7DC}', '\u{019B}'),
        ];
        for (up, lo) in single {
            let a = format!("C:\\review\\db_{up}.readers");
            let b = format!("C:\\review\\DB_{lo}.READERS");
            assert!(
                same_canonical(Path::new(&a), Path::new(&b)),
                "fold mismatch: U+{:04X} vs U+{:04X}",
                *up as u32,
                *lo as u32
            );
        }
        for up in 0x10D50..=0x10D65 {
            let a = format!(
                "C:\\review\\db_{}.readers",
                char::from_u32(up).unwrap()
            );
            let b = format!(
                "C:\\review\\DB_{}.READERS",
                char::from_u32(up + 0x20).unwrap()
            );
            assert!(
                same_canonical(Path::new(&a), Path::new(&b)),
                "fold mismatch: U+{up:04X}"
            );
        }
        for up in 0x16EA0..=0x16EB8 {
            let a = format!(
                "C:\\review\\db_{}.readers",
                char::from_u32(up).unwrap()
            );
            let b = format!(
                "C:\\review\\DB_{}.READERS",
                char::from_u32(up + 0x1B).unwrap()
            );
            assert!(
                same_canonical(Path::new(&a), Path::new(&b)),
                "fold mismatch: U+{up:04X}"
            );
        }
    }

    #[cfg(windows)]
    #[test]
    fn windows_fold_string_context_differential() {
        // Full-string differential corpus: every scalar in both
        // sigma-neighbor contexts plus fixed boundary spellings,
        // folded by the real production windows_fold_path.  The
        // FNV-1a 64 checksum pins the fold output to the
        // rustc 1.97.1 str::to_lowercase result over the identical
        // corpus, verified byte-identical on the Windows validation
        // host (corpus sha256
        // 3cdf661f6772e0ec6875a315d1662232cc1f80f11ab44edc65435f3b9992e4d4).
        // FNV-1a is used instead of a crate digest because
        // workspace test-binary sha2 instances intermittently
        // mis-hashed long Vec inputs on this host (rustc 1.97.1
        // windows/msvc codegen observation, 2026-09-11); a plain
        // byte loop is codegen-proof and pins the same bytes.
        let mut corpus = String::with_capacity(14_000_000);
        for cp in 0u32..=0x10FFFF {
            if (0xD800..=0xDFFF).contains(&cp) {
                continue;
            }
            let c = char::from_u32(cp).unwrap();
            corpus.push(c); // sigma preceded by c, followed by '1'
            corpus.push('\u{3A3}');
            corpus.push('1');
        }
        for cp in 0u32..=0x10FFFF {
            if (0xD800..=0xDFFF).contains(&cp) {
                continue;
            }
            let c = char::from_u32(cp).unwrap();
            corpus.push('a'); // sigma preceded by 'a', followed by c
            corpus.push('\u{3A3}');
            corpus.push(c);
            corpus.push('1');
        }
        corpus.push_str(
            "a\u{3A3}a\u{3A3}a\u{3A3}\u{308}a\u{308}\u{3A3}\u{3A3}1\u{3A3}\u{2160}\u{3A3}a\u{3A3}\u{345}a\u{345}\u{3A3}a\u{3A3}\u{1C89}\u{3A3}",
        );
        let out = windows_fold_path(&corpus);
        let mut h: u64 = 0xcbf29ce484222325;
        for &b in out.as_bytes() {
            h ^= b as u64;
            h = h.wrapping_mul(0x100000001b3);
        }
        assert_eq!(
            h, 0x732bbe7ae850adfc,
            "fold string corpus FNV-1a mismatch"
        );
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
        let error = refuse_output_over_source(&renamed, &identity, None).unwrap_err();
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
            assert!(refuse_output_over_source(&alias, &identity, None).is_err());
            let _ = fs::remove_file(&alias);
        }
        let other = dir.join("other.iprange");
        fs::write(&other, b"other").unwrap();
        assert!(refuse_output_over_source(&other, &identity, None).is_ok());
        let _ = fs::remove_dir_all(&dir);
    }

    #[test]
    fn refuse_output_over_source_refuses_the_source_sidecar() {
        // The wave-19.5 P1 repair: a live database's reader-coordination
        // sidecar (<main>.readers) is a distinct file that records
        // reader state; publishing output over it destroys the source's
        // readability, so the guard refuses the sidecar pathname, its
        // decorated spellings, and a hard-link alias of the sidecar
        // FILE, while a genuinely distinct file is accepted.
        let dir = std::env::temp_dir().join(format!(
            "iprange-refuse-sidecar-{}",
            std::process::id()
        ));
        fs::create_dir_all(&dir).unwrap();
        let source = dir.join("db.iprange");
        fs::write(&source, b"source").unwrap();
        let sidecar = iprange_livedb::sidecar_path(&source).expect("sidecar path");
        assert!(sidecar != source);
        // Both files exist, exactly like an open live database.
        fs::write(&sidecar, b"readers").unwrap();
        let identity = file_identity(&source).expect("identity");
        let sidecar_id = file_identity(&sidecar).expect("sidecar identity");
        for destination in [
            sidecar.clone(),
            dir.join("sub").join("..").join("db.iprange.readers"),
        ] {
            let error = refuse_output_over_source(&destination, &identity, Some(&sidecar_id))
                .unwrap_err();
            assert_eq!(error.code, "invalid_argument");
            assert_eq!(error.outcome, "not_started");
            assert_eq!(
                error.message,
                "destination must differ from the source database"
            );
        }
        // A hard-link alias of the sidecar file is refused through the
        // file-identity arm even though its pathname differs.
        let alias = dir.join("sidecar-alias.iprange");
        if fs::hard_link(&sidecar, &alias).is_ok() {
            assert!(refuse_output_over_source(&alias, &identity, Some(&sidecar_id)).is_err());
            let _ = fs::remove_file(&alias);
        }
        // A distinct file stays accepted.  This assertion runs BEFORE
        // the sidecar is renamed and unlinked: a fresh file created
        // after the sidecar inode was freed can reuse that exact inode
        // on common filesystems, which would make the captured-identity
        // comparison refuse it and the test nondeterministic (tester
        // role wave-19.6 determinism finding).
        let other = dir.join("other.iprange");
        fs::write(&other, b"other").unwrap();
        assert!(refuse_output_over_source(&other, &identity, Some(&sidecar_id)).is_ok());

        // A RENAMED sidecar keeps its captured identity: a destination
        // at the renamed path is refused through the file-identity arm
        // (tester role wave-19.6, mirroring the renamed-main arm).  An
        // ephemeral guard without the captured identity accepts the
        // renamed pathname because a fresh stat no longer matches it;
        // the wave-19.6 record documents this handle-vs-preflight
        // distinction.
        let renamed = dir.join("db.iprange.readers.old");
        fs::rename(&sidecar, &renamed).unwrap();
        let error = refuse_output_over_source(&renamed, &identity, Some(&sidecar_id)).unwrap_err();
        assert_eq!(
            (error.code, error.outcome, error.message.as_str()),
            (
                "invalid_argument",
                "not_started",
                "destination must differ from the source database",
            )
        );
        assert!(refuse_output_over_source(&renamed, &identity, None).is_ok());
        let _ = fs::remove_dir_all(&dir);
    }

    #[test]
    fn refuse_output_over_source_derives_the_sidecar_lexically_for_reserved_names() {
        // Parity finding wave-19.6: the sidecar derivation must be
        // purely lexical (Go pathname.FileName/WithFileName parity), so
        // a reserved-name source such as `x.readers` derives
        // `x.readers.readers` and the guard refuses that destination
        // preflight with the canonical shape instead of unarming the
        // sidecar arm and failing later at the SDK open with a
        // different machine-contract outcome.
        let dir = std::env::temp_dir().join(format!(
            "iprange-refuse-reserved-{}",
            std::process::id()
        ));
        fs::create_dir_all(&dir).unwrap();
        let source = dir.join("x.readers");
        fs::write(&source, b"coordination").unwrap();
        let identity = file_identity(&source).expect("identity");
        let sidecar = iprange_livedb::sidecar_path(&source).expect("sidecar path");
        assert_eq!(sidecar.file_name().unwrap(), "x.readers.readers");
        let error = refuse_output_over_source(&sidecar, &identity, None).unwrap_err();
        assert_eq!(error.code, "invalid_argument");
        assert_eq!(error.outcome, "not_started");
        assert_eq!(
            error.message,
            "destination must differ from the source database"
        );
        let _ = fs::remove_dir_all(&dir);
    }

    #[test]
    fn canonical_absolute_resolves_parent_dotdot_through_symlinked_ancestors() {
        // Wave 19 round 19.8 parity with the Go engine: ".." must be
        // resolved with symlink semantics against the deepest existing
        // ancestor ("<link>/.." pops the link TARGET's parent), not
        // folded lexically before symlinks are followed, so a
        // decorated spelling of the source still compares equal and
        // the same-source guard refuses it.
        let outer = std::env::temp_dir().join(format!(
            "iprange-canon-dotdot-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        let deeper = std::env::temp_dir().join(format!(
            "iprange-canon-dotdot-target-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        fs::create_dir_all(&outer).unwrap();
        fs::create_dir_all(&deeper).unwrap();
        if make_symlink(&deeper, &outer.join("link")).is_err() {
            let _ = fs::remove_dir_all(&outer);
            let _ = fs::remove_dir_all(&deeper);
            return; // platforms without symlink permission cannot test
        }
        let source = deeper.join("db.iprange");
        fs::write(&source, b"source").unwrap();
        let spelling = outer
            .join("link")
            .join("..")
            .join(deeper.file_name().unwrap())
            .join("db.iprange");
        assert_eq!(
            canonical_absolute(&spelling),
            canonical_absolute(&source),
            "spelling {}",
            spelling.display()
        );
        let identity = file_identity(&source).expect("identity");
        let error = refuse_output_over_source(&spelling, &identity, None).unwrap_err();
        assert_eq!(
            (error.code, error.outcome, error.message.as_str()),
            (
                "invalid_argument",
                "not_started",
                "destination must differ from the source database",
            )
        );
        let _ = fs::remove_dir_all(&outer);
        let _ = fs::remove_dir_all(&deeper);
    }

    #[test]
    fn canonical_absolute_resolves_parent_dotdot_before_missing_suffix() {
        // Wave 19 round 19.10 (astra turn-2 P2): a ".." component that
        // precedes a MISSING ancestor must keep symlink semantics.
        // Path::file_name returns None for a probe terminating in
        // ".."; the walk must pop the ParentDir explicitly instead of
        // lexically cleaning the whole original path (which would fold
        // "<link>/.." against the symlink name and miss the sidecar
        // pathname this spelling names).
        let outer = std::env::temp_dir().join(format!(
            "iprange-canon-premissing-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        let deeper = std::env::temp_dir().join(format!(
            "iprange-canon-premissing-target-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        fs::create_dir_all(&outer).unwrap();
        fs::create_dir_all(&deeper).unwrap();
        if make_symlink(&deeper, &outer.join("link")).is_err() {
            let _ = fs::remove_dir_all(&outer);
            let _ = fs::remove_dir_all(&deeper);
            return; // platforms without symlink permission cannot test
        }
        let source = deeper.join("db.iprange");
        fs::write(&source, b"source").unwrap();
        // <link>/.. resolves through the target to the parent of
        // deeper; the missing ancestor and its ".." then fold
        // lexically, landing on the sidecar pathname of the source.
        let spelling = outer
            .join("link")
            .join("..")
            .join(deeper.file_name().unwrap())
            .join("not-present-control")
            .join("..")
            .join("db.iprange.readers");
        let sidecar = deeper.join("db.iprange.readers");
        assert_eq!(
            canonical_absolute(&spelling),
            canonical_absolute(&sidecar),
            "spelling {}",
            spelling.display()
        );
        let identity = file_identity(&source).expect("identity");
        let error = refuse_output_over_source(&spelling, &identity, None).unwrap_err();
        assert_eq!(
            (error.code, error.outcome, error.message.as_str()),
            (
                "invalid_argument",
                "not_started",
                "destination must differ from the source database",
            )
        );
        let _ = fs::remove_dir_all(&outer);
        let _ = fs::remove_dir_all(&deeper);
    }

    #[test]
    fn refuse_output_over_source_sidecar_fallback_stats_the_derived_path() {
        // Wave 19 round 19.8 repair: the ephemeral sidecar fallback
        // stats the already-derived <main>.readers path directly.  A
        // second ".readers" suffix would derive <main>.readers.readers
        // and wrongly refuse a distinct destination that carries that
        // double-suffixed name.
        let dir = std::env::temp_dir().join(format!(
            "iprange-refuse-doublesuffix-{}",
            std::process::id()
        ));
        fs::create_dir_all(&dir).unwrap();
        let source = dir.join("db.iprange");
        fs::write(&source, b"source").unwrap();
        let identity = file_identity(&source).expect("identity");
        // The real sidecar (<main>.readers) does not exist; a distinct
        // file carries the double-suffixed name.
        let double = dir.join("db.iprange.readers.readers");
        fs::write(&double, b"distinct").unwrap();
        assert!(
            refuse_output_over_source(&double, &identity, None).is_ok(),
            "distinct double-suffixed destination must be accepted"
        );
        // The real sidecar pathname is still refused lexically.
        let sidecar = dir.join("db.iprange.readers");
        fs::write(&sidecar, b"readers").unwrap();
        let sidecar_id = file_identity(&sidecar).expect("sidecar identity");
        let error = refuse_output_over_source(&sidecar, &identity, Some(&sidecar_id)).unwrap_err();
        assert_eq!((error.code, error.outcome), ("invalid_argument", "not_started"));
        let _ = fs::remove_dir_all(&dir);
    }
