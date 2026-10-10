#![cfg(windows)]

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
            "iprange-v4-native-windows-{}-{unique}",
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

#[test]
fn external_c_caller_uses_the_windows_abi_and_utf16_paths() {
    let temporary = TemporaryDirectory::new();
    install_runtime(&temporary.0);
    let executable = compile_c(&temporary.0, "abi_windows.c");
    let output = Command::new(&executable)
        .arg(&temporary.0)
        .output()
        .unwrap();
    assert!(
        output.status.success(),
        "Windows C ABI behavior failed\nstdout:\n{}\nstderr:\n{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
}

fn compile_c(work: &Path, source_name: &str) -> PathBuf {
    let dependencies = std::env::current_exe()
        .unwrap()
        .parent()
        .unwrap()
        .to_path_buf();
    let import_library = dependencies.join("libiprange_v4.dll.a");
    let executable = work.join(source_name.replace(".c", ".exe"));
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
        // The fixtures' shared support header sits beside the sources;
        // the cygwin gcc's quote-include search does not resolve the
        // mixed-separator absolute source directory, so name it.
        .arg("-I")
        .arg(source.parent().unwrap())
        .arg(source)
        .arg(&import_library)
        .arg("-Wl,--no-undefined")
        .arg("-o")
        .arg(&executable);
    let output = command.output().unwrap();
    assert!(
        output.status.success(),
        "Windows C link failed for {source_name}\nstdout:\n{}\nstderr:\n{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
    executable
}

fn install_runtime(work: &Path) {
    let dependencies = std::env::current_exe()
        .unwrap()
        .parent()
        .unwrap()
        .to_path_buf();
    let library = dependencies.join("iprange_v4.dll");
    let worker = dependencies.parent().unwrap().join("iprange-v4-worker.exe");
    for artifact in [&library, &worker] {
        assert!(artifact.is_file(), "missing {}", artifact.display());
    }
    fs::copy(&library, work.join("iprange_v4.dll")).unwrap();
    fs::copy(&worker, work.join("iprange-v4-worker.exe")).unwrap();
}

#[test]
fn external_c_caller_reads_the_conformance_cases() {
    let temporary = TemporaryDirectory::new();
    install_runtime(&temporary.0);
    let executable = compile_c(&temporary.0, "abi_cases.c");
    let corpus = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../conformance");
    // The C entry acquires the OS wide command line itself (sol
    // turn-3): std::process passes UTF-16 arguments, and
    // CommandLineToArgvW inside abi_cases.c consumes them without
    // the ANSI code-page conversion the CRT's narrow argv would
    // impose — a checkout path containing non-ASCII survives.
    let output = Command::new(&executable).arg(&corpus).output().unwrap();
    let stdout = String::from_utf8_lossy(&output.stdout);
    assert!(
        output.status.success(),
        "Windows C cases.json run failed\nstdout:\n{stdout}\nstderr:\n{}",
        String::from_utf8_lossy(&output.stderr)
    );
    assert!(
        stdout.contains("cases=16 gaps=1007 address_count_exact=16"),
        "cases.json was not applied: {stdout}"
    );
}
