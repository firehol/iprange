#![cfg(target_os = "linux")]

use std::ffi::OsString;
use std::fs;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::time::{SystemTime, UNIX_EPOCH};

use serde_json::Value;

const MANIFEST: &str = include_str!("../include/iprange_v4_abi1_manifest.json");
const NATIVE_SOURCES: &[&str] = &[
    include_str!("native/abi_behavior.c"),
    include_str!("native/abi_lifecycle.c"),
    include_str!("native/abi_maintenance.c"),
    include_str!("native/abi_membership.c"),
    include_str!("native/abi_workflows.c"),
    include_str!("native/abi_sdk.c"),
];

struct TestFiles {
    directory: PathBuf,
    main: PathBuf,
    snapshot: PathBuf,
}

impl TestFiles {
    fn new() -> Self {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let directory = std::env::temp_dir().join(format!(
            "iprange-v4-native-behavior-{}-{unique}",
            std::process::id()
        ));
        fs::create_dir(&directory).unwrap();
        Self {
            main: directory.join("main.ipr"),
            snapshot: directory.join("snapshot.ipr"),
            directory,
        }
    }
}

impl Drop for TestFiles {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.directory);
    }
}

#[test]
fn native_c_caller_exercises_the_real_shared_library() {
    let files = TestFiles::new();
    let library = shared_library();
    let dependencies = library.parent().unwrap();
    let panic_shim = compile_panic_shim(&files.directory, dependencies);
    assert!(
        library.is_file(),
        "cargo did not build {}",
        library.display()
    );
    let executable = compile_c_fixture(&files, "abi_behavior.c", &[panic_shim]);
    let output = run_fixture(&executable, [&files.main, &files.snapshot]);
    assert!(
        output.status.success(),
        "native C behavior failed\nstdout:\n{}\nstderr:\n{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
}

#[test]
fn native_c_membership_surface_uses_the_real_shared_library() {
    let files = TestFiles::new();
    let executable = compile_c_fixture(&files, "abi_membership.c", &[]);
    let output = run_fixture(&executable, [&files.main, &files.snapshot]);
    assert!(
        output.status.success(),
        "native C membership behavior failed\nstdout:\n{}\nstderr:\n{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
}

#[test]
fn native_c_workflows_use_the_real_shared_library() {
    let files = TestFiles::new();
    let source = files.directory.join("source.ipr");
    let destination = files.directory.join("destination.ipr");
    let direct = files.directory.join("direct.ipr");
    let first_seen = files.directory.join("first-seen.ipr");
    let last_seen = files.directory.join("last-seen.ipr");
    let executable = compile_c_fixture(&files, "abi_workflows.c", &[]);
    let output = run_fixture(
        &executable,
        [&source, &destination, &direct, &first_seen, &last_seen],
    );
    assert!(
        output.status.success(),
        "native C workflow behavior failed\nstdout:\n{}\nstderr:\n{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
}

#[test]
fn native_c_update_ipsets_sdk_surface_uses_the_real_shared_library() {
    let files = TestFiles::new();
    let executable = compile_c_fixture(&files, "abi_sdk.c", &[]);
    let arguments = [
        files.directory.join("sdk-a.ipr"),
        files.directory.join("sdk-b.ipr"),
        files.directory.join("sdk-direct.ipr"),
        files.directory.join("sdk-history.ipr"),
        files.directory.join("sdk-algebra.ipr"),
    ];
    let output = run_fixture(&executable, &arguments);
    assert!(
        output.status.success(),
        "native C SDK behavior failed\nstdout:\n{}\nstderr:\n{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
}

#[test]
fn native_c_lifecycle_and_recovery_use_the_real_shared_library() {
    let files = TestFiles::new();
    let executable = compile_c_fixture(&files, "abi_lifecycle.c", &[]);
    let arguments = [
        files.directory.join("live.ipr"),
        files.directory.join("immutable.ipr"),
        files.directory.join("recovered-immutable.ipr"),
        files.directory.join("recovered-live.ipr"),
        files.directory.join("recovered-offline.ipr"),
        files.directory.join("residue-main.ipr"),
        files.directory.join("missing.ipr"),
    ];
    let output = run_fixture(&executable, &arguments);
    assert!(
        output.status.success(),
        "native C lifecycle behavior failed\nstdout:\n{}\nstderr:\n{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
}

#[test]
fn native_c_maintenance_uses_the_real_shared_library() {
    let files = TestFiles::new();
    let executable = compile_c_fixture(&files, "abi_maintenance.c", &[]);
    let output = run_fixture(&executable, [&files.directory]);
    assert!(
        output.status.success(),
        "native C maintenance behavior failed\nstdout:\n{}\nstderr:\n{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
}

fn compile_c_fixture(files: &TestFiles, source_name: &str, extra_inputs: &[PathBuf]) -> PathBuf {
    let library = shared_library();
    let dependencies = library.parent().unwrap();
    let stem = source_name.strip_suffix(".c").unwrap_or(source_name);
    let executable = files.directory.join(stem);
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
        .args(extra_inputs)
        .arg(&library)
        .arg(format!("-Wl,-rpath,{}", files.directory.display()))
        .arg(format!("-Wl,-rpath,{}", dependencies.display()))
        .arg("-Wl,-z,defs")
        .arg("-o")
        .arg(&executable);
    let output = command.output().unwrap();
    assert!(
        output.status.success(),
        "native C link failed for {source_name}\nstdout:\n{}\nstderr:\n{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
    fs::copy(worker_binary(), files.directory.join("iprange-v4-worker"))
        .expect("install native fixture worker");
    executable
}

fn run_fixture<I, S>(executable: &Path, arguments: I) -> std::process::Output
where
    I: IntoIterator<Item = S>,
    S: AsRef<std::ffi::OsStr>,
{
    let runner = std::env::var_os("IPRANGE_V4_NATIVE_RUNNER");
    let mut command = if let Some(runner) = runner {
        let runner = runner.to_string_lossy();
        let mut words = runner.split_ascii_whitespace();
        let mut command = Command::new(words.next().expect("native runner command"));
        command.args(words).arg(executable);
        command
    } else {
        Command::new(executable)
    };
    command.args(arguments).output().unwrap()
}

fn compile_panic_shim(directory: &Path, dependencies: &Path) -> PathBuf {
    let source = Path::new(env!("CARGO_MANIFEST_DIR")).join("tests/native/panic_shim.rs");
    let output = directory.join("libiprange_v4_native_test.so");
    let rustc = std::env::var_os("RUSTC").unwrap_or_else(|| OsString::from("rustc"));
    let result = Command::new(rustc)
        .args(["--crate-name", "iprange_v4_native_test"])
        .args(["--crate-type", "cdylib"])
        .arg("--edition=2021")
        .arg(source)
        .arg("--extern")
        .arg(format!(
            "iprange_v4={}",
            dependencies.join("libiprange_v4.rlib").display()
        ))
        .arg("-L")
        .arg(format!("dependency={}", dependencies.display()))
        .arg("-o")
        .arg(&output)
        .output()
        .unwrap();
    assert!(
        result.status.success(),
        "panic shim build failed\nstdout:\n{}\nstderr:\n{}",
        String::from_utf8_lossy(&result.stdout),
        String::from_utf8_lossy(&result.stderr)
    );
    output
}

#[test]
fn shared_library_exports_exactly_the_frozen_symbols() {
    let output = Command::new("nm")
        .args(["-D", "--defined-only"])
        .arg(shared_library())
        .output()
        .unwrap();
    assert!(
        output.status.success(),
        "nm failed: {}",
        String::from_utf8_lossy(&output.stderr)
    );
    let actual = String::from_utf8(output.stdout)
        .unwrap()
        .lines()
        .filter_map(|line| line.split_whitespace().last())
        .map(|name| name.split('@').next().unwrap())
        .filter(|name| name.starts_with("iprange_v4_abi1_"))
        .map(str::to_owned)
        .collect::<std::collections::BTreeSet<_>>();
    let manifest: Value = serde_json::from_str(MANIFEST).unwrap();
    let expected = manifest["functions"]
        .as_array()
        .unwrap()
        .iter()
        .map(|function| function["name"].as_str().unwrap().to_owned())
        .collect::<std::collections::BTreeSet<_>>();
    assert_eq!(actual, expected);
    assert_eq!(actual.len(), 158);
}

#[test]
fn native_c_fixtures_reference_every_frozen_function() {
    let sources = NATIVE_SOURCES.join("\n");
    let manifest: Value = serde_json::from_str(MANIFEST).unwrap();
    let functions = manifest["functions"].as_array().unwrap();
    let missing = functions
        .iter()
        .filter_map(|function| {
            let name = function["name"].as_str().unwrap();
            (!sources.contains(name)).then_some(name)
        })
        .collect::<Vec<_>>();
    assert!(missing.is_empty(), "native C fixture gaps: {missing:?}");
    assert_eq!(functions.len(), 158);
}

fn shared_library() -> PathBuf {
    let dependencies = std::env::current_exe()
        .unwrap()
        .parent()
        .unwrap()
        .to_path_buf();
    let library = dependencies.join("libiprange_v4.so");
    assert!(
        library.is_file(),
        "cargo did not build {}",
        library.display()
    );
    library
}

fn worker_binary() -> PathBuf {
    let worker = shared_library()
        .parent()
        .unwrap()
        .parent()
        .unwrap()
        .join("iprange-v4-worker");
    assert!(worker.is_file(), "cargo did not build {}", worker.display());
    worker
}
