use iprange_livedb::extsort::{ExtSortConfig, ExtSorter};
use iprange_livedb::interval::normalize_overlapping;
use iprange_livedb::migrate::{migrate, DesiredRecord, DesiredStream, MigrateOptions};
use iprange_livedb::overlap::{all_to_all_overlap, foreign_vs_all_slice};
use iprange_livedb::scope_table::MAX_BITMAP_WIDTH;
use iprange_livedb::spec::{SCOPE_MODE_BITMAP, SCOPE_MODE_INDIRECT, SCOPE_MODE_SCALAR};
use iprange_livedb::{Ipv4Key, Ipv6Key, Reader, Writer};

struct Round4DesiredStream {
    records: Vec<DesiredRecord<Ipv4Key>>,
    pos: usize,
    error: Option<&'static str>,
}

impl DesiredStream<Ipv4Key> for Round4DesiredStream {
    fn peek(&self) -> Option<&DesiredRecord<Ipv4Key>> {
        self.records.get(self.pos)
    }

    fn next(&mut self) -> Option<DesiredRecord<Ipv4Key>> {
        let result = self.records.get(self.pos).copied();
        if result.is_some() {
            self.pos += 1;
        }
        result
    }

    fn err(&self) -> Option<&str> {
        if self.pos >= self.records.len() {
            self.error
        } else {
            None
        }
    }
}

#[test]
fn normalize_overlapping_preserves_family_maximum() {
    let got_v4 = normalize_overlapping(&[(Ipv4Key(10), Ipv4Key(u32::MAX), 7)]);
    assert_eq!(got_v4.len(), 1, "IPv4 family-max tail was dropped");
    assert_eq!(got_v4[0].from, Ipv4Key(10));
    assert_eq!(got_v4[0].to, Ipv4Key(u32::MAX));
    assert_eq!(got_v4[0].scopes, vec![7]);

    let from_v6 = Ipv6Key { hi: 1, lo: 10 };
    let max_v6 = Ipv6Key {
        hi: u64::MAX,
        lo: u64::MAX,
    };
    let got_v6 = normalize_overlapping(&[(from_v6, max_v6, 7)]);
    assert_eq!(got_v6.len(), 1, "IPv6 family-max tail was dropped");
    assert_eq!(got_v6[0].from, from_v6);
    assert_eq!(got_v6[0].to, max_v6);
    assert_eq!(got_v6[0].scopes, vec![7]);
}

#[test]
fn migrate_source_error_poisons_transaction() {
    let mut writer = Writer::<Ipv4Key>::create(SCOPE_MODE_SCALAR, 0).unwrap();
    let mut stream = Round4DesiredStream {
        records: vec![DesiredRecord {
            from: Ipv4Key(10),
            to: Ipv4Key(20),
            scope_id: 7,
        }],
        pos: 0,
        error: Some("late source read failure"),
    };
    assert!(migrate(&mut writer, &mut stream, &MigrateOptions::default()).is_err());
    assert!(
        writer.commit(1, u64::MAX).is_err(),
        "Commit published partial migration state after Migrate returned an error"
    );
}

#[test]
fn migrate_rejects_malformed_desired_stream_and_poisons_transaction() {
    let cases = [
        (
            "unsorted",
            vec![
                DesiredRecord {
                    from: Ipv4Key(100),
                    to: Ipv4Key(110),
                    scope_id: 1,
                },
                DesiredRecord {
                    from: Ipv4Key(10),
                    to: Ipv4Key(20),
                    scope_id: 2,
                },
            ],
        ),
        (
            "overlapping",
            vec![
                DesiredRecord {
                    from: Ipv4Key(10),
                    to: Ipv4Key(20),
                    scope_id: 1,
                },
                DesiredRecord {
                    from: Ipv4Key(20),
                    to: Ipv4Key(30),
                    scope_id: 2,
                },
            ],
        ),
        (
            "reversed",
            vec![DesiredRecord {
                from: Ipv4Key(20),
                to: Ipv4Key(10),
                scope_id: 1,
            }],
        ),
    ];

    for (name, records) in cases {
        let mut writer = Writer::<Ipv4Key>::create(SCOPE_MODE_SCALAR, 0).unwrap();
        let mut stream = Round4DesiredStream {
            records,
            pos: 0,
            error: None,
        };
        assert!(
            migrate(&mut writer, &mut stream, &MigrateOptions::default()).is_err(),
            "Migrate accepted {name} desired input"
        );
        assert!(
            writer.commit(1, u64::MAX).is_err(),
            "Commit accepted transaction after {name} migration failure"
        );
    }
}

