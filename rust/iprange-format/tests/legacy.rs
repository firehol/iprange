//! Legacy-format conformance: parse the real `iprange --print-binary` fixtures under
//! conformance/legacy/, check the decoded ranges against each manifest, and verify
//! migration to v3 (build → read back → lookup).

use std::fs;
use std::net::{Ipv4Addr, Ipv6Addr};
use std::path::{Path, PathBuf};

use iprange_format::legacy::{self, Legacy};
use iprange_format::{FeedMeta, Ipv4Key, Ipv6Key, Reader, Writer};
use serde::Deserialize;

#[derive(Deserialize)]
struct Manifest {
    name: String,
    ip_version: String,
    file: String,
    optimized: bool,
    unique_ips: String,
    lines: u64,
    ranges: Vec<R>,
}

#[derive(Deserialize)]
struct R {
    start: String,
    end: String,
}

fn legacy_dir() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../../conformance/legacy")
}

#[test]
fn legacy_corpus() {
    let dir = legacy_dir();
    let mut manifests: Vec<PathBuf> = fs::read_dir(&dir)
        .unwrap()
        .map(|e| e.unwrap().path())
        .filter(|p| p.extension().is_some_and(|x| x == "json"))
        .collect();
    manifests.sort();
    assert!(!manifests.is_empty(), "no legacy manifests in {dir:?}");

    for mpath in &manifests {
        let m: Manifest = serde_json::from_str(&fs::read_to_string(mpath).unwrap()).unwrap();
        let bytes = fs::read(dir.join(&m.file)).unwrap();
        let parsed =
            legacy::parse(&bytes).unwrap_or_else(|e| panic!("{}: parse failed: {e}", m.name));

        match (&m.ip_version[..], parsed) {
            (
                "v4",
                Legacy::V4 {
                    optimized,
                    unique_ips,
                    lines,
                    ranges,
                },
            ) => {
                assert_eq!(optimized, m.optimized, "{}: optimized", m.name);
                assert_eq!(
                    unique_ips.to_string(),
                    m.unique_ips,
                    "{}: unique_ips",
                    m.name
                );
                assert_eq!(lines, m.lines, "{}: lines", m.name);
                assert_eq!(ranges.len(), m.ranges.len(), "{}: range count", m.name);
                for (got, want) in ranges.iter().zip(&m.ranges) {
                    let s = Ipv4Key(u32::from(want.start.parse::<Ipv4Addr>().unwrap()));
                    let e = Ipv4Key(u32::from(want.end.parse::<Ipv4Addr>().unwrap()));
                    assert_eq!(*got, (s, e), "{}: range", m.name);
                }
                migrate_v4_and_check(&m, &ranges);
            }
            (
                "v6",
                Legacy::V6 {
                    optimized,
                    unique_ips,
                    lines,
                    ranges,
                },
            ) => {
                assert_eq!(optimized, m.optimized, "{}: optimized", m.name);
                assert_eq!(
                    unique_ips.to_string(),
                    m.unique_ips,
                    "{}: unique_ips",
                    m.name
                );
                assert_eq!(lines, m.lines, "{}: lines", m.name);
                assert_eq!(ranges.len(), m.ranges.len(), "{}: range count", m.name);
                for (got, want) in ranges.iter().zip(&m.ranges) {
                    let s = Ipv6Key::from_u128(u128::from(want.start.parse::<Ipv6Addr>().unwrap()));
                    let e = Ipv6Key::from_u128(u128::from(want.end.parse::<Ipv6Addr>().unwrap()));
                    assert_eq!(*got, (s, e), "{}: range", m.name);
                }
                migrate_v6_and_check(&m, &ranges);
            }
            _ => panic!("{}: family mismatch", m.name),
        }
    }
}

fn meta(name: &str) -> FeedMeta {
    FeedMeta {
        name: name.into(),
        category: "migrated".into(),
        ..Default::default()
    }
}

fn migrate_v4_and_check(m: &Manifest, ranges: &[(Ipv4Key, Ipv4Key)]) {
    let mut w = Writer::<Ipv4Key>::new(meta(&m.name), 0, 0);
    for &(s, e) in ranges {
        w.add_range(s, e, None).unwrap();
    }
    let v3 = w.build().unwrap();
    let r = Reader::open(&v3).unwrap();
    assert_eq!(
        r.record_count(),
        ranges.len() as u64,
        "{}: migrated record count",
        m.name
    );
    // every legacy range's start is present in the migrated v3 file.
    for &(s, _) in ranges {
        assert!(
            r.lookup_v4(s).unwrap().is_some(),
            "{}: migrated lookup",
            m.name
        );
    }
}

fn migrate_v6_and_check(m: &Manifest, ranges: &[(Ipv6Key, Ipv6Key)]) {
    let mut w = Writer::<Ipv6Key>::new(meta(&m.name), 0, 0);
    for &(s, e) in ranges {
        w.add_range(s, e, None).unwrap();
    }
    let v3 = w.build().unwrap();
    let r = Reader::open(&v3).unwrap();
    assert_eq!(
        r.record_count(),
        ranges.len() as u64,
        "{}: migrated record count",
        m.name
    );
    for &(s, _) in ranges {
        assert!(
            r.lookup_v6(s).unwrap().is_some(),
            "{}: migrated lookup",
            m.name
        );
    }
}
