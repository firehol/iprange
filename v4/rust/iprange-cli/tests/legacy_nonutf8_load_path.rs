//! Load-path diagnostics for names that are not valid UTF-8.
//!
//! A POSIX file name is any sequence of non-NUL, non-slash bytes, and an
//! `@list` record names a file the same way, so a legal name may hold
//! bytes such as 0xFF. The C tool opens those bytes and writes them back
//! verbatim in its diagnostics (`fprintf(stderr, "%s", ...)` over the
//! name it was given; `src/iprange.c` `Cannot load ipset: %s`, the
//! `FILE_LIST` branch's `Cannot load file %s from list %s (line %d)`, and
//! `src/ipset_load.c` `Cannot understand line No %d from %s: %s`).
//!
//! Two defects are pinned here:
//!
//! - the file named by an `@list` line must be opened by its **bytes**:
//!   decoding the record to text turns `bad\377name` into a path holding
//!   U+FFFD, which does not exist, so the run exits 1 where C merges the
//!   file and exits 0;
//! - the load diagnostics must carry those bytes: the exit code and
//!   stdout were already right while stderr rendered U+FFFD, which is a
//!   wrong byte stream on the channel the released tool owns.
//!
//! Every case compares all three channels, and the C reference is
//! consulted when installed, so the expectation cannot drift from the
//! oracle silently.

#![cfg(unix)]

use std::ffi::OsStr;
use std::os::unix::ffi::OsStrExt;
use std::path::{Path, PathBuf};

mod parity_support;
use parity_support::{assert_parity, raw, Scratch};

/// The `@ARGUMENT` token that names `path`, built from bytes so the
/// argument carries the name exactly (a lossy `display()` would hand
/// the tool a different argument than the one under test).
fn at_arg(path: &Path) -> std::ffi::OsString {
    let mut bytes = Vec::from(b"@");
    bytes.extend_from_slice(path.as_os_str().as_bytes());
    raw(&bytes)
}

/// A path built from raw bytes: directory, then `/`, then `name`.
fn path_from_raw(dir: &Path, name: &[u8]) -> PathBuf {
    let mut bytes = Vec::from(dir.as_os_str().as_bytes());
    bytes.push(b'/');
    bytes.extend_from_slice(name);
    PathBuf::from(OsStr::from_bytes(&bytes))
}

/// The C two-line open failure for `name`.
fn open_failure(name: &[u8]) -> Vec<u8> {
    let mut out = Vec::from(b"iprange: ");
    out.extend_from_slice(name);
    out.extend_from_slice(b" - No such file or directory\niprange: Cannot load ipset: ");
    out.extend_from_slice(name);
    out.push(b'\n');
    out
}

/// The C context line for a file named by a list line.
fn list_context(entry: &[u8], list: &[u8], line: &str) -> Vec<u8> {
    let mut out = Vec::from(b"iprange: ");
    out.extend_from_slice(entry);
    out.extend_from_slice(b" - No such file or directory\niprange: Cannot load file ");
    out.extend_from_slice(entry);
    out.extend_from_slice(b" from list ");
    out.extend_from_slice(list);
    out.extend_from_slice(b" (line ");
    out.extend_from_slice(line.as_bytes());
    out.push(b')');
    out.push(b'\n');
    out
}

#[test]
fn missing_input_named_with_an_invalid_byte_is_reported_by_bytes() {
    let dir = Scratch::new("load-argv");
    let missing = path_from_raw(dir.path(), b"nope\xffx.iprange");
    assert_parity(
        "argv name with 0xFF",
        &[missing.as_os_str()],
        b"",
        (
            1,
            b"".as_slice(),
            open_failure(missing.as_os_str().as_bytes()).as_slice(),
        ),
    );
}

#[test]
fn a_list_line_opens_the_exact_bytes_of_the_name() {
    // The regression: decoding the record opened `bad\ufffdname` (which
    // does not exist) instead of `bad\xffname`, turning a successful
    // merge into exit 1.
    let dir = Scratch::new("load-list-open");
    let target = path_from_raw(dir.path(), b"bad\xffname.iprange");
    std::fs::write(&target, b"4.4.4.4\n").unwrap();
    let list = dir.path().join("list.txt");
    // The record is the name's bytes, not a rendering of them.
    let mut record = Vec::from(target.as_os_str().as_bytes());
    record.push(b'\n');
    std::fs::write(&list, &record).unwrap();
    let list_at = at_arg(&list);

    assert_parity(
        "@list line naming a 0xFF file",
        &[list_at.as_ref()],
        b"",
        (0, b"4.4.4.4\n".as_slice(), b"".as_slice()),
    );
    // A record that names the file relative to the process directory is
    // the same rule; tests.d/108-nonutf8-load-path runs it with the
    // fixture directory as the working directory (changing the process
    // directory here would race the other tests in this binary).
}

#[test]
fn a_missing_list_entry_reports_both_names_by_bytes() {
    // The list itself and the entry it names can each hold invalid
    // bytes; C echoes both verbatim.
    let dir = Scratch::new("load-list-missing");
    let list = path_from_raw(dir.path(), b"badlist\xff.txt");
    std::fs::write(&list, b"nope\xffx.iprange\n").unwrap();
    let list_at = at_arg(&list);
    assert_parity(
        "@list with a 0xFF entry that does not exist",
        &[list_at.as_ref()],
        b"",
        (
            1,
            b"".as_slice(),
            list_context(b"nope\xffx.iprange", list.as_os_str().as_bytes(), "1").as_slice(),
        ),
    );
}

