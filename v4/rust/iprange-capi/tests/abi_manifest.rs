use std::collections::BTreeSet;
use std::mem::{align_of, size_of, MaybeUninit};
use std::path::{Path, PathBuf};

use serde_json::{json, Value};

const HEADER: &str = include_str!("../include/iprange_v4.h");
const FUNCTION_PREFIX: &str = "IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL";
const CONSTANT_PREFIX: &str = "#define IPRANGE_V4_ABI1_";

macro_rules! layout {
    ($type:ty, $name:literal, $($field:ident),+ $(,)?) => {{
        let uninitialized = MaybeUninit::<$type>::uninit();
        let base = uninitialized.as_ptr();
        let fields = vec![
            $(
                json!({
                    "name": stringify!($field),
                    // SAFETY: addr_of! forms a raw field pointer without reading the
                    // uninitialized value.
                    "offset": unsafe {
                        std::ptr::addr_of!((*base).$field) as usize - base as usize
                    },
                })
            ),+
        ];
        json!({
            "alignment": align_of::<$type>(),
            "fields": fields,
            "name": $name,
            "size": size_of::<$type>(),
        })
    }};
}

include!(concat!(env!("OUT_DIR"), "/abi_layouts.rs"));

#[test]
fn generated_symbols_match_the_frozen_spec() {
    let functions = function_declarations();
    let actual = functions
        .iter()
        .map(|declaration| declaration_name(declaration))
        .collect::<BTreeSet<_>>();
    let expected = frozen_function_names();
    assert_eq!(actual, expected);
    assert_eq!(actual.len(), 136);

    let opaque = opaque_names().into_iter().collect::<BTreeSet<_>>();
    assert_eq!(
        opaque,
        frozen_names_after("opaque tags:", "It declares exact callback")
    );
    assert_eq!(opaque.len(), 12);

    let callbacks = callback_declarations()
        .iter()
        .map(|declaration| callback_name(declaration))
        .collect::<BTreeSet<_>>();
    assert_eq!(
        callbacks,
        frozen_names_after("callback typedefs named:", "The source callbacks")
    );
    assert_eq!(callbacks.len(), 11);
}

#[test]
#[cfg(target_pointer_width = "64")]
fn committed_manifest_matches_the_rust_boundary() {
    assert_eq!(align_of::<u64>(), 8);
    let generated = manifest();
    let path = artifact_directory().join("iprange_v4_abi1_manifest.json");
    if std::env::var_os("IPRANGE_UPDATE_ABI_ARTIFACTS").is_some() {
        std::fs::write(&path, &generated).unwrap();
    }
    let committed = std::fs::read_to_string(&path)
        .unwrap_or_else(|error| panic!("read committed ABI manifest {}: {error}", path.display()));
    assert_eq!(committed, generated);
}

fn manifest() -> String {
    let opaque = opaque_names();
    let functions = function_declarations()
        .into_iter()
        .map(|prototype| {
            let name = declaration_name(&prototype);
            json!({
                "name": name,
                "ownership": function_ownership(&name, &prototype, &opaque),
                "prototype": prototype,
            })
        })
        .collect::<Vec<_>>();
    let callbacks = callback_declarations()
        .into_iter()
        .map(|signature| {
            json!({
                "name": callback_name(&signature),
                "signature": signature,
            })
        })
        .collect::<Vec<_>>();
    let constants = numeric_registry()
        .into_iter()
        .map(|(name, value)| json!({"name": name, "value": value}))
        .collect::<Vec<_>>();

    let value = json!({
        "abi_generation": 1,
        "callbacks": callbacks,
        "canonical_layout_model": {
            "pointer_width_bits": 64,
            "u64_alignment": 8,
        },
        "functions": functions,
        "numeric_registry": constants,
        "opaque_types": opaque,
        "ownership_rules": {
            "borrowed": "caller retains ownership for the call",
            "borrowed_output": "engine retains ownership; lifetime is operation-specific",
            "close_on_success_keep_allocation":
                "engine resource closes; caller still owns the opaque allocation",
            "destroy_on_success": "opaque allocation is released only on status OK",
            "owned_when_nonnull":
                "caller owns every non-null output and must use its matching close/destroy API",
            "transfer_on_success":
                "the source loses the named obligation and the returned handle owns it",
        },
        "schema": "iprange-v4-c-abi-manifest",
        "schema_version": 1,
        "structures": generated_layouts(),
    });
    format!("{}\n", serde_json::to_string_pretty(&value).unwrap())
}

