//! The v4 fixture generator must accept a non-UTF-8 output path.
//!
//! `examples/v4-fixture.rs` is a development-only tool, and like every
//! other binary in this repository it reads argv. A POSIX path may hold
//! bytes that are not valid UTF-8, which `std::env::args()` reports by
//! panicking (exit 101) instead of running. The fixture's job is to write
//! a file at the requested path, so an arbitrary byte in that path is
//! ordinary input, not a fatal startup error.
//!
//! The assertion is parity rather than a pinned exit code: the same run
//! with a valid name and with an invalid-UTF-8 name must be classified
//! identically and must never panic. That holds whether or not the SDK
//! workflow behind the fixture succeeds in the current environment.

#![cfg(unix)]

use std::ffi::OsString;
use std::os::unix::ffi::OsStringExt;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::atomic::{AtomicUsize, Ordering};

/// The example binary, found the way the SDK finds its worker: cargo
/// puts integration tests in `target/<profile>/deps` and examples in
/// the sibling `examples` directory.
fn fixture_program() -> PathBuf {
    let current = std::env::current_exe().expect("path of this test binary");
    let deps = current.parent().expect("deps directory of this test");
    let target = deps.parent().expect("target directory of this test");
    let program = target
        .join("examples")
        .join(format!("v4-fixture{}", std::env::consts::EXE_SUFFIX));
    assert!(
        program.is_file(),
        "{} must be built by `cargo test -p iprange-livedb` (it is an \
         example target of this package)",
        program.display()
    );
    program
}

fn raw(bytes: &[u8]) -> OsString {
    OsStringExt::from_vec(bytes.to_vec())
}

/// Serial number of the next scratch directory, so that no two runs in
/// one test process share a directory.
static SCRATCH_SEQ: AtomicUsize = AtomicUsize::new(0);

/// Run the fixture on a scratch output path built from `name`.
///
/// The directory is unique per call, not per name: the fixture keeps its
/// live and immutable-source sidecars next to the output, and the tests
/// run in parallel threads over the same names. A name-derived directory
/// would let one test's cleanup delete another test's in-flight sidecars,
/// which shows up as an exit-code mismatch against the very same fixture.
fn run(program: &Path, name: &[u8]) -> std::process::Output {
    let mut dir = std::env::temp_dir();
    dir.push(format!(
        "iprange-fixture-argv-{}-{}-{}",
        std::process::id(),
        SCRATCH_SEQ.fetch_add(1, Ordering::Relaxed),
        String::from_utf8_lossy(name)
            .chars()
            .map(|c| if c.is_ascii_alphanumeric() { c } else { '_' })
            .collect::<String>()
    ));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).expect("create the scratch directory");
    let output_path = dir.join(raw(name));
    let output = Command::new(program)
        .arg("structured-v4")
        .arg(&output_path)
        .stdin(std::process::Stdio::null())
        .output()
        .expect("spawn v4-fixture");
    std::fs::remove_dir_all(&dir).ok();
    output
}

#[test]
fn non_utf8_output_path_is_not_a_panic() {
    let program = fixture_program();
    let invalid = run(&program, b"fixture\xff.v4db");
    assert_ne!(
        invalid.status.code(),
        Some(101),
        "argv decoding must not panic: {}",
        String::from_utf8_lossy(&invalid.stderr)
    );
    assert!(
        !String::from_utf8_lossy(&invalid.stderr).contains("panicked at"),
        "v4-fixture panicked on a legal POSIX path: {}",
        String::from_utf8_lossy(&invalid.stderr)
    );
}

#[test]
fn non_utf8_output_path_is_classified_like_a_valid_one() {
    let program = fixture_program();
    let valid = run(&program, b"fixture.v4db");
    let invalid = run(&program, b"fixture\xff.v4db");
    assert_eq!(
        valid.status.code(),
        invalid.status.code(),
        "an invalid-UTF-8 byte in the output path must not change the \
         exit classification; stderr of the invalid run: {}",
        String::from_utf8_lossy(&invalid.stderr)
    );
    assert_eq!(
        valid.stderr, invalid.stderr,
        "the same workflow must report the same diagnostics"
    );
}
