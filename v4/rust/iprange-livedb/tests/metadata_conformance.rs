#![cfg(disabled)] // TODO: re-enable when scope/KV APIs are re-implemented (SOW-0014)
//! Bidirectional **metadata** cross-read conformance (§C, §12): a metadata-bearing file
//! written by EITHER language is read identically by BOTH. Unlike the IP-tree
//! `behavioral_conformance` (Rust writes the only golden, both read), each writer's KV /
//! scope-table encoding is independent, so the goldens are two-sided:
//!
//! - Rust writes `v4/conformance/files/<name>.r.iprdb`;
//! - Go writes `v4/conformance/files/<name>.go.iprdb`;
//! - each reader opens BOTH and verifies the same expectations.
//!
//! All reads go through the **Reader** API (the consumer path being certified), never the
//! Writer. Cases come from `v4/conformance/metadata_cases.json`, plus one programmatic
//! 25-scope case (`meta_many`) that overflows the 14-record scope-leaf capacity and so
//! forces a multi-level scope tree — proving branchy-scope-table cross-read.

use std::path::{Path, PathBuf};

use iprange_livedb::key::IpKey;
use iprange_livedb::spec::{self, FILE_SCOPE_ID};
use iprange_livedb::{Ipv4Key, Ipv6Key, MetaEntry, Reader, Writer};
use serde::Deserialize;

// --- corpus schema (mirrors metadata_cases.json) ---

#[derive(Deserialize)]
struct Case {
    name: String,
    family: String,
    scope_width: u8,
    #[serde(default)]
    ip_ops: Vec<IpOp>,
    #[serde(default)]
    scopes: Vec<ScopeDef>,
    #[serde(default)]
    file_kv: Vec<Kv>,
    #[serde(default)]
    expect_scopes: Vec<ExpectScope>,
    #[serde(default)]
    expect_file_kv: Vec<Kv>,
}

#[derive(Deserialize)]
struct IpOp {
    op: String,
    from: String,
    to: String,
    #[serde(default)]
    scope: Vec<u8>,
}

#[derive(Deserialize)]
struct ScopeDef {
    name: String,
    set_version: Option<u64>,
    set_type: Option<u8>,
    #[serde(default)]
    kv: Vec<Kv>,
}

#[derive(Deserialize)]
struct ExpectScope {
    id: u32,
    name: String,
    version: u64,
    #[serde(rename = "type")]
    type_: u8,
    #[serde(default)]
    kv: Vec<Kv>,
}

/// One KV entry in the corpus. The value is exactly one of `value_hex` or `value_fill`.
#[derive(Deserialize)]
struct Kv {
    key: String,
    #[serde(rename = "type")]
    type_: u32,
    value_hex: Option<String>,
    value_fill: Option<Fill>,
}

#[derive(Deserialize)]
struct Fill {
    byte: u8,
    len: u32,
}

impl Kv {
    /// A text KV (`type 0`) with a hex value — for the programmatic case.
    fn text(key: &str, value: &[u8]) -> Kv {
        Kv {
            key: key.to_string(),
            type_: 0,
            value_hex: Some(hex(value)),
            value_fill: None,
        }
    }

    /// Materialize the value bytes from `value_hex` XOR `value_fill`.
    fn value(&self) -> Vec<u8> {
        match (&self.value_hex, &self.value_fill) {
            (Some(h), None) => decode_hex(h),
            (None, Some(f)) => vec![f.byte; f.len as usize],
            _ => panic!(
                "kv {}: exactly one of value_hex/value_fill required",
                self.key
            ),
        }
    }
}

fn hex(bytes: &[u8]) -> String {
    use std::fmt::Write;
    bytes.iter().fold(String::new(), |mut s, b| {
        let _ = write!(s, "{b:02x}");
        s
    })
}

fn decode_hex(s: &str) -> Vec<u8> {
    assert!(s.len() % 2 == 0, "odd-length hex {s:?}");
    (0..s.len())
        .step_by(2)
        .map(|i| u8::from_str_radix(&s[i..i + 2], 16).expect("hex digit"))
        .collect()
}

// --- corpus + golden paths ---

fn corpus() -> String {
    let p = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../conformance/metadata_cases.json");
    std::fs::read_to_string(&p).unwrap_or_else(|e| panic!("read {}: {e}", p.display()))
}

