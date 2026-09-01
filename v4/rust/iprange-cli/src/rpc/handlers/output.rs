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

pub fn metadata_output(
    path: &Path,
    bytes: &[u8],
    policy: PublicationPolicy,
    max_output_bytes: u64,
    max_open_files: u32,
) -> Result<Value, HandlerError> {
    if bytes.len() as u64 > max_output_bytes {
        return Err(HandlerError::new(
            "output_limit",
            "not_started",
            format!(
                "metadata output is {} bytes, limit is {max_output_bytes}",
                bytes.len()
            ),
        ));
    }
    if max_open_files < 1 {
        return Err(HandlerError::new(
            "invalid_argument",
            "not_started",
            "metadata file delivery requires at least one open file",
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
    temporary.push(format!(".{}.metadata.tmp", new_handle()));
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
    if error.kind() == std::io::ErrorKind::AlreadyExists {
        HandlerError::new("name_exists", "not_started", message)
    } else {
        HandlerError::new("io", "not_started", message)
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
