//! Language-neutral **behavioral** conformance (§12): the shared op-sequences in
//! `v4/conformance/cases.json` are run through the writer + reader, and the resulting
//! scan / point-lookup results must match the expected values. The Go port runs the
//! exact same file — both implementations must agree.
//!
//! Keys are decimal strings (a `u32` value for v4, a `u128` value for v6) so the corpus
//! is JSON-number-precision-safe and trivially parsed in either language. Byte-level
//! cross-read goldens (one impl reading the other's file) are added alongside the Go
//! port.

use std::path::Path;

use iprange_livedb::{Ipv4Key, Ipv6Key, Reader, Writer};
use serde::Deserialize;

#[derive(Deserialize)]
struct Case {
    name: String,
    family: String,
    scope_width: u8,
    ops: Vec<Op>,
    expect_scan: Vec<(String, String, Vec<u8>)>,
    #[serde(default)]
    expect_lookup: Vec<(String, Option<Vec<u8>>)>,
}

#[derive(Deserialize)]
struct Op {
    op: String,
    from: String,
    to: String,
    #[serde(default)]
    scope: Vec<u8>,
}

fn corpus() -> String {
    let p = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../conformance/cases.json");
    std::fs::read_to_string(&p).unwrap_or_else(|e| panic!("read {}: {e}", p.display()))
}

fn golden_path(name: &str) -> std::path::PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../conformance/files")
        .join(format!("{name}.iprdb"))
}

/// Write the committed image as a cross-read golden when `REGENERATE_GOLDENS` is set;
/// otherwise (when the golden exists) it is read back and verified by the caller. The
/// Go port reads these same files (cross-read, §12).
fn maybe_write_golden(name: &str, img: &[u8]) {
    if std::env::var_os("REGENERATE_GOLDENS").is_some() {
        let p = golden_path(name);
        std::fs::create_dir_all(p.parent().unwrap()).unwrap();
        std::fs::write(&p, img).unwrap();
    }
}

#[test]
fn behavioral_conformance() {
    let cases: Vec<Case> = serde_json::from_str(&corpus()).expect("parse cases.json");
    assert!(!cases.is_empty(), "corpus is empty");
    for c in &cases {
        match c.family.as_str() {
            "v4" => run_v4(c),
            "v6" => run_v6(c),
            other => panic!("case {}: unknown family {other}", c.name),
        }
    }
}

fn run_v4(c: &Case) {
    let mut w = Writer::<Ipv4Key>::create(c.scope_width, 0);
    for op in &c.ops {
        let from = Ipv4Key(op.from.parse().unwrap());
        let to = Ipv4Key(op.to.parse().unwrap());
        match op.op.as_str() {
            "set" => w.set(from, to, &op.scope).unwrap(),
            "delete" => w.delete(from, to).unwrap(),
            o => panic!("case {}: bad op {o}", c.name),
        }
        // Commit per op: realistic usage, and reclaims this txn's COW garbage (D7) so
        // the committed golden stays compact instead of accumulating one page per set.
        w.commit(0).unwrap();
    }
    if c.ops.is_empty() {
        w.commit(0).unwrap();
    }
    let img = w.into_image();
    let r = Reader::open(&img).unwrap();

    let mut got = Vec::new();
    r.scan_v4(|f, t, s| got.push((f.0.to_string(), t.0.to_string(), s.to_vec())))
        .unwrap();
    assert_eq!(got, c.expect_scan, "scan mismatch: {}", c.name);

    for (ip, want) in &c.expect_lookup {
        let g = r
            .lookup_v4(Ipv4Key(ip.parse().unwrap()))
            .unwrap()
            .map(<[u8]>::to_vec);
        assert_eq!(&g, want, "lookup {ip} mismatch: {}", c.name);
    }

    maybe_write_golden(&c.name, &img);
    if let Ok(bytes) = std::fs::read(golden_path(&c.name)) {
        let gr = Reader::open(&bytes).unwrap();
        let mut ggot = Vec::new();
        gr.scan_v4(|f, t, s| ggot.push((f.0.to_string(), t.0.to_string(), s.to_vec())))
            .unwrap();
        assert_eq!(
            ggot, c.expect_scan,
            "golden cross-read mismatch: {}",
            c.name
        );
    }
}

fn run_v6(c: &Case) {
    let mut w = Writer::<Ipv6Key>::create(c.scope_width, 0);
    for op in &c.ops {
        let from = Ipv6Key::from_u128(op.from.parse().unwrap());
        let to = Ipv6Key::from_u128(op.to.parse().unwrap());
        match op.op.as_str() {
            "set" => w.set(from, to, &op.scope).unwrap(),
            "delete" => w.delete(from, to).unwrap(),
            o => panic!("case {}: bad op {o}", c.name),
        }
        // Commit per op: realistic usage, and reclaims this txn's COW garbage (D7) so
        // the committed golden stays compact instead of accumulating one page per set.
        w.commit(0).unwrap();
    }
    if c.ops.is_empty() {
        w.commit(0).unwrap();
    }
    let img = w.into_image();
    let r = Reader::open(&img).unwrap();

    let mut got = Vec::new();
    r.scan_v6(|f, t, s| got.push((f.to_u128().to_string(), t.to_u128().to_string(), s.to_vec())))
        .unwrap();
    assert_eq!(got, c.expect_scan, "scan mismatch: {}", c.name);

    for (ip, want) in &c.expect_lookup {
        let g = r
            .lookup_v6(Ipv6Key::from_u128(ip.parse().unwrap()))
            .unwrap()
            .map(<[u8]>::to_vec);
        assert_eq!(&g, want, "lookup {ip} mismatch: {}", c.name);
    }

    maybe_write_golden(&c.name, &img);
    if let Ok(bytes) = std::fs::read(golden_path(&c.name)) {
        let gr = Reader::open(&bytes).unwrap();
        let mut ggot = Vec::new();
        gr.scan_v6(|f, t, s| {
            ggot.push((f.to_u128().to_string(), t.to_u128().to_string(), s.to_vec()))
        })
        .unwrap();
        assert_eq!(
            ggot, c.expect_scan,
            "golden cross-read mismatch: {}",
            c.name
        );
    }
}
