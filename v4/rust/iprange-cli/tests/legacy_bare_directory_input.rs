//! A directory given as an input file is an empty set, not an error.
//!
//! The released tool opens every input with `fopen(path, "r")`
//! (`iprange_fopen_read`, `src/iprange.h:57-69`). On Linux that call
//! succeeds for a directory, and the first `fgets()` then fails, so
//! `ipset_load()` reports no error and returns the empty ipset
//! (`src/ipset_load.c:270-280`; the IPv6 twin is identical,
//! `src/ipset6_load.c:200-218`). The observable contract is therefore
//! exit 0, no output and no diagnostic, whether or not the directory
//! holds files: its content is never read and it is never expanded.
//! Directory expansion is the separate `@directory` feature
//! (`src/iprange.c:749-841`), which does fail for an empty directory.
//!
//! The same rule reaches a directory named by a line of an `@file` list,
//! because `ipset_load()` is the only loader (`src/iprange.c:878`).
//!
//! The empty result is specific to a directory. `fopen()` is the one C
//! error path, and it fails before any read for a missing file
//! (`ENOENT`) and for a directory the caller may not open (`EACCES`), so
//! both keep the two C load lines and exit 1. The Rust loader therefore
//! maps `EISDIR` alone to an empty read (`read_legacy_input`).

#![cfg(unix)]

use std::ffi::OsStr;
use std::os::unix::ffi::OsStrExt;
use std::os::unix::fs::PermissionsExt;
use std::path::Path;

mod parity_support;
use parity_support::{assert_parity, raw, Scratch};

/// `@PATH`: the C file-list / directory source token.
fn at(path: &Path) -> std::ffi::OsString {
    let mut token = std::ffi::OsString::from("@");
    token.push(path);
    token
}

/// The two C load lines for a source that could not be opened.
fn load_failure(name: &str, reason: &str) -> Vec<u8> {
    format!("iprange: {name} - {reason}\niprange: Cannot load ipset: {name}\n").into_bytes()
}

#[test]
fn empty_directory_input_is_an_empty_set() {
    let dir = Scratch::new("empty-dir");
    let target = dir.path().join("empty");
    std::fs::create_dir(&target).expect("create the empty directory");

    assert_parity(
        "empty directory as input",
        &[target.as_os_str()],
        b"",
        (0, b"".as_slice(), b"".as_slice()),
    );
}

#[test]
fn directory_with_files_input_is_an_empty_set_and_is_not_expanded() {
    let dir = Scratch::new("dir-with-files");
    let target = dir.path().join("withfiles");
    std::fs::create_dir(&target).expect("create the directory");
    std::fs::write(target.join("f1"), b"10.0.0.1\n").expect("write f1");
    std::fs::write(target.join("f2"), b"10.0.0.2\n").expect("write f2");
    let file = dir.file(b"a", b"198.51.100.0/24\n");

    assert_parity(
        "directory with files as input",
        &[target.as_os_str()],
        b"",
        (0, b"".as_slice(), b"".as_slice()),
    );

    // The union prints only the file's range: had the directory been
    // expanded, 10.0.0.1 and 10.0.0.2 would appear as well.
    for (label, argv) in [
        (
            "file then directory",
            [file.as_os_str(), target.as_os_str()],
        ),
        (
            "directory then file",
            [target.as_os_str(), file.as_os_str()],
        ),
    ] {
        assert_parity(
            &format!("{label}: the directory contributes nothing"),
            &argv,
            b"",
            (0, b"198.51.100.0/24\n".as_slice(), b"".as_slice()),
        );
    }
}

#[test]
fn directory_input_in_ipv6_mode_is_an_empty_set() {
    let dir = Scratch::new("v6-empty-dir");
    let target = dir.path().join("empty");
    std::fs::create_dir(&target).expect("create the empty directory");

    assert_parity(
        "-6 with a directory input",
        &[OsStr::new("-6"), target.as_os_str()],
        b"",
        (0, b"".as_slice(), b"".as_slice()),
    );
}

