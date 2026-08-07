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
    let dependencies = std::env::current_exe()
        .unwrap()
        .parent()
        .unwrap()
        .to_path_buf();
    let library = dependencies.join("iprange_v4.dll");
    let import_library = dependencies.join("libiprange_v4.dll.a");
    let worker = dependencies.parent().unwrap().join("iprange-v4-worker.exe");
    for artifact in [&library, &import_library, &worker] {
        assert!(artifact.is_file(), "missing {}", artifact.display());
    }

    let executable = temporary.0.join("abi_windows.exe");
    let source = Path::new(env!("CARGO_MANIFEST_DIR")).join("tests/native/abi_windows.c");
    let include = Path::new(env!("CARGO_MANIFEST_DIR")).join("include");
    let compiler = std::env::var_os("CC").unwrap_or_else(|| OsString::from("cc"));
    let compiler = compiler.to_string_lossy();
    let mut words = compiler.split_ascii_whitespace();
    let mut command = Command::new(words.next().expect("C compiler"));
    command.args(words);
    let output = command
        .args(["-std=c11", "-Wall", "-Wextra", "-Werror"])
        .arg("-I")
        .arg(include)
        .arg(source)
        .arg(&import_library)
        .arg("-Wl,--no-undefined")
        .arg("-o")
        .arg(&executable)
        .output()
        .unwrap();
    assert!(
        output.status.success(),
        "Windows C ABI link failed\nstdout:\n{}\nstderr:\n{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    fs::copy(&library, temporary.0.join("iprange_v4.dll")).unwrap();
    fs::copy(&worker, temporary.0.join("iprange-v4-worker.exe")).unwrap();
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
