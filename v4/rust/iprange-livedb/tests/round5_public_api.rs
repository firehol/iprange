use iprange_livedb::spec;
use iprange_livedb::{DesiredRecord, Ipv4Key, Ipv6Key, Reader, SortedStream, Writer};
use std::ops::ControlFlow;

#[test]
fn query_cidrs_merged_combines_selected_adjacent_scopes() {
    let mut writer = Writer::<Ipv4Key>::create(spec::SCOPE_MODE_SCALAR, 0).unwrap();
    for (from, to, scope) in [(0, 3, 1), (4, 7, 2), (8, 15, 1), (20, 23, 2)] {
        writer.set(Ipv4Key(from), Ipv4Key(to), scope).unwrap();
    }
    writer.commit(1, u64::MAX).unwrap();
    let image = writer.into_image().unwrap();
    let reader = Reader::open(&image).unwrap();
    let mut cursor = reader.cursor::<Ipv4Key>().unwrap();
    let mut got = Vec::new();
    cursor
        .query_cidrs_merged(
            Ipv4Key(0),
            Ipv4Key(23),
            |scope| scope != 0,
            |network, prefix| {
                got.push((network, prefix));
                ControlFlow::Continue(())
            },
        )
        .unwrap();
    assert_eq!(got, vec![(Ipv4Key(0), 28), (Ipv4Key(20), 30)]);
}

#[test]
fn ipv6_merged_cidr_at_family_maximum() {
    let max = Ipv6Key {
        hi: u64::MAX,
        lo: u64::MAX,
    };
    let from = Ipv6Key {
        hi: u64::MAX,
        lo: u64::MAX - 3,
    };
    let mut writer = Writer::<Ipv6Key>::create(spec::SCOPE_MODE_SCALAR, 0).unwrap();
    writer.set(from, max, 1).unwrap();
    writer.commit(1, u64::MAX).unwrap();
    let image = writer.into_image().unwrap();
    let reader = Reader::open(&image).unwrap();
    let mut cursor = reader.cursor::<Ipv6Key>().unwrap();
    let mut got = Vec::new();
    cursor
        .query_cidrs_merged(
            from,
            max,
            |scope| scope == 1,
            |network, prefix| {
                got.push((network, prefix));
                ControlFlow::Continue(())
            },
        )
        .unwrap();
    assert_eq!(got, vec![(from, 126)]);
    assert_eq!(cursor.count_ips(from, max, |scope| scope == 1), 4);
}

#[cfg(unix)]
fn temp_dir(label: &str) -> std::path::PathBuf {
    let dir = std::env::temp_dir().join(format!(
        "iprange-round5-public-{label}-{}-{:?}",
        std::process::id(),
        std::thread::current().id()
    ));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).unwrap();
    dir
}

#[cfg(unix)]
#[test]
fn file_writer_delegates_migration_and_overlap() {
    use iprange_livedb::os::FileWriter;
    use iprange_livedb::overlap::FeedOverlap;
    use iprange_livedb::MigrateOptions;
    use std::collections::BTreeMap;

    let dir = temp_dir("delegates");
    let migration_path = dir.join("migration.iprdb");
    let mut migration =
        FileWriter::<Ipv4Key>::create(&migration_path, spec::SCOPE_MODE_SCALAR, 0).unwrap();
    let mut desired = SortedStream::from_unsorted(vec![
        DesiredRecord {
            from: Ipv4Key(30),
            to: Ipv4Key(39),
            scope_id: 2,
        },
        DesiredRecord {
            from: Ipv4Key(10),
            to: Ipv4Key(19),
            scope_id: 1,
        },
    ]);
    let counters = migration
        .migrate(&mut desired, &MigrateOptions::default())
        .unwrap();
    assert_eq!(counters.added, 2);
    migration.commit(1).unwrap();
    assert_eq!(migration.record_count(), 2);
    migration.close();

    let overlap_path = dir.join("overlap.iprdb");
    let mut overlap =
        FileWriter::<Ipv4Key>::create(&overlap_path, spec::SCOPE_MODE_BITMAP, 0).unwrap();
    overlap.set(Ipv4Key(10), Ipv4Key(19), 0b11).unwrap();
    overlap.commit(1).unwrap();
    let mut pairs = Vec::new();
    overlap
        .all_to_all_overlap(&mut |pair| pairs.push(pair))
        .unwrap();
    assert_eq!(
        pairs,
        vec![FeedOverlap {
            feed_a: 0,
            feed_b: 1,
            ip_count: 10,
        }]
    );
    let mut counts = BTreeMap::new();
    overlap
        .foreign_vs_all_slice(&[(Ipv4Key(12), Ipv4Key(15))], &mut |feed, _, count| {
            *counts.entry(feed).or_insert(0u64) += count
        })
        .unwrap();
    assert_eq!(counts.get(&0), Some(&4));
    assert_eq!(counts.get(&1), Some(&4));
    overlap.close();
    let _ = std::fs::remove_dir_all(dir);
}

#[test]
fn all_to_all_overlap_emits_one_sorted_deterministic_result_per_pair() {
    use iprange_livedb::overlap::{all_to_all_overlap, FeedOverlap};

    let mut writer = Writer::<Ipv4Key>::create(spec::SCOPE_MODE_BITMAP, 0).unwrap();
    for (from, to, scope) in [(0, 9, 0b0111), (20, 24, 0b1010), (30, 34, 0b0101)] {
        writer.set(Ipv4Key(from), Ipv4Key(to), scope).unwrap();
    }
    let want = vec![
        FeedOverlap {
            feed_a: 0,
            feed_b: 1,
            ip_count: 10,
        },
        FeedOverlap {
            feed_a: 0,
            feed_b: 2,
            ip_count: 15,
        },
        FeedOverlap {
            feed_a: 1,
            feed_b: 2,
            ip_count: 10,
        },
        FeedOverlap {
            feed_a: 1,
            feed_b: 3,
            ip_count: 5,
        },
    ];
    for _ in 0..2 {
        let mut got = Vec::new();
        all_to_all_overlap(&writer, &mut |overlap| got.push(overlap)).unwrap();
        assert_eq!(got, want);
    }
}
