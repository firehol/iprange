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
    match reader.version() {
        IpVersion::V4 => export_inner::<Ipv4Key, V3Ipv4Key, _>(&reader, type_id, meta, |k| {
            V3Ipv4Key(k.0)
        }),
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
    let scope_width = reader.scope_width();
    let mut writer = V3Writer::<K3>::new(meta.feed_meta, meta.license_flags, meta.generation_unixtime);

    // A v3 `add_range` error inside the scan closure is captured here and short-circuits
    // the scan (the closure cannot return a `Result`). Mapped to ExportUnrepresentable.
    let mut add_err: Option<Error> = None;
    reader.scan::<K4, _>(|from, to, scope| {
        if add_err.is_some() {
            return; // already failed; ignore the rest of the records
        }
        // §13 step 2: scope_width == 0 -> sentinel (None); otherwise the scope bytes
        // verbatim under the caller's type_id.
        let value = if scope_width == 0 {
            None
        } else {
            Some(V3Value {
                type_id,
                bytes: scope.to_vec(),
            })
        };
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

    // Build a committed v4 IPv4 image from `(from, to, scope)` ranges with `scope_width`.
    fn v4_image_v4(scope_width: u8, ranges: &[(u32, u32, &[u8])]) -> Vec<u8> {
        let mut w = V4Writer::<Ipv4Key>::create(scope_width, 0);
        for &(from, to, scope) in ranges {
            w.set(Ipv4Key(from), Ipv4Key(to), scope).unwrap();
        }
        w.commit(0).unwrap();
        w.into_image()
    }

    fn v4_image_v6(scope_width: u8, ranges: &[(u64, u64, &[u8])]) -> Vec<u8> {
        let mut w = V4Writer::<Ipv6Key>::create(scope_width, 0);
        for &(from, to, scope) in ranges {
            w.set(Ipv6Key { hi: 0, lo: from }, Ipv6Key { hi: 0, lo: to }, scope)
                .unwrap();
        }
        w.commit(0).unwrap();
        w.into_image()
    }

    // --- ROUND-TRIP: v4 -> export_v3 -> v3 Reader, asserting parity ---

    #[test]
    fn roundtrip_v4_scope_width_4() {
        // Distinct scopes so no coalescing collapses the records.
        let ranges: &[(u32, u32, &[u8])] = &[
            (10, 20, &[1, 0, 0, 0]),
            (30, 40, &[2, 0, 0, 0]),
            (100, 200, &[3, 0, 0, 0]),
        ];
        let img = v4_image_v4(4, ranges);
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
                assert_eq!(v.bytes, scope, "v3 value bytes == v4 scope, verbatim");
            }
        }
        // gaps are absent.
        for ip in [9u32, 25, 41, 99, 201] {
            assert!(r.lookup_v4(V3Ipv4Key(ip)).unwrap().is_none(), "gap {ip}");
        }
    }

    #[test]
    fn roundtrip_v4_scope_width_0_presence() {
        // scope_width == 0 => v3 "present, no value" sentinel; type_id ignored.
        let ranges: &[(u32, u32, &[u8])] = &[(10, 20, &[][..]), (100, 110, &[][..])];
        let img = v4_image_v4(0, ranges);
        let v3 = export_v3(&img, /* ignored */ 1, meta()).unwrap();

        let r = V3Reader::open(&v3).unwrap();
        assert_eq!(r.record_count(), 2);
        for &(from, to, _) in ranges {
            for ip in [from, to] {
                let hit = r.lookup_v4(V3Ipv4Key(ip)).unwrap().expect("present");
                // sentinel: present, no value.
                assert_eq!(hit.value_id, 0xFFFF_FFFF, "present-no-value sentinel");
                assert!(r.value(hit.value_id).is_none());
            }
        }
        assert!(r.lookup_v4(V3Ipv4Key(50)).unwrap().is_none());
    }

    #[test]
    fn roundtrip_v6_scope_width_4() {
        let ranges: &[(u64, u64, &[u8])] =
            &[(10, 20, &[9, 9, 9, 9]), (1000, 2000, &[8, 8, 8, 8])];
        let img = v4_image_v6(4, ranges);
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
                assert_eq!(v.bytes, scope);
            }
        }
        assert!(r.lookup_v6(V3Ipv6Key { hi: 0, lo: 500 }).unwrap().is_none());
    }

    #[test]
    fn roundtrip_v6_scope_width_0_presence() {
        let ranges: &[(u64, u64, &[u8])] = &[(5, 9, &[][..]), (50, 60, &[][..])];
        let img = v4_image_v6(0, ranges);
        let v3 = export_v3(&img, 1, meta()).unwrap();
        let r = V3Reader::open(&v3).unwrap();
        assert_eq!(r.record_count(), 2);
        let hit = r.lookup_v6(V3Ipv6Key { hi: 0, lo: 7 }).unwrap().unwrap();
        assert_eq!(hit.value_id, 0xFFFF_FFFF);
        assert!(r.lookup_v6(V3Ipv6Key { hi: 0, lo: 100 }).unwrap().is_none());
    }

    #[test]
    fn empty_v4_exports_empty_v3() {
        let img = v4_image_v4(4, &[]);
        let v3 = export_v3(&img, 1, meta()).unwrap();
        let r = V3Reader::open(&v3).unwrap();
        assert_eq!(r.record_count(), 0);
        assert!(r.lookup_v4(V3Ipv4Key(42)).unwrap().is_none());
    }

    // --- COALESCING: adjacent byte-equal scopes merge in the v3 file ---

    #[test]
    fn coalesces_adjacent_equal_scopes() {
        // The v4 writer already coalesces same-scope adjacent `set`s into one record,
        // but force two physically-separate v4 records (via a deleted gap that is then
        // refilled) to prove the v3 writer also coalesces the exported stream. Simpler:
        // build two contiguous records with the SAME scope but a non-contiguous third,
        // then confirm the v3 reader sees them merged into fewer records than the v4 file
        // would if it did not coalesce. We rely on the v3 writer's coalescing (§9).
        let mut w = V4Writer::<Ipv4Key>::create(4, 0);
        // Insert as one set so v4 keeps it as a single record across [10,40]...
        w.set(Ipv4Key(10), Ipv4Key(40), &[5, 0, 0, 0]).unwrap();
        // ...plus a contiguous same-scope continuation [41,60]: v4 coalesces these.
        w.set(Ipv4Key(41), Ipv4Key(60), &[5, 0, 0, 0]).unwrap();
        // A different scope after a gap: stays separate.
        w.set(Ipv4Key(100), Ipv4Key(110), &[6, 0, 0, 0]).unwrap();
        w.commit(0).unwrap();
        let img = w.into_image();

        let v3 = export_v3(&img, 1, meta()).unwrap();
        let r = V3Reader::open(&v3).unwrap();
        // [10,60] (merged) + [100,110] = 2 records.
        assert_eq!(r.record_count(), 2, "contiguous same-scope coalesced to one");
        // the merged span is fully covered and resolves to the shared scope.
        for ip in [10u32, 40, 41, 60] {
            let hit = r.lookup_v4(V3Ipv4Key(ip)).unwrap().expect("present");
            assert_eq!(r.value(hit.value_id).unwrap().bytes, &[5, 0, 0, 0][..]);
        }
        assert!(r.lookup_v4(V3Ipv4Key(70)).unwrap().is_none()); // the gap
        let hit = r.lookup_v4(V3Ipv4Key(105)).unwrap().unwrap();
        assert_eq!(r.value(hit.value_id).unwrap().bytes, &[6, 0, 0, 0][..]);
    }

    #[test]
    fn distinct_scopes_share_one_interned_value() {
        // Two non-contiguous records with byte-equal scope intern to one value_id.
        let ranges: &[(u32, u32, &[u8])] = &[(10, 20, &[1, 1, 1, 1]), (100, 200, &[1, 1, 1, 1])];
        let img = v4_image_v4(4, ranges);
        let v3 = export_v3(&img, 3, meta()).unwrap();
        let r = V3Reader::open(&v3).unwrap();
        assert_eq!(r.record_count(), 2);
        let a = r.lookup_v4(V3Ipv4Key(15)).unwrap().unwrap();
        let b = r.lookup_v4(V3Ipv4Key(150)).unwrap().unwrap();
        assert_eq!(a.value_id, b.value_id, "byte-equal scopes interned to one id");
    }

    // --- ExportUnrepresentable: the v3 writer rejects the stream ---

    #[test]
    fn unrepresentable_bad_membership_value() {
        // type_id == 1 is a v3 membership set: bytes must be a non-empty, %4==0,
        // strictly-ascending list of LE u32 feed-ids. A 3-byte scope under type_id 1 is
        // not %4 == 0 => the v3 writer rejects => ExportUnrepresentable.
        let img = v4_image_v4(3, &[(10, 20, &[1, 2, 3])]);
        let err = export_v3(&img, 1, meta()).unwrap_err();
        assert!(
            matches!(err, Error::ExportUnrepresentable(_)),
            "expected ExportUnrepresentable, got {err:?}"
        );
    }

    #[test]
    fn unrepresentable_membership_not_ascending() {
        // 8-byte scope under type_id 1 = two LE u32 feed-ids; make them non-ascending
        // (5, then 5) => v3 rejects "strictly ascending" => ExportUnrepresentable.
        let scope: &[u8] = &[5, 0, 0, 0, 5, 0, 0, 0];
        let img = v4_image_v4(8, &[(10, 20, scope)]);
        let err = export_v3(&img, 1, meta()).unwrap_err();
        assert!(matches!(err, Error::ExportUnrepresentable(_)), "{err:?}");
    }

    #[test]
    fn membership_value_well_formed_roundtrips() {
        // A *conforming* type_id 1 scope (ascending LE u32 ids) exports fine — proving
        // the rejection above is about conformance, not type_id 1 itself.
        let scope: &[u8] = &[1, 0, 0, 0, 2, 0, 0, 0]; // feed-ids 1, 2
        let img = v4_image_v4(8, &[(10, 20, scope)]);
        let v3 = export_v3(&img, 1, meta()).unwrap();
        let r = V3Reader::open(&v3).unwrap();
        let hit = r.lookup_v4(V3Ipv4Key(15)).unwrap().unwrap();
        let v = r.value(hit.value_id).unwrap();
        assert_eq!(v.type_id, 1);
        assert_eq!(v.bytes, scope);
    }

    // --- determinism: same v4 input -> identical v3 bytes (canonical) ---

    #[test]
    fn export_is_deterministic() {
        let ranges: &[(u32, u32, &[u8])] =
            &[(10, 20, &[1, 0, 0, 0]), (100, 200, &[2, 0, 0, 0])];
        let a = export_v3(&v4_image_v4(4, ranges), 9, meta()).unwrap();
        let b = export_v3(&v4_image_v4(4, ranges), 9, meta()).unwrap();
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
        let mut w = V4Writer::<Ipv4Key>::create(4, 0);
        w.set(Ipv4Key(10), Ipv4Key(20), &[1, 0, 0, 0]).unwrap();
        w.set(Ipv4Key(30), Ipv4Key(40), &[2, 0, 0, 0]).unwrap();
        w.set(Ipv4Key(100), Ipv4Key(200), &[3, 0, 0, 0]).unwrap();
        w.commit(0).unwrap();
        let out = export_v3(&w.into_image(), 7, m).unwrap();
        assert_eq!(out, decode_hex(CROSS_GOLDEN_HEX), "cross-language golden vector");
    }
}
