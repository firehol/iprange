//! The v4 -> v3 snapshot bridge (§13): export a sealed, canonical v3 file from a
//! validated v4 image.
//!
//! Export **does not re-implement the v3 writer rules** — it opens the v4 file with the
//! v4 [`Reader`] (which fully validates it), scans the records in key order, maps each
//! `scope` to a v3 [`Value`], and feeds the ordered `(range, value)` stream plus the
//! caller-supplied [`V3Meta`] to the v3 [`Writer`]. The v3 writer owns coalescing,
//! value interning, the `u32` values-table cap, the 128-bit `unique_ip_count`
//! accounting, and byte-identical canonicalization (§13 export contract).
//!
//! The mapping (§13 step 2):
//! - `scope_width == 0` -> the v3 "present, no value" sentinel (`None`); `type_id` is
//!   ignored.
//! - `scope_width > 0` -> `Some(Value { type_id, bytes: scope.to_vec() })` — the scope
//!   bytes **verbatim** (v4 stores no `type_id`; it is the caller's, D11).
//!
//! Export is **not total**: every error the v3 writer returns from `add_range` / `build`
//! (the §13 unrepresentable cases — `unique_ip_count` reaches `2^128`, distinct
//! `(type_id, value)` pairs exceed the v3 cap, or a non-conforming `type_id` / `scope`)
//! is mapped to [`Error::ExportUnrepresentable`]. A corrupt v4 file or a v4-family /
//! v3-writer mismatch is a normal error (surfaced as-is).

use alloc::string::ToString;
use alloc::vec::Vec;

use iprange_format::{
    FeedMeta as V3FeedMeta, Ipv4Key as V3Ipv4Key, Ipv6Key as V3Ipv6Key, Value as V3Value,
    Writer as V3Writer,
};

use crate::error::{Error, Result};
use crate::key::{Ipv4Key, Ipv6Key};
use crate::reader::Reader;
use crate::spec::IpVersion;

/// The v3 inputs v4 does not store (§13): the six feed-meta fields, `license_flags`, and
/// `generation_unixtime`. Passed through to the v3 writer verbatim.
#[derive(Clone, Debug, Default)]
pub struct V3Meta {
    /// The six §7 feed-meta fields, in order.
    pub feed_meta: V3FeedMeta,
    /// The v3 `license_flags` (§7) — only the dont-redistribute bit is defined.
    pub license_flags: u32,
    /// The v3 `generation_unixtime` (§7) — when this snapshot was produced.
    pub generation_unixtime: u64,
}

/// Export a validated v4 image to a sealed v3 snapshot (§13).
///
/// Opens `v4_bytes` with the v4 [`Reader`] (full validation), scans every record in key
/// order, maps each `scope` to a v3 value (`type_id` applies only when the file carries
/// scopes), and seals the v3 file with `meta`.
///
/// Returns [`Error::ExportUnrepresentable`] if the v3 writer rejects the stream (§13);
/// other errors (a corrupt v4 file) are surfaced as-is.
pub fn export_v3(v4_bytes: &[u8], type_id: u32, meta: V3Meta) -> Result<Vec<u8>> {
    let reader = Reader::open(v4_bytes)?;
    // Exporting from a structurally invalid (but checksum-valid) image would
    // silently produce a wrong v3 file. Run the full §9 structural walk first.
    reader.validate()?;
    match reader.version() {
        IpVersion::V4 => {
            export_inner::<Ipv4Key, V3Ipv4Key, _>(&reader, type_id, meta, |k| V3Ipv4Key(k.0))
        }
        IpVersion::V6 => export_inner::<Ipv6Key, V3Ipv6Key, _>(&reader, type_id, meta, |k| {
            V3Ipv6Key { hi: k.hi, lo: k.lo }
        }),
    }
}

/// Width-generic core: scan the v4 records with key type `K4`, convert each key to the
/// v3 key `K3` (same numeric value, distinct type) via `to_v3`, and feed the v3 writer.
fn export_inner<K4, K3, F>(
    reader: &Reader<'_>,
    type_id: u32,
    meta: V3Meta,
    to_v3: F,
) -> Result<Vec<u8>>
where
    K4: crate::key::IpKey,
    K3: iprange_format::key::IpKey,
    F: Fn(K4) -> K3,
{
    let _scope_mode = reader.scope_mode();
    let mut writer =
        V3Writer::<K3>::new(meta.feed_meta, meta.license_flags, meta.generation_unixtime);

    // A v3 `add_range` error inside the scan closure is captured here and short-circuits
    // the scan (the closure cannot return a `Result`). Mapped to ExportUnrepresentable.
    let mut add_err: Option<Error> = None;
    reader.scan::<K4, _>(|from, to, scope_id| {
        if add_err.is_some() {
            return; // already failed; ignore the rest of the records
        }
        // v4.3: scope_id is u32. Encode as 4 LE bytes.
        let scope_bytes = scope_id.to_le_bytes();
        let value = Some(V3Value {
            type_id,
            bytes: scope_bytes.to_vec(),
        });
        if let Err(e) = writer.add_range(to_v3(from), to_v3(to), value) {
            add_err = Some(unrepresentable(e));
        }
    })?;
    if let Some(e) = add_err {
        return Err(e);
    }

    writer.build().map_err(unrepresentable)
}

