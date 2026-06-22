//! Conformance harness: drive the language-neutral corpus in `conformance/`.
//!
//! For every `cases/<name>.json`, build the file with the writer and either compare
//! to the committed `golden/<name>.iprbin` (byte-identical) or assert the expected
//! rejection. `bytes` goldens are additionally round-tripped through the reader.
//!
//! Regenerate goldens on purpose: `REGENERATE_GOLDENS=1 cargo test --test conformance`.
//! A normal run is the cross-language byte-identity guard — it fails on any drift.

use std::fs;
use std::net::{Ipv4Addr, Ipv6Addr};
use std::path::{Path, PathBuf};

use iprange_format::error::Error;
use iprange_format::{FeedMeta, Ipv4Key, Ipv6Key, MergeWriter, Reader, Value, Writer};
use serde::Deserialize;

#[derive(Deserialize)]
struct Case {
    name: String,
    #[serde(default)]
    #[allow(dead_code)]
    description: String,
    ip_version: String,
    #[serde(default)]
    feed_meta: Meta,
    #[serde(default)]
    license_flags: u32,
    generation_unixtime: u64,
    #[serde(default)]
    ranges: Vec<RangeSpec>,
    /// Multi-feed merge input (v3.1). If non-empty, the case is built via `MergeWriter`.
    #[serde(default)]
    feeds: Vec<FeedSpec>,
    expect: String,
    #[serde(default)]
    reject_class: Option<String>,
}

#[derive(Deserialize, Default)]
struct FeedSpec {
    feed_id: u32,
    #[serde(default)]
    feed_meta: Meta,
    #[serde(default)]
    ranges: Vec<RangeSpec>,
}

#[derive(Deserialize, Default)]
struct Meta {
    #[serde(default)]
    name: String,
    #[serde(default)]
    category: String,
    #[serde(default)]
    maintainer: String,
    #[serde(default)]
    maintainer_url: String,
    #[serde(default)]
    source_url: String,
    #[serde(default)]
    license: String,
}

#[derive(Deserialize)]
struct RangeSpec {
    start: String,
    end: String,
    value: Option<ValueSpec>,
}

#[derive(Deserialize)]
struct ValueSpec {
    type_id: u32,
    #[serde(default)]
    bytes_hex: String,
}

fn corpus_dir() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../../conformance")
}

fn from_hex(s: &str) -> Vec<u8> {
    assert!(s.len() % 2 == 0, "hex string must have even length");
    (0..s.len())
        .step_by(2)
        .map(|i| u8::from_str_radix(&s[i..i + 2], 16).expect("valid hex"))
        .collect()
}

fn feed_meta(m: &Meta) -> FeedMeta {
    FeedMeta {
        name: m.name.clone(),
        category: m.category.clone(),
        maintainer: m.maintainer.clone(),
        maintainer_url: m.maintainer_url.clone(),
        source_url: m.source_url.clone(),
        license: m.license.clone(),
    }
}

fn value_of(r: &RangeSpec) -> Option<Value> {
    r.value.as_ref().map(|v| Value {
        type_id: v.type_id,
        bytes: from_hex(&v.bytes_hex),
    })
}

fn v4(s: &str) -> Ipv4Key {
    Ipv4Key(u32::from(s.parse::<Ipv4Addr>().expect("v4 addr")))
}
fn v6(s: &str) -> Ipv6Key {
    Ipv6Key::from_u128(u128::from(s.parse::<Ipv6Addr>().expect("v6 addr")))
}

fn build_v4(case: &Case) -> Result<Vec<u8>, Error> {
    if !case.feeds.is_empty() {
        let mut m = MergeWriter::<Ipv4Key>::new(
            feed_meta(&case.feed_meta),
            case.license_flags,
            case.generation_unixtime,
        );
        for f in &case.feeds {
            let ranges = f
                .ranges
                .iter()
                .map(|r| (v4(&r.start), v4(&r.end)))
                .collect();
            m.add_feed(f.feed_id, feed_meta(&f.feed_meta), ranges)?;
        }
        return m.build();
    }
    let mut w = Writer::<Ipv4Key>::new(
        feed_meta(&case.feed_meta),
        case.license_flags,
        case.generation_unixtime,
    );
    for r in &case.ranges {
        w.add_range(v4(&r.start), v4(&r.end), value_of(r))?;
    }
    w.build()
}