#[test]
fn foreign_vs_all_malformed_input_never_returns_wrong_counts() {
    let mut writer = Writer::<Ipv4Key>::create(SCOPE_MODE_BITMAP, 0).unwrap();
    writer.set(Ipv4Key(0), Ipv4Key(30), 1).unwrap();
    let overlapping = [(Ipv4Key(10), Ipv4Key(20)), (Ipv4Key(15), Ipv4Key(25))];
    let mut total = 0u64;
    let result = foreign_vs_all_slice(&writer, &overlapping, &mut |_, _, n| total += n);
    assert!(
        result.is_err() || total == 16,
        "overlapping input returned {total} addresses, want union count 16 or an error"
    );

    let mut writer = Writer::<Ipv4Key>::create(SCOPE_MODE_BITMAP, 0).unwrap();
    writer.set(Ipv4Key(0), Ipv4Key(9), 1).unwrap();
    writer.set(Ipv4Key(20), Ipv4Key(29), 1).unwrap();
    let unsorted = [(Ipv4Key(20), Ipv4Key(25)), (Ipv4Key(0), Ipv4Key(5))];
    let mut total = 0u64;
    let result = foreign_vs_all_slice(&writer, &unsorted, &mut |_, _, n| total += n);
    assert!(
        result.is_err() || total == 12,
        "unsorted input returned {total} addresses, want exact count 12 or an error"
    );

    let reversed = [(Ipv4Key(20), Ipv4Key(10))];
    assert!(
        foreign_vs_all_slice(&writer, &reversed, &mut |_, _, _| {}).is_err(),
        "ForeignVsAll accepted a range with from > to"
    );
}

#[test]
fn all_to_all_reports_one_total_per_feed_pair() {
    let mut writer = Writer::<Ipv4Key>::create(SCOPE_MODE_BITMAP, 0).unwrap();
    writer.set(Ipv4Key(0), Ipv4Key(9), 0b11).unwrap();
    writer.set(Ipv4Key(20), Ipv4Key(29), 0b11).unwrap();
    let mut got = Vec::new();
    all_to_all_overlap(&writer, &mut |overlap| got.push(overlap)).unwrap();
    assert_eq!(got.len(), 1, "callbacks = {got:?}, want one aggregate");
    assert_eq!(got[0].feed_a, 0);
    assert_eq!(got[0].feed_b, 1);
    assert_eq!(got[0].ip_count, 20);
}

#[test]
fn overlap_rejects_scalar_scope_mode() {
    let mut writer = Writer::<Ipv4Key>::create(SCOPE_MODE_SCALAR, 0).unwrap();
    writer.set(Ipv4Key(1), Ipv4Key(10), 123).unwrap();
    assert!(
        all_to_all_overlap(&writer, &mut |_| {}).is_err(),
        "AllToAllOverlap silently returned empty data for scalar mode"
    );
    assert!(
        foreign_vs_all_slice(&writer, &[(Ipv4Key(1), Ipv4Key(10))], &mut |_, _, _| {},).is_err(),
        "ForeignVsAll silently returned empty data for scalar mode"
    );
}

#[test]
fn ipv6_overlap_count_reports_overflow() {
    let mut writer = Writer::<Ipv6Key>::create(SCOPE_MODE_BITMAP, 0).unwrap();
    writer
        .set(
            Ipv6Key { hi: 0, lo: 0 },
            Ipv6Key {
                hi: 0,
                lo: u64::MAX,
            },
            0b11,
        )
        .unwrap();
    assert!(
        all_to_all_overlap(&writer, &mut |_| {}).is_err(),
        "2^64-address IPv6 overlap did not report u64 overflow"
    );
}

#[test]
fn indirect_scope_feed_bit_must_round_trip_without_truncation() {
    let mut writer = Writer::<Ipv4Key>::create(SCOPE_MODE_INDIRECT, 0).unwrap();
    let original = writer.scope_intern(&[1]).unwrap();
    let high = writer
        .scope_bitmap_set_feed(original, (MAX_BITMAP_WIDTH * 8) as u32)
        .unwrap();
    writer.set(Ipv4Key(1), Ipv4Key(1), high).unwrap();
    writer.commit(1, u64::MAX).unwrap();
    let image = writer.into_image().unwrap();
    let reader = Reader::open(&image).unwrap();
    let stored_id = reader.lookup_v4(Ipv4Key(1)).unwrap().unwrap();
    let bitmap = reader.scope_resolve(stored_id).unwrap();
    assert!(
        bitmap.len() > MAX_BITMAP_WIDTH && bitmap[MAX_BITMAP_WIDTH] & 1 != 0,
        "feed bit {} was silently lost; persisted bitmap length={}",
        MAX_BITMAP_WIDTH * 8,
        bitmap.len()
    );
}

#[test]
fn indirect_scope_bitmap_is_canonical() {
    let mut writer = Writer::<Ipv4Key>::create(SCOPE_MODE_INDIRECT, 0).unwrap();
    let original = writer.scope_intern(&[1]).unwrap();
    let expanded = writer.scope_bitmap_set_feed(original, 8).unwrap();
    let cleared = writer.scope_bitmap_clear_feed(expanded, 8).unwrap();
    assert_eq!(
        cleared, original,
        "identical membership minted another scope"
    );
}