#[test]
fn directory_named_by_a_file_list_entry_is_an_empty_set() {
    let dir = Scratch::new("list-dir-entry");
    let target = dir.path().join("withfiles");
    std::fs::create_dir(&target).expect("create the directory");
    std::fs::write(target.join("f1"), b"10.0.0.1\n").expect("write f1");

    // Entries are named by bytes, so the list is written from the raw
    // directory bytes rather than from a lossy text rendering.
    let mut bytes = target.as_os_str().as_bytes().to_vec();
    bytes.push(b'\n');

    let mut mixed = format!("{}/withfiles/f1\n", dir.path().to_string_lossy()).into_bytes();
    mixed.extend_from_slice(&bytes);
    let mixed_list = dir.file(b"mixed.list", &mixed);
    assert_parity(
        "@list whose entries are a file and a directory",
        &[at(&mixed_list).as_os_str()],
        b"",
        (0, b"10.0.0.1\n".as_slice(), b"".as_slice()),
    );

    let only_list_path = dir.path().join(raw(b"only.list"));
    std::fs::write(&only_list_path, &bytes).expect("write the list of one directory");
    assert_parity(
        "@list whose only entry is a directory",
        &[at(&only_list_path).as_os_str()],
        b"",
        (0, b"".as_slice(), b"".as_slice()),
    );
}

#[test]
fn a_missing_file_is_not_made_empty_by_the_directory_rule() {
    let dir = Scratch::new("missing-file");
    let target = dir.path().join("no-such-file");

    assert_parity(
        "missing file still fails",
        &[target.as_os_str()],
        b"",
        (
            1,
            b"".as_slice(),
            load_failure(&target.to_string_lossy(), "No such file or directory").as_slice(),
        ),
    );
}

#[test]
fn an_unopenable_directory_keeps_the_c_diagnostic() {
    if unsafe { libc::geteuid() } == 0 {
        // Root is not denied by mode 000, so this case cannot be set up.
        return;
    }
    let dir = Scratch::new("unreadable-dir");
    let target = dir.path().join("unreadable");
    std::fs::create_dir(&target).expect("create the directory");
    std::fs::write(target.join("f"), b"10.0.0.3\n").expect("write f");
    std::fs::set_permissions(&target, std::fs::Permissions::from_mode(0o000)).expect("chmod 000");

    // `fopen()` fails with EACCES before any read happens, so this is
    // not the directory case and the C diagnostic must survive.
    let name = target.to_string_lossy().into_owned();
    assert_parity(
        "directory the caller may not open",
        &[target.as_os_str()],
        b"",
        (
            1,
            b"".as_slice(),
            load_failure(&name, "Permission denied").as_slice(),
        ),
    );

    std::fs::set_permissions(&target, std::fs::Permissions::from_mode(0o755))
        .expect("restore the mode so the scratch directory can be removed");
}

#[test]
fn empty_directory_still_fails_as_an_at_source() {
    let dir = Scratch::new("at-empty-dir");
    let target = dir.path().join("empty");
    std::fs::create_dir(&target).expect("create the empty directory");
    let expected = format!(
        "iprange: No valid files found in directory: {}\n",
        target.display()
    )
    .into_bytes();

    // `@directory` expansion is a different feature: C requires at least
    // one regular file there and says so.
    assert_parity(
        "@empty directory still fails",
        &[at(&target).as_os_str()],
        b"",
        (1, b"".as_slice(), expected.as_slice()),
    );

    // A `@directory` that does hold regular files loads them, which is
    // what distinguishes it from the bare-directory case above.
    let with_files = target.with_file_name("withfiles");
    std::fs::create_dir(&with_files).expect("create the second directory");
    std::fs::write(with_files.join("f1"), b"10.0.0.1\n").expect("write f1");
    std::fs::write(with_files.join("f2"), b"10.0.0.2\n").expect("write f2");

    assert_parity(
        "@directory with files loads them",
        &[at(&with_files).as_os_str()],
        b"",
        (0, b"10.0.0.1\n10.0.0.2\n".as_slice(), b"".as_slice()),
    );
}
