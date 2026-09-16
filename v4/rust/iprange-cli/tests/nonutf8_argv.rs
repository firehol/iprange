//! Non-UTF-8 argv on the released legacy surface.
//!
//! A POSIX file name is any sequence of non-NUL, non-slash bytes, so a
//! legal name may hold bytes that are not valid UTF-8 (here 0xFF). The
//! released C tool and the Go port treat such an argument as an ordinary
//! input path and classify the run by its content, exiting 0 or 1.
//! `std::env::args()` instead panics while decoding argv, which is exit
//! 101 with no output, and it does so before `--jsonrpc` is examined, so
//! both surfaces are affected.
//!
//! Contract pinned here, per case:
//! - the exit code is the C/Go classification (never the panic code);
//! - stdout bytes are identical to the C/Go bytes, including the argv
//!   bytes echoed back by `as NAME`, `--print-prefix`/`--print-suffix`
//!   and the `-h` usage line;
//! - the one diagnostic whose text the C builds from argv bytes
//!   (`Invalid value`) is byte-identical too.
//!
//! Load-path diagnostics (`Cannot load ipset: <path>`, `Cannot access ...`,
//! `Cannot load file ... from list ...`) are byte-exact too: the loader
//! carries them as bytes (see `legacy::diag`), and
//! `tests/legacy_nonutf8_load_path.rs` pins their equality with the C
//! bytes, together with the requirement that an `@list` record opens the
//! file named by its bytes.

#![cfg(unix)]

use std::ffi::{OsStr, OsString};
use std::os::unix::ffi::OsStringExt;
use std::os::unix::process::CommandExt;
use std::path::PathBuf;
use std::process::{Command, Stdio};

const PROGRAM: &str = env!("CARGO_BIN_EXE_iprange");
/// Rust's panic exit code; reaching it is the defect under test.
const PANIC_CODE: i32 = 101;
/// True when `haystack` contains `needle` (byte substring search).
fn contains(haystack: &[u8], needle: &[u8]) -> bool {
    haystack
        .windows(needle.len())
        .any(|window| window == needle)
}

/// An OS string holding raw bytes, i.e. what a real command line can
/// carry for a file named with `printf '\377'`.
fn raw(bytes: &[u8]) -> OsString {
    OsStringExt::from_vec(bytes.to_vec())
}