#[test]
fn indirect_scope_mutation_rejects_unknown_scope() {
    let mut writer = Writer::<Ipv4Key>::create(SCOPE_MODE_INDIRECT, 0).unwrap();
    assert!(writer.scope_bitmap_set_feed(999, 1).is_err());
    assert!(writer.scope_bitmap_clear_feed(999, 1).is_err());
}

#[test]
fn indirect_records_reject_dangling_scope_ids() {
    let mut set_writer = Writer::<Ipv4Key>::create(SCOPE_MODE_INDIRECT, 0).unwrap();
    assert!(
        set_writer.set(Ipv4Key(1), Ipv4Key(10), 999).is_err(),
        "Set accepted a dangling scope ID"
    );

    let mut append_writer = Writer::<Ipv4Key>::create(SCOPE_MODE_INDIRECT, 0).unwrap();
    assert!(
        append_writer.append(Ipv4Key(1), Ipv4Key(10), 999).is_err(),
        "Append accepted a dangling scope ID"
    );
}

#[test]
fn multi_level_scope_lookup_routes_at_every_boundary() {
    let mut writer = Writer::<Ipv4Key>::create(SCOPE_MODE_INDIRECT, 0).unwrap();
    for i in 0..8_000u32 {
        let id = writer.scope_intern(&i.to_le_bytes()).unwrap();
        assert_eq!(id, i + 1);
    }
    writer.commit(1, u64::MAX).unwrap();
    for id in [1u32, 2, 7_634, 7_635, 7_636, 7_999, 8_000] {
        assert_eq!(
            writer.scope_resolve(id).as_deref(),
            Some((id - 1).to_le_bytes().as_slice()),
            "scope {id} was routed to the wrong leaf"
        );
    }
}

#[test]
fn feed_add_at_family_maximum_does_not_duplicate_tail() {
    let mut writer = Writer::<Ipv4Key>::create(SCOPE_MODE_BITMAP, 0).unwrap();
    writer
        .feed_add_range(Ipv4Key(10), Ipv4Key(u32::MAX), 0)
        .unwrap();
    writer
        .feed_add_range(Ipv4Key(10), Ipv4Key(u32::MAX), 1)
        .unwrap();
    writer.commit(1, u64::MAX).unwrap();
    let image = writer.into_image().unwrap();
    let reader = Reader::open(&image).unwrap();
    reader
        .validate()
        .expect("FeedAddRange produced an invalid tree");
    let mut got = Vec::new();
    reader
        .scan_v4(|from, to, scope| got.push((from, to, scope)))
        .unwrap();
    assert_eq!(
        got,
        vec![(Ipv4Key(10), Ipv4Key(u32::MAX), 3)],
        "FeedAddRange duplicated the family-max tail"
    );
}

#[test]
fn external_sorter_rejects_invalid_configuration_and_ranges() {
    let dir = round4_temp_dir("sort-invalid");
    let mut zero_chunk = ExtSorter::<Ipv4Key>::new(ExtSortConfig {
        chunk_size: 0,
        temp_dir: Some(dir.clone()),
    });
    assert!(
        zero_chunk.add(Ipv4Key(1), Ipv4Key(1), 1).is_err(),
        "zero chunk_size was accepted"
    );

    let mut reversed = ExtSorter::<Ipv4Key>::new(ExtSortConfig {
        chunk_size: 10,
        temp_dir: Some(dir.clone()),
    });
    assert!(
        reversed.add(Ipv4Key(20), Ipv4Key(10), 1).is_err(),
        "ExtSorter.add accepted from > to"
    );
    let _ = std::fs::remove_dir_all(dir);
}

#[test]
fn dropping_external_sorter_cleans_spill_files() {
    let dir = round4_temp_dir("sort-drop");
    {
        let mut sorter = ExtSorter::<Ipv4Key>::new(ExtSortConfig {
            chunk_size: 1,
            temp_dir: Some(dir.clone()),
        });
        for i in 0..4u32 {
            sorter.add(Ipv4Key(i * 2), Ipv4Key(i * 2), 1).unwrap();
        }
    }
    let entries = std::fs::read_dir(&dir).unwrap().count();
    let _ = std::fs::remove_dir_all(&dir);
    assert_eq!(
        entries, 0,
        "dropping ExtSorter leaked {entries} spill files"
    );
}

fn round4_temp_dir(label: &str) -> std::path::PathBuf {
    let dir = std::env::temp_dir().join(format!(
        "iprange-round4-{label}-{}-{:?}",
        std::process::id(),
        std::thread::current().id()
    ));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).unwrap();
    dir
}
