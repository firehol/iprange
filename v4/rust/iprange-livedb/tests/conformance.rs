//! Conformance tests: shared behavioral cases from v4/conformance/cases.json.
//! Compares merged interval coverage (not exact record count).

#![cfg(test)]

use iprange_livedb::{Ipv4Key, Reader, Writer};
use serde::Deserialize;

#[derive(Deserialize)]
struct Case {
    name: String,
    family: String,
    #[allow(dead_code)]
    scope_width: u8,
    ops: Vec<Op>,
    expect_scan: Vec<ScanEntry>,
    expect_lookup: Vec<LookupEntry>,
}

#[derive(Deserialize)]
#[serde(tag = "op")]
enum Op {
    #[serde(rename = "set")]
    Set {
        from: String,
        to: String,
        scope: Vec<u8>,
    },
    #[serde(rename = "delete")]
    Delete { from: String, to: String },
}

#[derive(Deserialize)]
struct ScanEntry(String, String, Vec<u8>);
#[derive(Deserialize)]
struct LookupEntry(String, Option<Vec<u8>>);

fn s2u(s: &str) -> u32 {
    s.parse().unwrap_or(0)
}
fn scope_b2u(b: &[u8]) -> u32 {
    let mut buf = [0u8; 4];
    for (i, &v) in b.iter().take(4).enumerate() {
        buf[i] = v;
    }
    u32::from_le_bytes(buf)
}

fn merge_adjacent(recs: &[(u32, u32, u32)]) -> Vec<(u32, u32, u32)> {
    if recs.is_empty() {
        return vec![];
    }
    let mut out = vec![recs[0]];
    for &(f, t, s) in recs.iter().skip(1) {
        let last = out.len() - 1;
        if out[last].2 == s && out[last].1.checked_add(1) == Some(f) {
            out[last].1 = t;
        } else {
            out.push((f, t, s));
        }
    }
    out
}

#[test]
fn behavioral_conformance() {
    let cases_json = include_str!("../../../conformance/cases.json");
    let cases: Vec<Case> = serde_json::from_str(cases_json).unwrap();

    for case in &cases {
        if case.family != "v4" {
            continue;
        }

        let mut w = Writer::<Ipv4Key>::create(0, 0).unwrap();
        for op in &case.ops {
            match op {
                Op::Set { from, to, scope } => {
                    w.set(Ipv4Key(s2u(from)), Ipv4Key(s2u(to)), scope_b2u(scope))
                        .unwrap();
                }
                Op::Delete { from, to } => {
                    w.delete(Ipv4Key(s2u(from)), Ipv4Key(s2u(to))).unwrap();
                }
            }
        }
        w.commit(0, u64::MAX).unwrap();
        let img = w.into_image().unwrap();
        let r = Reader::open(&img).unwrap();

        // Scan comparison: merge adjacent same-scope intervals.
        let mut actual: Vec<(u32, u32, u32)> = vec![];
        r.scan_v4(|f, t, s| actual.push((f.0, t.0, s))).unwrap();
        actual.sort_by_key(|r| r.0);

        let mut expected: Vec<(u32, u32, u32)> = case
            .expect_scan
            .iter()
            .map(|e| (s2u(&e.0), s2u(&e.1), scope_b2u(&e.2)))
            .collect();
        expected.sort_by_key(|r| r.0);

        assert_eq!(
            merge_adjacent(&actual),
            merge_adjacent(&expected),
            "{}: scan mismatch",
            case.name
        );

        // Lookup verification.
        for entry in &case.expect_lookup {
            let result = r.lookup(Ipv4Key(s2u(&entry.0))).unwrap();
            match &entry.1 {
                None => assert!(
                    result.is_none(),
                    "{}: lookup({}) should be None",
                    case.name,
                    entry.0
                ),
                Some(sb) => assert_eq!(
                    result,
                    Some(scope_b2u(sb)),
                    "{}: lookup({}) scope mismatch",
                    case.name,
                    entry.0
                ),
            }
        }
    }
}