/// Map a v3-writer rejection to the distinct [`Error::ExportUnrepresentable`] variant
/// (§13) — never leak it as a generic error.
fn unrepresentable(e: iprange_format::Error) -> Error {
    Error::ExportUnrepresentable(e.to_string())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::key::{Ipv4Key, Ipv6Key};
    use crate::writer::Writer as V4Writer;
    use iprange_format::Reader as V3Reader;

    fn meta() -> V3Meta {
        V3Meta {
            feed_meta: V3FeedMeta {
                name: "export-test".into(),
                category: "attacks".into(),
                ..Default::default()
            },
            license_flags: 0,
            generation_unixtime: 1_700_000_000,
        }
    }

    // Build a committed v4 IPv4 image from `(from, to, scope_id)` ranges in scalar
    // scope mode (mode 0). Under v4.3 every scope is a u32; the export bridge emits
    // `scope_id.to_le_bytes()` as the v3 value bytes.
    fn v4_image_v4(ranges: &[(u32, u32, u32)]) -> Vec<u8> {
        let mut w = V4Writer::<Ipv4Key>::create(0, 0).unwrap();
        for &(from, to, scope) in ranges {
            w.set(Ipv4Key(from), Ipv4Key(to), scope).unwrap();
        }
        w.commit(0, u64::MAX).unwrap();
        w.into_image().unwrap()
    }

    fn v4_image_v6(ranges: &[(u64, u64, u32)]) -> Vec<u8> {
        let mut w = V4Writer::<Ipv6Key>::create(0, 0).unwrap();
        for &(from, to, scope) in ranges {
            w.set(
                Ipv6Key { hi: 0, lo: from },
                Ipv6Key { hi: 0, lo: to },
                scope,
            )
            .unwrap();
        }
        w.commit(0, u64::MAX).unwrap();
        w.into_image().unwrap()
    }

    // --- ROUND-TRIP: v4 -> export_v3 -> v3 Reader, asserting parity ---

    #[test]
    fn roundtrip_v4_scope_width_4() {
        // Distinct scopes so no coalescing collapses the records.
        let ranges: &[(u32, u32, u32)] = &[(10, 20, 1), (30, 40, 2), (100, 200, 3)];
        let img = v4_image_v4(ranges);
        let type_id = 7u32;
        let v3 = export_v3(&img, type_id, meta()).unwrap();

        let r = V3Reader::open(&v3).unwrap();
        assert_eq!(r.record_count(), 3, "one v3 record per v4 record");
        // feed-meta passed through.
        let fm = r.feed_meta().unwrap();
        assert_eq!(fm.name, "export-test");
        assert_eq!(fm.category, "attacks");

        for &(from, to, scope) in ranges {
            // every covered IP hits, and resolves to the verbatim scope under type_id.
            for ip in [from, (from + to) / 2, to] {
                let hit = r.lookup_v4(V3Ipv4Key(ip)).unwrap().expect("present");
                let v = r.value(hit.value_id).expect("has value");
                assert_eq!(v.type_id, type_id);
                assert_eq!(
                    v.bytes,
                    scope.to_le_bytes(),
                    "v3 value bytes == v4 scope_id LE"
                );
            }
        }
        // gaps are absent.
        for ip in [9u32, 25, 41, 99, 201] {
            assert!(r.lookup_v4(V3Ipv4Key(ip)).unwrap().is_none(), "gap {ip}");
        }
    }

    #[test]
    fn roundtrip_v4_scope_zero_exports_zero_bytes() {
        // v4.3 always emits Some(value) via scope_id.to_le_bytes(); scope_id 0 maps to
        // four zero bytes. There is no "present, no value" None path in the bridge.
        let ranges: &[(u32, u32, u32)] = &[(10, 20, 0), (100, 110, 0)];
        let img = v4_image_v4(ranges);
        let v3 = export_v3(&img, 7, meta()).unwrap();

        let r = V3Reader::open(&v3).unwrap();
        assert_eq!(r.record_count(), 2);
        for &(from, to, _) in ranges {
            for ip in [from, to] {
                let hit = r.lookup_v4(V3Ipv4Key(ip)).unwrap().expect("present");
                let v = r.value(hit.value_id).expect("has value");
                assert_eq!(v.bytes, &[0, 0, 0, 0][..], "scope_id 0 -> 4 zero bytes");
            }
        }
        assert!(r.lookup_v4(V3Ipv4Key(50)).unwrap().is_none());
    }

    #[test]
    fn roundtrip_v6_scope_width_4() {
        let ranges: &[(u64, u64, u32)] = &[(10, 20, 0x09090909), (1000, 2000, 0x08080808)];
        let img = v4_image_v6(ranges);
        let type_id = 2u32;
        let v3 = export_v3(&img, type_id, meta()).unwrap();

        let r = V3Reader::open(&v3).unwrap();
        assert_eq!(r.record_count(), 2);
        for &(from, to, scope) in ranges {
            for lo in [from, to] {
                let hit = r
                    .lookup_v6(V3Ipv6Key { hi: 0, lo })
                    .unwrap()
                    .expect("present");
                let v = r.value(hit.value_id).expect("has value");
                assert_eq!(v.type_id, type_id);
                assert_eq!(v.bytes, scope.to_le_bytes());
            }
        }
        assert!(r.lookup_v6(V3Ipv6Key { hi: 0, lo: 500 }).unwrap().is_none());
    }

    #[test]
    fn roundtrip_v6_scope_zero_exports_zero_bytes() {
        let ranges: &[(u64, u64, u32)] = &[(5, 9, 0), (50, 60, 0)];
        let img = v4_image_v6(ranges);
        let v3 = export_v3(&img, 7, meta()).unwrap();
        let r = V3Reader::open(&v3).unwrap();
        assert_eq!(r.record_count(), 2);
        let hit = r.lookup_v6(V3Ipv6Key { hi: 0, lo: 7 }).unwrap().unwrap();
        let v = r.value(hit.value_id).unwrap();
        assert_eq!(v.bytes, &[0, 0, 0, 0][..]);
        assert!(r.lookup_v6(V3Ipv6Key { hi: 0, lo: 100 }).unwrap().is_none());
    }

    #[test]
    fn empty_v4_exports_empty_v3() {
        let img = v4_image_v4(&[]);
        let v3 = export_v3(&img, 1, meta()).unwrap();
        let r = V3Reader::open(&v3).unwrap();
        assert_eq!(r.record_count(), 0);
        assert!(r.lookup_v4(V3Ipv4Key(42)).unwrap().is_none());
    }

    // --- COALESCING: adjacent byte-equal scopes merge in the v3 file ---

    #[test]
    fn coalesces_adjacent_equal_scopes() {
        // Two contiguous records with the SAME scope_id coalesce into one v3 record
        // ([10,60]); a different scope after a gap ([100,110]) stays separate.
        let mut w = V4Writer::<Ipv4Key>::create(0, 0).unwrap();
        // Insert as one set so v4 keeps it as a single record across [10,40]...
        w.set(Ipv4Key(10), Ipv4Key(40), 5).unwrap();
        // ...plus a contiguous same-scope continuation [41,60]: v4 coalesces these.
        w.set(Ipv4Key(41), Ipv4Key(60), 5).unwrap();
        // A different scope after a gap: stays separate.
        w.set(Ipv4Key(100), Ipv4Key(110), 6).unwrap();
        w.commit(0, u64::MAX).unwrap();
        let img = w.into_image().unwrap();

        let v3 = export_v3(&img, 1, meta()).unwrap();
        let r = V3Reader::open(&v3).unwrap();
        // [10,60] (merged) + [100,110] = 2 records.
        assert_eq!(
            r.record_count(),
            2,
            "contiguous same-scope coalesced to one"
        );
        // the merged span is fully covered and resolves to the shared scope.
        for ip in [10u32, 40, 41, 60] {
            let hit = r.lookup_v4(V3Ipv4Key(ip)).unwrap().expect("present");
            assert_eq!(r.value(hit.value_id).unwrap().bytes, 5u32.to_le_bytes());
        }
        assert!(r.lookup_v4(V3Ipv4Key(70)).unwrap().is_none()); // the gap
        let hit = r.lookup_v4(V3Ipv4Key(105)).unwrap().unwrap();
        assert_eq!(r.value(hit.value_id).unwrap().bytes, 6u32.to_le_bytes());
    }

    #[test]
    fn distinct_scopes_share_one_interned_value() {
        // Two non-contiguous records with byte-equal scope intern to one value_id.
        let ranges: &[(u32, u32, u32)] = &[(10, 20, 0x01010101), (100, 200, 0x01010101)];
        let img = v4_image_v4(ranges);
        let v3 = export_v3(&img, 3, meta()).unwrap();
        let r = V3Reader::open(&v3).unwrap();
        assert_eq!(r.record_count(), 2);
        let a = r.lookup_v4(V3Ipv4Key(15)).unwrap().unwrap();
        let b = r.lookup_v4(V3Ipv4Key(150)).unwrap().unwrap();
        assert_eq!(
            a.value_id, b.value_id,
            "byte-equal scopes interned to one id"
        );
    }

    // --- membership (type_id 1): under v4.3 a scope_id is one 4-byte LE value, i.e.
    // exactly one feed-id, which is always a conforming membership value. Variable-width
    // scenarios (3-byte, or non-ascending multi-feed) that were unrepresentable under
    // the old byte-scope API are no longer reachable through the u32-only API. ---

    #[test]
    fn membership_single_feed_id_roundtrips() {
        // scope_id 2 -> bytes [2,0,0,0] -> a single, trivially-ascending feed-id 2,
        // which is a conforming type_id 1 membership value.
        let img = v4_image_v4(&[(10, 20, 2)]);
        let v3 = export_v3(&img, 1, meta()).unwrap();
        let r = V3Reader::open(&v3).unwrap();
        let hit = r.lookup_v4(V3Ipv4Key(15)).unwrap().unwrap();
        let v = r.value(hit.value_id).unwrap();
        assert_eq!(v.type_id, 1);
        assert_eq!(v.bytes, 2u32.to_le_bytes());
    }

    // --- determinism: same v4 input -> identical v3 bytes (canonical) ---

    #[test]
    fn export_is_deterministic() {
        let ranges: &[(u32, u32, u32)] = &[(10, 20, 1), (100, 200, 2)];
        let a = export_v3(&v4_image_v4(ranges), 9, meta()).unwrap();
        let b = export_v3(&v4_image_v4(ranges), 9, meta()).unwrap();
        assert_eq!(a, b, "same v4 input -> byte-identical v3 snapshot");
    }

    /// Cross-language golden vector: the v3 export of a fixed v4 input MUST be exactly
    /// these bytes. The identical vector is asserted by the Go suite
    /// (`TestExportCrossLanguageGolden`), so a drift in either the v4 writer, the v3
    /// writer, or this bridge — in either language — breaks one of the two tests. This is
    /// the in-repo proof that Rust and Go `export_v3` produce byte-identical v3 files.
    const CROSS_GOLDEN_HEX: &str = concat!(
        "495052414e474533030000004800000000020000000000004800000000000000",
        "0400000000000000030000000000000000f15365000000007b00000000000000",
        "0000000000000000010000000100000068010000000000002800000000000000",
        "0800000000000000000000000000000008c7c19038fe37bc38e43319e29a397c",
        "90db31b02273878f11cabac3d06a9aa402000000010000009001000000000000",
        "440000000000000010000000000000000000000000000000f2e428b622f379c6",
        "7609ceb5e410a8650b4093511aabf7c416fa041345de9aa00300000001000000",
        "d801000000000000280000000000000008000000000000000000000000000000",
        "0d2957ad5396b26d66ad391625bfb507f81f37af57c81d67a605fb489e04a7b6",
        "0500000000000000000200000000000000000000000000000800000000000000",
        "0000000000000000e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934c",
        "a495991b7852b855060000000500000063726f73730700000061747461636b73",
        "000000000000000000000000000000000c000000040000000300000000000000",
        "000000000000000000000000000000000a00000014000000000000001e000000",
        "280000000100000064000000c800000002000000000000000300000007000000",
        "0400000001000000070000000400000002000000070000000400000003000000",
    );

    fn decode_hex(s: &str) -> Vec<u8> {
        (0..s.len())
            .step_by(2)
            .map(|i| u8::from_str_radix(&s[i..i + 2], 16).unwrap())
            .collect()
    }

    #[test]
    fn export_cross_language_golden() {
        // The exact input the Go cross-language test also exports.
        let m = V3Meta {
            feed_meta: V3FeedMeta {
                name: "cross".into(),
                category: "attacks".into(),
                ..Default::default()
            },
            license_flags: 0,
            generation_unixtime: 1_700_000_000,
        };
        let mut w = V4Writer::<Ipv4Key>::create(0, 0).unwrap();
        w.set(Ipv4Key(10), Ipv4Key(20), 1).unwrap();
        w.set(Ipv4Key(30), Ipv4Key(40), 2).unwrap();
        w.set(Ipv4Key(100), Ipv4Key(200), 3).unwrap();
        w.commit(0, u64::MAX).unwrap();
        let out = export_v3(&w.into_image().unwrap(), 7, m).unwrap();
        assert_eq!(
            out,
            decode_hex(CROSS_GOLDEN_HEX),
            "cross-language golden vector"
        );
    }
}