fn files_dir() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../../conformance/files")
}

/// This language's golden (Rust writes `.r.iprdb`).
fn rust_golden(name: &str) -> PathBuf {
    files_dir().join(format!("{name}.r.iprdb"))
}

/// The other language's golden (Go writes `.go.iprdb`).
fn go_golden(name: &str) -> PathBuf {
    files_dir().join(format!("{name}.go.iprdb"))
}

/// Write the committed image as this language's golden when `REGENERATE_GOLDENS` is set
/// (mirrors the IP-tree harness's `maybe_write_golden`). Read-back is done by the caller.
fn maybe_write_golden(name: &str, img: &[u8]) {
    if std::env::var_os("REGENERATE_GOLDENS").is_some() {
        let p = rust_golden(name);
        std::fs::create_dir_all(p.parent().unwrap()).unwrap();
        std::fs::write(&p, img).unwrap();
    }
}

// --- expectations the Reader API must satisfy on any committed image ---

/// Verify a committed image **through the Reader** against the case's expectations.
/// `where_` labels which file (built / Go golden) for a clear failure message.
fn verify_reader(img: &[u8], c: &Case, where_: &str) {
    let r =
        Reader::open(img).unwrap_or_else(|e| panic!("{} {}: Reader::open: {e:?}", c.name, where_));

    // scope_list excludes FILE(0); compare (id, name) in ascending id order.
    let got_list = r.scope_list();
    let want_list: Vec<(u32, Vec<u8>)> = c
        .expect_scopes
        .iter()
        .map(|s| (s.id, s.name.as_bytes().to_vec()))
        .collect();
    assert_eq!(got_list, want_list, "{} {}: scope_list", c.name, where_);

    // Per-scope header fields + KV.
    for es in &c.expect_scopes {
        assert_eq!(
            r.scope_name(es.id),
            Some(es.name.as_bytes().to_vec()),
            "{} {}: scope_name({})",
            c.name,
            where_,
            es.id
        );
        assert_eq!(
            r.scope_version(es.id),
            Some(es.version),
            "{} {}: scope_version({})",
            c.name,
            where_,
            es.id
        );
        assert_eq!(
            r.scope_type(es.id),
            Some(es.type_),
            "{} {}: scope_type({})",
            c.name,
            where_,
            es.id
        );
        verify_kv(&r, es.id, &es.kv, c, where_);
    }

    // FILE(0) target KV, plus FILE is never a "defined scope".
    verify_kv(&r, FILE_SCOPE_ID, &c.expect_file_kv, c, where_);
    assert_eq!(
        r.scope_name(FILE_SCOPE_ID),
        None,
        "{} {}: FILE(0) must not be a defined scope",
        c.name,
        where_
    );
}

/// Verify `meta_list(target)` equals the expected sorted KV (byte-for-byte values,
/// including overflow-spanning ones), and spot-check `meta_get` for a present + absent key.
fn verify_kv(r: &Reader, target: u32, want: &[Kv], c: &Case, where_: &str) {
    let got: Vec<MetaEntry> = r
        .meta_list(target)
        .unwrap_or_else(|e| panic!("{} {}: meta_list({target}): {e:?}", c.name, where_));
    let want_entries: Vec<MetaEntry> = want
        .iter()
        .map(|k| (k.key.as_bytes().to_vec(), k.type_, k.value()))
        .collect();
    assert_eq!(
        got, want_entries,
        "{} {}: meta_list({target})",
        c.name, where_
    );

    if let Some(first) = want.first() {
        // Present key: meta_get returns (type, value) byte-for-byte.
        assert_eq!(
            r.meta_get(target, first.key.as_bytes()).unwrap(),
            Some((first.type_, first.value())),
            "{} {}: meta_get({target}, {:?})",
            c.name,
            where_,
            first.key
        );
    }
    // Absent key: never present in any case (no corpus key is "__absent__").
    assert_eq!(
        r.meta_get(target, b"__absent__").unwrap(),
        None,
        "{} {}: meta_get({target}, absent) must be None",
        c.name,
        where_
    );
}

// --- build a case image via the Writer (one body, generic over the key family) ---