fn build_v6(case: &Case) -> Result<Vec<u8>, Error> {
    if !case.feeds.is_empty() {
        let mut m = MergeWriter::<Ipv6Key>::new(
            feed_meta(&case.feed_meta),
            case.license_flags,
            case.generation_unixtime,
        );
        for f in &case.feeds {
            let ranges = f
                .ranges
                .iter()
                .map(|r| (v6(&r.start), v6(&r.end)))
                .collect();
            m.add_feed(f.feed_id, feed_meta(&f.feed_meta), ranges)?;
        }
        return m.build();
    }
    let mut w = Writer::<Ipv6Key>::new(
        feed_meta(&case.feed_meta),
        case.license_flags,
        case.generation_unixtime,
    );
    for r in &case.ranges {
        w.add_range(v6(&r.start), v6(&r.end), value_of(r))?;
    }
    w.build()
}

fn build(case: &Case) -> Result<Vec<u8>, Error> {
    match case.ip_version.as_str() {
        "v4" => build_v4(case),
        "v6" => build_v6(case),
        other => panic!("unknown ip_version {other:?}"),
    }
}

fn error_class(e: &Error) -> String {
    let s = format!("{e:?}");
    s.split(['(', ' ', '{']).next().unwrap_or("").to_string()
}

#[test]
fn conformance_corpus() {
    let dir = corpus_dir();
    let cases_dir = dir.join("cases");
    let golden_dir = dir.join("golden");
    let regenerate = std::env::var_os("REGENERATE_GOLDENS").is_some();

    let mut entries: Vec<PathBuf> = fs::read_dir(&cases_dir)
        .unwrap_or_else(|e| panic!("read {cases_dir:?}: {e}"))
        .map(|e| e.unwrap().path())
        .filter(|p| p.extension().is_some_and(|x| x == "json"))
        .collect();
    entries.sort();
    assert!(
        !entries.is_empty(),
        "no conformance cases found in {cases_dir:?}"
    );

    let mut checked = 0usize;
    for path in &entries {
        let text = fs::read_to_string(path).unwrap();
        let case: Case = serde_json::from_str(&text).unwrap_or_else(|e| panic!("{path:?}: {e}"));
        let result = build(&case);

        match case.expect.as_str() {
            "reject" => {
                let err = result.expect_err(&format!("{}: expected rejection", case.name));
                if let Some(expected) = &case.reject_class {
                    assert_eq!(
                        &error_class(&err),
                        expected,
                        "{}: wrong error class ({err})",
                        case.name
                    );
                }
            }
            "bytes" => {
                let bytes = result.unwrap_or_else(|e| panic!("{}: build failed: {e}", case.name));
                let golden = golden_dir.join(format!("{}.iprbin", case.name));
                if regenerate {
                    fs::create_dir_all(&golden_dir).unwrap();
                    fs::write(&golden, &bytes).unwrap();
                } else {
                    let want = fs::read(&golden).unwrap_or_else(|e| {
                        panic!(
                            "{}: missing golden {golden:?} ({e}); run REGENERATE_GOLDENS=1",
                            case.name
                        )
                    });
                    assert_eq!(bytes, want, "{}: output differs from golden", case.name);
                }
                // A bytes golden must also read back and parse cleanly.
                Reader::open(&bytes)
                    .unwrap_or_else(|e| panic!("{}: reader rejected own output: {e}", case.name));
            }
            other => panic!("{}: unknown expect {other:?}", case.name),
        }
        checked += 1;
    }
    eprintln!("conformance: {checked} cases checked (regenerate={regenerate})");
}
