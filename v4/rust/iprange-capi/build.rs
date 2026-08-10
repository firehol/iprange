use std::env;
use std::fmt::Write as _;
use std::fs;
use std::path::{Path, PathBuf};

fn main() {
    let crate_dir = PathBuf::from(env::var_os("CARGO_MANIFEST_DIR").expect("manifest directory"));
    let output_dir = PathBuf::from(env::var_os("OUT_DIR").expect("build output directory"));

    println!("cargo:rerun-if-changed=cbindgen.toml");
    println!("cargo:rerun-if-changed=src");

    let config =
        cbindgen::Config::from_file(crate_dir.join("cbindgen.toml")).expect("read cbindgen config");
    let header = generate_header(&crate_dir, config.clone());
    fs::write(output_dir.join("iprange_v4.h"), header).expect("write generated C header");
    let layouts = generate_layout_source(&crate_dir, &config);
    fs::write(output_dir.join("abi_layouts.rs"), layouts).expect("write ABI layout source");
}

fn generate_header(crate_dir: &Path, config: cbindgen::Config) -> Vec<u8> {
    // Source parsing avoids Cargo metadata loading target-only packages newer than our MSRV.
    let bindings = cbindgen::Builder::new()
        .with_src(crate_dir.join("src/lib.rs"))
        .with_config(config)
        .generate()
        .expect("generate C bindings");
    let mut generated = Vec::new();
    bindings.write(&mut generated);

    let text = String::from_utf8(generated).expect("cbindgen emitted UTF-8");
    normalize_registry_defines(&text).into_bytes()
}

fn normalize_registry_defines(header: &str) -> String {
    let mut normalized = String::with_capacity(header.len());
    for line in header.lines() {
        if let Some(suffix) = line.strip_prefix("#define iprange_v4_abi1_") {
            if suffix
                .as_bytes()
                .first()
                .is_some_and(u8::is_ascii_uppercase)
            {
                normalized.push_str("#define IPRANGE_V4_ABI1_");
                normalized.push_str(suffix);
                normalized.push('\n');
                continue;
            }
        }
        normalized.push_str(line);
        normalized.push('\n');
    }
    normalized
}

fn generate_layout_source(crate_dir: &Path, config: &cbindgen::Config) -> String {
    let prefix = config.export.prefix.as_deref().unwrap_or_default();
    let mut layouts = Vec::new();

    for relative in ["src/abi.rs", "src/abi_extra.rs", "src/abi_sdk.rs"] {
        let source = fs::read_to_string(crate_dir.join(relative)).expect("read ABI layout source");
        let parsed = syn::parse_file(&source).expect("parse ABI layout source");
        for item in parsed.items {
            let syn::Item::Struct(item) = item else {
                continue;
            };
            if !matches!(item.vis, syn::Visibility::Public(_)) || !has_c_repr(&item.attrs) {
                continue;
            }
            let syn::Fields::Named(fields) = item.fields else {
                panic!("public C ABI structures must have named fields");
            };
            let rust_name = item.ident.to_string();
            let c_base = config
                .export
                .rename
                .get(&rust_name)
                .cloned()
                .unwrap_or_else(|| snake_case(&rust_name));
            let field_names = fields
                .named
                .iter()
                .map(|field| field.ident.as_ref().expect("named field").to_string())
                .collect::<Vec<_>>();
            layouts.push((format!("{prefix}{c_base}"), rust_name, field_names));
        }
    }
    layouts.sort_by(|left, right| left.0.cmp(&right.0));

    let mut generated = String::from(
        "// Generated from the public #[repr(C)] Rust structures. Do not edit.\n\
         fn generated_layouts() -> Vec<Value> {\n    vec![\n",
    );
    for (c_name, rust_name, fields) in layouts {
        write!(
            generated,
            "        layout!(iprange_v4::{rust_name}, \"{c_name}\""
        )
        .expect("write layout source");
        for field in fields {
            write!(generated, ", {field}").expect("write layout field");
        }
        generated.push_str("),\n");
    }
    generated.push_str("    ]\n}\n");
    generated
}

fn has_c_repr(attributes: &[syn::Attribute]) -> bool {
    attributes.iter().any(|attribute| {
        if !attribute.path().is_ident("repr") {
            return false;
        }
        let mut found = false;
        attribute
            .parse_nested_meta(|meta| {
                if meta.path.is_ident("C") {
                    found = true;
                }
                Ok(())
            })
            .expect("parse repr attribute");
        found
    })
}

fn snake_case(name: &str) -> String {
    let mut output = String::with_capacity(name.len() + 4);
    for (index, character) in name.char_indices() {
        if character.is_ascii_uppercase() && index != 0 {
            output.push('_');
        }
        output.push(character.to_ascii_lowercase());
    }
    output
}