#[test]
fn missing_list_and_missing_directory_report_their_bytes() {
    let dir = Scratch::new("load-list-dir");
    let no_list = path_from_raw(dir.path(), b"nolist\xff.txt");
    let no_list_at = at_arg(&no_list);
    assert_parity(
        "@ of a missing list",
        &[no_list_at.as_ref()],
        b"",
        (
            1,
            b"".as_slice(),
            {
                let mut out = Vec::from(b"iprange: Cannot access ");
                out.extend_from_slice(no_list.as_os_str().as_bytes());
                out.extend_from_slice(b": No such file or directory\n");
                out
            }
            .as_slice(),
        ),
    );

    let no_dir = path_from_raw(dir.path(), b"nodir\xff");
    let no_dir_at = at_arg(&no_dir);
    let mut want = Vec::from(b"iprange: Cannot access ");
    want.extend_from_slice(no_dir.as_os_str().as_bytes());
    want.extend_from_slice(b": No such file or directory\n");
    assert_parity(
        "@ of a missing directory",
        &[no_dir_at.as_ref()],
        b"",
        (1, b"".as_slice(), want.as_slice()),
    );

    // An existing directory whose *name* holds the invalid byte, with no
    // loadable entries, reports that name by bytes too.
    let empty = path_from_raw(dir.path(), b"empty\xff");
    std::fs::create_dir(&empty).unwrap();
    let empty_at = at_arg(&empty);
    let mut want = Vec::from(b"iprange: No valid files found in directory: ");
    want.extend_from_slice(empty.as_os_str().as_bytes());
    want.push(b'\n');
    assert_parity(
        "@ of an empty directory named with 0xFF",
        &[empty_at.as_ref()],
        b"",
        (1, b"".as_slice(), want.as_slice()),
    );
}

#[test]
fn a_directory_entry_named_with_an_invalid_byte_loads() {
    let dir = Scratch::new("load-dir-entry");
    let inner = dir.path().join("dir");
    std::fs::create_dir(&inner).unwrap();
    let bad = path_from_raw(&inner, b"z\xffy.txt");
    std::fs::write(&bad, b"9.9.9.9\n").unwrap();
    let good = inner.join("a.txt");
    std::fs::write(&good, b"8.8.8.8\n").unwrap();
    let inner_at = at_arg(&inner);

    assert_parity(
        "@dir holding a 0xFF entry",
        &[inner_at.as_ref()],
        b"",
        (0, b"8.8.8.8\n9.9.9.9\n".as_slice(), b"".as_slice()),
    );
}

#[test]
fn a_parse_failure_in_a_file_named_with_an_invalid_byte_keeps_the_bytes() {
    // `Cannot understand line No N from NAME: RECORD` embeds the name
    // and the raw record; C writes both with `%s`.
    let dir = Scratch::new("load-parse-error");
    let bad = path_from_raw(dir.path(), b"bad\xffparse.txt");
    std::fs::write(&bad, b"1.2.3.4/99\n").unwrap();

    let mut want =
        Vec::from(b"iprange: Invalid netmask 99\niprange: Cannot understand line No 1 from ");
    want.extend_from_slice(bad.as_os_str().as_bytes());
    want.extend_from_slice(b": 1.2.3.4/99\n\niprange: Cannot load ipset: ");
    want.extend_from_slice(bad.as_os_str().as_bytes());
    want.push(b'\n');
    assert_parity(
        "parse failure inside a 0xFF-named file",
        &[bad.as_os_str()],
        b"",
        (1, b"".as_slice(), want.as_slice()),
    );

    // The point of the fix stated directly: the raw byte is present and
    // no substitution character replaced it.
    let engine = PathBuf::from(parity_support::PROGRAM);
    let run = parity_support::run_bounded(&engine, &[bad.as_os_str()], b"");
    assert!(
        run.stderr.contains(&0xffu8),
        "stderr must carry the raw name byte: {:?}",
        String::from_utf8_lossy(&run.stderr)
    );
    assert!(
        !contains(&run.stderr, "\u{fffd}".as_bytes()),
        "stderr must not contain U+FFFD: {:?}",
        String::from_utf8_lossy(&run.stderr)
    );
}

/// Byte-substring search.
fn contains(haystack: &[u8], needle: &[u8]) -> bool {
    !needle.is_empty()
        && haystack
            .windows(needle.len())
            .any(|window| window == needle)
}

#[test]
fn a_bad_value_is_reported_with_the_argv_bytes_too() {
    // Adjacent surface, same rule: the option value is argv, and C
    // echoes its bytes (`src/iprange.c` parse_long_option_or_die).
    let bad = raw(b"1\xff2");
    let want = b"iprange: Invalid value '1\xff2' for --min-prefix. It must be between 1 and 32.\n"
        .to_vec();
    assert_parity(
        "--min-prefix value with 0xFF",
        &[OsStr::new("--min-prefix"), bad.as_os_str()],
        b"",
        (1, b"".as_slice(), want.as_slice()),
    );
}