fn scratch(tag: &str) -> PathBuf {
    let mut dir = std::env::temp_dir();
    dir.push(format!("iprange-nonutf8-argv-{}-{tag}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).expect("create the scratch directory");
    dir
}

/// Run the product binary with these exact argv bytes and no stdin.
fn run(args: &[&OsStr]) -> std::process::Output {
    Command::new(PROGRAM)
        .args(args.iter().copied())
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .expect("spawn iprange")
        .wait_with_output()
        .expect("wait for iprange")
}

fn assert_no_panic(output: &std::process::Output, case: &str) {
    assert_ne!(
        output.status.code(),
        Some(PANIC_CODE),
        "{case}: argv decoding must not panic (stderr: {})",
        String::from_utf8_lossy(&output.stderr)
    );
    assert!(
        !String::from_utf8_lossy(&output.stderr).contains("panicked at"),
        "{case}: iprange panicked instead of classifying argv: {}",
        String::from_utf8_lossy(&output.stderr)
    );
}

/// The scratch file `name` holding `payload`.
fn file_with(dir: &PathBuf, name: &[u8], payload: &[u8]) -> PathBuf {
    let path = dir.join(raw(name));
    std::fs::write(&path, payload).expect("write the input file");
    path
}

#[test]
fn non_utf8_input_path_loads_its_content() {
    let dir = scratch("path");
    let path = file_with(&dir, b"a\xffb.iprange", b"1.2.3.4\n");
    let output = run(&[path.as_os_str()]);
    assert_no_panic(&output, "input path with 0xFF");
    assert_eq!(output.status.code(), Some(0), "C and Go exit 0 here");
    assert_eq!(output.stdout, b"1.2.3.4\n", "the file content printed");
    assert!(output.stderr.is_empty(), "a valid load stays silent");
    std::fs::remove_dir_all(&dir).expect("clean the scratch directory");
}

#[test]
fn non_utf8_missing_path_exits_one() {
    let dir = scratch("missing");
    let path = dir.join(raw(b"nope\xffx.iprange"));
    let output = run(&[path.as_os_str()]);
    assert_no_panic(&output, "missing input path with 0xFF");
    assert_eq!(
        output.status.code(),
        Some(1),
        "C and Go report the failed load with exit 1"
    );
    assert!(output.stdout.is_empty());
    std::fs::remove_dir_all(&dir).expect("clean the scratch directory");
}

#[test]
fn one_non_utf8_argument_does_not_disturb_the_others() {
    let dir = scratch("mixed");
    let bad = file_with(&dir, b"a\xffb.iprange", b"1.2.3.4\n");
    let good = file_with(&dir, b"plain.iprange", b"5.6.7.8\n");
    let output = run(&[bad.as_os_str(), good.as_os_str()]);
    assert_no_panic(&output, "two inputs, one name with 0xFF");
    assert_eq!(output.status.code(), Some(0));
    assert_eq!(output.stdout, b"1.2.3.4\n5.6.7.8\n");
    std::fs::remove_dir_all(&dir).expect("clean the scratch directory");
}

#[test]
fn non_utf8_as_label_is_printed_verbatim_in_the_csv_name() {
    let dir = scratch("label");
    let path = file_with(&dir, b"plain.iprange", b"5.6.7.8\n");
    let label = raw(b"lab\xffel");
    let output = run(&[
        path.as_os_str(),
        OsStr::new("as"),
        label.as_os_str(),
        OsStr::new("--count-unique-all"),
    ]);
    assert_no_panic(&output, "as NAME with 0xFF");
    assert_eq!(output.status.code(), Some(0));
    assert_eq!(
        output.stdout, b"lab\xffel,1,1\n",
        "the CSV name column carries the argv bytes, as C and Go do"
    );
    std::fs::remove_dir_all(&dir).expect("clean the scratch directory");
}

#[test]
fn non_utf8_print_wrapper_is_printed_verbatim() {
    let dir = scratch("wrapper");
    let path = file_with(&dir, b"plain.iprange", b"5.6.7.8\n");
    let prefix = raw(b"x\xffy");
    let output = run(&[
        OsStr::new("--print-prefix"),
        prefix.as_os_str(),
        path.as_os_str(),
    ]);
    assert_no_panic(&output, "--print-prefix with 0xFF");
    assert_eq!(output.status.code(), Some(0));
    assert_eq!(output.stdout, b"x\xffy5.6.7.8\n");

    let suffix = raw(b"s\xff");
    let output = run(&[
        OsStr::new("--print-suffix-ips"),
        suffix.as_os_str(),
        path.as_os_str(),
    ]);
    assert_no_panic(&output, "--print-suffix-ips with 0xFF");
    assert_eq!(output.status.code(), Some(0));
    assert_eq!(output.stdout, b"5.6.7.8s\xff\n");
    std::fs::remove_dir_all(&dir).expect("clean the scratch directory");
}

#[test]
fn non_utf8_option_value_is_rejected_with_the_c_diagnostic() {
    let output = run(&[OsStr::new("--min-prefix"), raw(b"a\xffb").as_os_str()]);
    assert_no_panic(&output, "--min-prefix value with 0xFF");
    assert_eq!(
        output.status.code(),
        Some(1),
        "an invalid value is a usage failure, not a panic"
    );
    assert_eq!(
        output.stderr,
        b"iprange: Invalid value 'a\xffb' for --min-prefix. It must be between 1 and 32.\n",
        "C echoes the argv bytes verbatim in this line"
    );
}

#[test]
fn non_utf8_argument_next_to_jsonrpc_is_rejected_without_a_panic() {
    // The panic used to happen while collecting argv, before mode
    // selection, so it killed the JSON-RPC startup check too.
    let output = run(&[OsStr::new("--jsonrpc"), raw(b"a\xffb").as_os_str()]);
    assert_no_panic(&output, "--jsonrpc plus a name with 0xFF");
    assert_eq!(output.status.code(), Some(1));
    assert_eq!(
        output.stderr,
        b"iprange: --jsonrpc cannot be combined with other arguments\n"
    );
}

#[test]
fn non_utf8_invocation_name_is_printed_verbatim_in_the_usage() {
    let mut command = Command::new(PROGRAM);
    command
        .arg0(raw(b"prog\xffname").as_os_str())
        .arg("-h")
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    let output = command
        .spawn()
        .expect("spawn iprange")
        .wait_with_output()
        .expect("wait for iprange");
    assert_no_panic(&output, "argv[0] with 0xFF");
    assert_eq!(output.status.code(), Some(0));
    assert!(
        contains(&output.stdout, b"Usage: prog\xffname [options]"),
        "the usage line must echo the invocation name bytes, got {}",
        String::from_utf8_lossy(&output.stdout)
    );
}

/// The `-h` text is emitted by the byte substitution in
/// `legacy::usage::print`, so the ordinary (all-ASCII) invocation must
/// keep the released text exactly: the C prints the same format with
/// `fprintf(stdout, format, argv[0], dns_threads_max)`. This is the only
/// pin for the `%d` directive, which the option scan must feed with the
/// value in effect at `-h`, and for the directive-in-name case that a
/// two-pass `replace` would have rescanned.
#[test]
fn ascii_usage_keeps_the_released_text() {
    let program = OsStr::new(PROGRAM);
    let output = run(&[OsStr::new("-h")]);
    assert_eq!(output.status.code(), Some(0));
    assert!(
        output
            .stdout
            .starts_with(b"iprange manages IP ranges\n\nUsage: "),
        "the usage header changed: {}",
        String::from_utf8_lossy(&output.stdout)
    );
    let name = std::fs::canonicalize(program).expect("canonical program path");
    let usage_line = format!(
        "Usage: {} [options] file1 file2 file3 ...\n",
        name.display()
    );
    assert!(
        output
            .stdout
            .as_slice()
            .windows(usage_line.len())
            .any(|window| window == usage_line.as_bytes()),
        "the usage line must carry the full argv[0], got {}",
        String::from_utf8_lossy(&output.stdout)
    );
    assert!(
        contains(&output.stdout, b"(the default is 5)."),
        "the --dns-threads default must be substituted into %d"
    );

    // A percent directive inside the invocation name is data, not a
    // directive: C `printf` never rescans what it substituted.
    let mut command = Command::new(PROGRAM);
    command
        .arg0(OsStr::new("pro%gs"))
        .arg("-h")
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    let output = command
        .spawn()
        .expect("spawn iprange")
        .wait_with_output()
        .expect("wait for iprange");
    assert_eq!(output.status.code(), Some(0));
    assert!(
        contains(&output.stdout, b"Usage: pro%gs [options]"),
        "a percent sign in argv[0] must be printed verbatim, got {}",
        String::from_utf8_lossy(&output.stdout)
    );
}

#[test]
fn unrecognized_non_utf8_token_is_an_input_path() {
    // C compares argv bytes, so `--optimize\xff` is not the merge flag:
    // it is a file name, and loading it fails with exit 1.
    let dir = scratch("unknown-flag");
    let path = file_with(&dir, b"plain.iprange", b"5.6.7.8\n");
    let output = run(&[raw(b"--optimize\xff").as_os_str(), path.as_os_str()]);
    assert_no_panic(&output, "option-shaped name with 0xFF");
    assert_eq!(
        output.status.code(),
        Some(1),
        "the token is a path that does not exist, as in C and Go"
    );
    assert!(output.stdout.is_empty(), "nothing merged");
    std::fs::remove_dir_all(&dir).expect("clean the scratch directory");
}
