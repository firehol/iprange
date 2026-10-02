//! The portable C-ABI checks on every unix host.
//!
//! `native_behavior.rs` is gated to Linux because it asserts POSIX
//! path kinds and live entries, which FreeBSD refuses before any of
//! that runs. The path-free error codes are portable and run here on
//! every unix. The conformance-corpus execution is owned by
//! `native_behavior.rs` on Linux (one owner; sol turn-2) and by this
//! file on every other unix, where it is the only attestation: a
//! macOS or FreeBSD build that mis-reads a fixture or returns the
//! wrong code for an empty path fails here.

#![cfg(unix)]

use std::ffi::OsString;
use std::fs;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::time::{SystemTime, UNIX_EPOCH};

struct TemporaryDirectory(PathBuf);

impl TemporaryDirectory {
    fn new() -> Self {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let path = std::env::temp_dir().join(format!(
            "iprange-v4-native-unix-{}-{unique}",
            std::process::id()
        ));
        fs::create_dir(&path).unwrap();
        Self(path)
    }
}

impl Drop for TemporaryDirectory {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}

fn dependencies() -> PathBuf {
    std::env::current_exe().unwrap().parent().unwrap().to_path_buf()
}

fn shared_library() -> PathBuf {
    let directory = dependencies();
    for name in ["libiprange_v4.dylib", "libiprange_v4.so"] {
        let candidate = directory.join(name);
        if candidate.is_file() {
            return candidate;
        }
    }
    panic!("no shared library beside the test executable");
}

fn worker_binary() -> PathBuf {
    dependencies().parent().unwrap().join("iprange-v4-worker")
}

fn compile_c(work: &Path, source_name: &str) -> PathBuf {
    let library = shared_library();
    let deps = library.parent().unwrap();
    let stem = source_name.strip_suffix(".c").unwrap_or(source_name);
    let executable = work.join(stem);
    let source = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("tests/native")
        .join(source_name);
    let include = Path::new(env!("CARGO_MANIFEST_DIR")).join("include");
    let compiler = std::env::var_os("CC").unwrap_or_else(|| OsString::from("cc"));
    let compiler = compiler.to_string_lossy();
    let mut words = compiler.split_ascii_whitespace();
    let mut command = Command::new(words.next().expect("C compiler"));
    command.args(words);
    command
        .args(["-std=c11", "-Wall", "-Wextra", "-Werror"])
        .arg("-I")
        .arg(include)
        .arg(source)
        .arg(&library)
        .arg(format!("-Wl,-rpath,{}", deps.display()))
        .arg("-o")
        .arg(&executable);
    let output = command.output().unwrap();
    assert!(
        output.status.success(),
        "native C link failed for {source_name}\nstdout:\n{}\nstderr:\n{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
    let _ = fs::copy(worker_binary(), work.join("iprange-v4-worker"));
    executable
}

#[test]
fn native_c_portable_errors_hold_on_this_host() {
    let temporary = TemporaryDirectory::new();
    let executable = compile_c(&temporary.0, "abi_portable.c");
    let output = Command::new(&executable).output().unwrap();
    assert!(
        output.status.success(),
        "portable C error codes failed\nstdout:\n{}\nstderr:\n{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
}

#[cfg(not(target_os = "linux"))]
#[test]
fn native_c_reads_conformance_cases_on_this_host() {
    let temporary = TemporaryDirectory::new();
    let executable = compile_c(&temporary.0, "abi_cases.c");
    let corpus = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../conformance");
    let output = Command::new(&executable).arg(&corpus).output().unwrap();
    let stdout = String::from_utf8_lossy(&output.stdout);
    assert!(
        output.status.success(),
        "native C cases.json run failed\nstdout:\n{stdout}\nstderr:\n{}",
        String::from_utf8_lossy(&output.stderr)
    );
    // The gap count is corpus-derived (see native_behavior.rs): every
    // non-adjacent manifest range pair is verified absent through the C
    // ABI; the current corpus implies 1007 such holes.
    assert!(
        stdout.contains("cases=16 gaps=1007 address_count_exact=16"),
        "cases.json was not applied: {stdout}"
    );
}
