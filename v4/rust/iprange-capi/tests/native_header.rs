#![cfg(unix)]

use std::ffi::OsString;
use std::fs;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::time::{SystemTime, UNIX_EPOCH};

use serde_json::Value;

const MANIFEST: &str = include_str!("../include/iprange_v4_abi1_manifest.json");

struct TemporaryDirectory(PathBuf);

impl TemporaryDirectory {
    fn new() -> Self {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let path = std::env::temp_dir().join(format!(
            "iprange-v4-native-header-{}-{unique}",
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
fn generated_header_compiles_as_c11_and_cpp17_with_all_layout_assertions() {
    let manifest: Value = serde_json::from_str(MANIFEST).unwrap();
    let temporary = TemporaryDirectory::new();
    let c_source = temporary.0.join("layout.c");
    let cpp_source = temporary.0.join("layout.cpp");
    let c_object = temporary.0.join("layout.o");
    let cpp_object = temporary.0.join("layout-cpp.o");
    fs::write(&c_source, assertions(&manifest, false)).unwrap();
    fs::write(&cpp_source, assertions(&manifest, true)).unwrap();

    compile(
        "CC",
        "cc",
        &[
            "-std=c11",
            "-Wall",
            "-Wextra",
            "-Werror",
            "-c",
            path(&c_source),
            "-o",
            path(&c_object),
        ],
    );
    compile(
        "CXX",
        "c++",
        &[
            "-std=c++17",
            "-Wall",
            "-Wextra",
            "-Werror",
            "-c",
            path(&cpp_source),
            "-o",
            path(&cpp_object),
        ],
    );
}

fn assertions(manifest: &Value, cpp: bool) -> String {
    let assert = if cpp {
        "static_assert"
    } else {
        "_Static_assert"
    };
    let align = if cpp { "alignof" } else { "_Alignof" };
    let mut source = String::from("#include <stddef.h>\n#include \"iprange_v4.h\"\n\n");

    for structure in manifest["structures"].as_array().unwrap() {
        let name = structure["name"].as_str().unwrap();
        let size = structure["size"].as_u64().unwrap();
        let alignment = structure["alignment"].as_u64().unwrap();
        source.push_str(&format!(
            "{assert}(sizeof({name}) == {size}, \"{name} size\");\n"
        ));
        source.push_str(&format!(
            "{assert}({align}({name}) == {alignment}, \"{name} alignment\");\n"
        ));
        for field in structure["fields"].as_array().unwrap() {
            let field_name = field["name"].as_str().unwrap();
            let offset = field["offset"].as_u64().unwrap();
            source.push_str(&format!(
                "{assert}(offsetof({name}, {field_name}) == {offset}, \
                 \"{name}.{field_name} offset\");\n"
            ));
        }
    }
    for constant in manifest["numeric_registry"].as_array().unwrap() {
        let name = constant["name"].as_str().unwrap();
        let value = constant["value"].as_u64().unwrap();
        source.push_str(&format!(
            "{assert}({name} == {value}u, \"{name} value\");\n"
        ));
    }
    source
}

fn compile(environment: &str, fallback: &str, arguments: &[&str]) {
    let configured = std::env::var_os(environment).unwrap_or_else(|| OsString::from(fallback));
    let configured = configured.to_string_lossy();
    let mut words = configured.split_ascii_whitespace();
    let mut command = Command::new(words.next().expect("compiler command"));
    command.args(words);
    command
        .arg("-I")
        .arg(Path::new(env!("CARGO_MANIFEST_DIR")).join("include"))
        .args(arguments);
    let output = command.output().unwrap();
    assert!(
        output.status.success(),
        "native header compilation failed\nstdout:\n{}\nstderr:\n{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
}

fn path(path: &Path) -> &str {
    path.to_str().unwrap()
}