fn function_ownership(name: &str, prototype: &str, opaque: &[String]) -> Vec<Value> {
    let arguments = prototype
        .split_once('(')
        .and_then(|(_, tail)| tail.rsplit_once(')'))
        .map(|(arguments, _)| arguments)
        .unwrap();
    let destroys = name.ends_with("_destroy");
    let closes = name.ends_with("_close");
    let transfers = name.ends_with("_take_cleanup_guard") || name.ends_with("_take_residue");
    let mut first_input = true;
    let mut ownership = Vec::new();

    for argument in arguments.split(',').map(str::trim) {
        let Some(handle) = opaque
            .iter()
            .find(|handle| argument.contains(handle.as_str()))
        else {
            continue;
        };
        let parameter = argument
            .split_whitespace()
            .last()
            .unwrap()
            .trim_start_matches('*');
        let double_pointer = argument.contains("**");
        let immutable = argument.starts_with("const ");
        let rule = if double_pointer && immutable {
            "borrowed_output"
        } else if double_pointer {
            "owned_when_nonnull"
        } else if first_input && destroys {
            "destroy_on_success"
        } else if first_input && closes {
            "close_on_success_keep_allocation"
        } else if first_input && transfers {
            "transfer_on_success"
        } else {
            "borrowed"
        };
        ownership.push(json!({
            "handle_type": handle,
            "parameter": parameter,
            "rule": rule,
        }));
        if !double_pointer {
            first_input = false;
        }
    }
    ownership
}

fn function_declarations() -> Vec<String> {
    declarations(|line| line == FUNCTION_PREFIX)
}

fn callback_declarations() -> Vec<String> {
    declarations(|line| line.starts_with("typedef ") && line.contains("(*iprange_v4_abi1_"))
}

fn declarations(mut begins: impl FnMut(&str) -> bool) -> Vec<String> {
    let mut declarations = Vec::new();
    let mut current = None::<String>;
    for line in HEADER.lines().map(str::trim) {
        if current.is_none() && begins(line) {
            if line != FUNCTION_PREFIX && line.ends_with(';') {
                declarations.push(line.split_whitespace().collect::<Vec<_>>().join(" "));
            } else {
                current = Some(String::new());
                if line != FUNCTION_PREFIX {
                    current.as_mut().unwrap().push_str(line);
                }
            }
            continue;
        }
        let Some(declaration) = current.as_mut() else {
            continue;
        };
        if !declaration.is_empty() {
            declaration.push(' ');
        }
        declaration.push_str(line);
        if line.ends_with(';') {
            declarations.push(declaration.split_whitespace().collect::<Vec<_>>().join(" "));
            current = None;
        }
    }
    declarations.sort();
    declarations
}

fn numeric_registry() -> Vec<(String, u32)> {
    let mut constants = HEADER
        .lines()
        .filter_map(|line| {
            let suffix = line.strip_prefix(CONSTANT_PREFIX)?;
            let (name, value) = suffix.split_once(' ')?;
            Some((format!("IPRANGE_V4_ABI1_{name}"), value.parse().unwrap()))
        })
        .collect::<Vec<_>>();
    constants.sort();
    constants
}

fn opaque_names() -> Vec<String> {
    let mut names = HEADER
        .lines()
        .filter_map(|line| {
            let line = line.trim();
            let name = line.strip_prefix("typedef struct iprange_v4_abi1_")?;
            let (name, alias) = name.split_once(' ')?;
            let alias = alias.strip_prefix("iprange_v4_abi1_")?.strip_suffix(';')?;
            (name == alias).then(|| format!("iprange_v4_abi1_{name}"))
        })
        .collect::<Vec<_>>();
    names.sort();
    names
}

fn declaration_name(declaration: &str) -> String {
    let start = declaration.find("iprange_v4_abi1_").unwrap();
    let tail = &declaration[start..];
    tail[..tail.find('(').unwrap()].to_owned()
}

fn callback_name(declaration: &str) -> String {
    let start = declaration.find("(*iprange_v4_abi1_").unwrap() + 2;
    let tail = &declaration[start..];
    tail[..tail.find(')').unwrap()].to_owned()
}

fn frozen_function_names() -> BTreeSet<String> {
    frozen_names_after(
        "## Frozen generation-1 symbol manifest",
        "The exact prototype/layout manifest remains",
    )
}

fn frozen_names_after(start: &str, end: &str) -> BTreeSet<String> {
    let spec = std::fs::read_to_string(spec_path()).unwrap();
    let body = spec.split_once(start).unwrap().1.split_once(end).unwrap().0;
    body.split(|character: char| !character.is_ascii_alphanumeric() && character != '_')
        .filter(|word| word.starts_with("iprange_v4_abi1_"))
        .filter(|word| *word != "iprange_v4_abi1_")
        .map(str::to_owned)
        .collect()
}

fn artifact_directory() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("include")
}

fn spec_path() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../../../.agents/sow/specs/c-abi-v4.md")
}