/// Build a committed image for `c` using key family `K`: define scopes in order (id =
/// position+1), apply version/type/kv, write FILE(0) kv, apply ip_ops, commit.
fn build<K: IpKey>(c: &Case, parse: impl Fn(&str) -> K) -> Vec<u8> {
    let mut w = Writer::<K>::create(c.scope_width, 0);
    for s in &c.scopes {
        let id = w.scope_define(s.name.as_bytes()).unwrap();
        if let Some(v) = s.set_version {
            w.scope_set_version(id, v).unwrap();
        }
        if let Some(t) = s.set_type {
            w.scope_set_type(id, t).unwrap();
        }
        for kv in &s.kv {
            w.meta_set(id, kv.key.as_bytes(), kv.type_, &kv.value())
                .unwrap();
        }
    }
    for kv in &c.file_kv {
        w.meta_set(FILE_SCOPE_ID, kv.key.as_bytes(), kv.type_, &kv.value())
            .unwrap();
    }
    for op in &c.ip_ops {
        let (from, to) = (parse(&op.from), parse(&op.to));
        match op.op.as_str() {
            "set" => w.set(from, to, &op.scope).unwrap(),
            "delete" => w.delete(from, to).unwrap(),
            o => panic!("case {}: bad ip op {o}", c.name),
        }
    }
    w.commit(1, u64::MAX).unwrap();
    w.into_image()
}

#[test]
fn metadata_conformance() {
    let cases: Vec<Case> = serde_json::from_str(&corpus()).expect("parse metadata_cases.json");
    assert!(!cases.is_empty(), "metadata corpus is empty");
    for c in &cases {
        let img = match c.family.as_str() {
            "v4" => build::<Ipv4Key>(c, |s| Ipv4Key(s.parse().unwrap())),
            "v6" => build::<Ipv6Key>(c, |s| Ipv6Key::from_u128(s.parse().unwrap())),
            other => panic!("case {}: unknown family {other}", c.name),
        };

        // 1) verify the freshly built image through the Reader.
        verify_reader(&img, c, "built");
        // 2) write this language's golden (Rust → .r.iprdb) when regenerating.
        maybe_write_golden(&c.name, &img);
        // 3) cross-read: if the OTHER language's golden exists, Rust reads it and verifies
        // the SAME expectations (clean bootstrap ⇒ missing golden ⇒ skip).
        if let Ok(bytes) = std::fs::read(go_golden(&c.name)) {
            verify_reader(&bytes, c, "go-golden (Rust reads Go)");
        }
    }
}

// --- programmatic 25-scope case (not in JSON; built identically in Go) ---

/// 25 scopes overflow the 14-record scope-leaf capacity, forcing a multi-level scope tree.
/// The expectations are derived from the SAME deterministic loop the Go side uses.
fn meta_many_case() -> Case {
    let expect_scopes: Vec<ExpectScope> = (1u32..=25)
        .map(|i| ExpectScope {
            id: i,
            name: format!("scope-{i}"),
            version: (i as u64) * 10,
            type_: (i % 3) as u8,
            kv: vec![Kv::text(&format!("k{i}"), format!("v{i}").as_bytes())],
        })
        .collect();
    Case {
        name: "meta_many".into(),
        family: "v4".into(),
        scope_width: 0,
        ip_ops: vec![],
        // Build inputs mirror the expectations (no separate set_version/type fields needed
        // since the Writer setters take the same values).
        scopes: (1u32..=25)
            .map(|i| ScopeDef {
                name: format!("scope-{i}"),
                set_version: Some((i as u64) * 10),
                set_type: Some((i % 3) as u8),
                kv: vec![Kv::text(&format!("k{i}"), format!("v{i}").as_bytes())],
            })
            .collect(),
        file_kv: vec![Kv::text("root", b"top")],
        expect_scopes,
        expect_file_kv: vec![Kv::text("root", b"top")],
    }
}

#[test]
fn metadata_many_scopes_multilevel() {
    // 25 > scope-leaf capacity (14) ⇒ a branchy scope table.
    assert!(25 > spec::scope_leaf_max(), "expected scope-leaf cap < 25");

    let c = meta_many_case();
    let img = build::<Ipv4Key>(&c, |s| Ipv4Key(s.parse().unwrap()));

    verify_reader(&img, &c, "built");
    maybe_write_golden(&c.name, &img);
    if let Ok(bytes) = std::fs::read(go_golden(&c.name)) {
        verify_reader(&bytes, &c, "go-golden (Rust reads Go)");
    }
}
