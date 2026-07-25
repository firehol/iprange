use std::fs;
use std::path::PathBuf;
use std::time::{SystemTime, UNIX_EPOCH};

use iprange_livedb::{
    create_live, AddressFamily, AddressRange, CancellationToken, Cardinality129, FeedName,
    FinishedWorkflow, Ipv4Key, LiveReader, LiveWriter, MembershipImportSource, MembershipOperation,
    TransactionBudget, ValueKind, ValueTag,
};

struct TestPair {
    main: PathBuf,
}

impl TestPair {
    fn new(label: &str) -> Self {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        Self {
            main: std::env::temp_dir().join(format!(
                "iprange-v4-import-property-{label}-{}-{unique}",
                std::process::id()
            )),
        }
    }

    fn sidecar(&self) -> PathBuf {
        let mut name = self.main.file_name().unwrap().to_os_string();
        name.push(".readers");
        self.main.with_file_name(name)
    }
}

impl Drop for TestPair {
    fn drop(&mut self) {
        let _ = fs::remove_file(&self.main);
        let _ = fs::remove_file(self.sidecar());
    }
}

fn budget() -> TransactionBudget {
    TransactionBudget {
        max_heap_bytes: 2 * 1024 * 1024,
        max_private_pages: 50_000,
        max_file_growth_pages: 50_000,
        max_open_files: 2,
    }
}

fn next(seed: &mut u64) -> u32 {
    *seed = seed.wrapping_mul(6_364_136_223_846_793_005).wrapping_add(1);
    (*seed >> 32) as u32
}

fn add_feed(
    writer: &mut LiveWriter,
    feed_name: &str,
    bit: u8,
    seed: &mut u64,
    model: &mut [u8; 256],
    cancellation: &CancellationToken,
) {
    let mut ranges = Vec::with_capacity(40);
    for _ in 0..40 {
        let first = (next(seed) & 255) as u8;
        let second = (next(seed) & 255) as u8;
        let (from, to) = if first <= second {
            (first, second)
        } else {
            (second, first)
        };
        ranges.push(AddressRange {
            from: Ipv4Key(u32::from(from)),
            to: Ipv4Key(u32::from(to)),
        });
        for address in from..=to {
            model[usize::from(address)] |= bit;
        }
    }
    let mut create = writer
        .begin_create_feed(FeedName::new(feed_name).unwrap(), cancellation)
        .unwrap();
    create.add_ranges_v4_slice(&ranges).unwrap();
    match create.finish_input().unwrap() {
        FinishedWorkflow::Changed(prepared) => {
            prepared.commit().unwrap();
        }
        FinishedWorkflow::NoChange(_) => panic!("feed creation cannot be a no-op"),
    }
}

fn cardinality(count: usize) -> Cardinality129 {
    Cardinality129::from_u64(count as u64)
}

fn interval_count(model: &[u8; 256]) -> u64 {
    model
        .iter()
        .enumerate()
        .filter(|(index, value)| **value != 0 && (*index == 0 || model[*index - 1] != **value))
        .count() as u64
}

#[test]
fn randomized_import_matches_named_feed_union_and_exact_report() {
    let source_files = TestPair::new("source");
    let destination_files = TestPair::new("destination");
    for files in [&source_files, &destination_files] {
        create_live(
            &files.main,
            AddressFamily::Ipv4,
            ValueKind::Membership,
            ValueTag::new(b"membership").unwrap(),
            4,
            &CancellationToken::new(),
        )
        .unwrap();
    }

    let cancellation = CancellationToken::new();
    let mut seed = 0x1297_3a4b_55aa_f00d;
    let mut source_model = [0u8; 256];
    let mut source_writer =
        LiveWriter::open(&source_files.main, budget(), &CancellationToken::new()).unwrap();
    for (feed_name, bit) in [("alpha", 1), ("beta", 2), ("gamma", 4), ("delta", 8)] {
        add_feed(
            &mut source_writer,
            feed_name,
            bit,
            &mut seed,
            &mut source_model,
            &cancellation,
        );
    }
    source_writer.close().unwrap();

    let mut destination_model = [0u8; 256];
    let mut writer =
        LiveWriter::open(&destination_files.main, budget(), &CancellationToken::new()).unwrap();
    for (feed_name, bit) in [("beta", 2), ("delta", 8), ("epsilon", 16)] {
        add_feed(
            &mut writer,
            feed_name,
            bit,
            &mut seed,
            &mut destination_model,
            &cancellation,
        );
    }

    let mut source = LiveReader::open(&source_files.main, &CancellationToken::new()).unwrap();
    let source_records = source.info().unwrap().range_record_count;
    assert_eq!(source_records, interval_count(&source_model));
    let before_records = interval_count(&destination_model);
    let prepared = match writer
        .begin_membership_import(MembershipImportSource::Live(&source), &cancellation)
        .unwrap()
        .finish_input()
        .unwrap()
    {
        FinishedWorkflow::Changed(prepared) => prepared,
        FinishedWorkflow::NoChange(_) => panic!("randomized import unexpectedly did nothing"),
    };
    let report = *prepared.report();
    let after_model = std::array::from_fn(|index| source_model[index] | destination_model[index]);
    assert_eq!(report.input_record_count, source_records);
    assert_eq!(report.input_normalized_interval_count, source_records);
    assert_eq!(report.before_range_record_count, before_records);
    assert_eq!(
        report.after_range_record_count,
        interval_count(&after_model)
    );
    assert_eq!(
        report.input_addresses,
        cardinality(source_model.iter().filter(|&&value| value != 0).count())
    );
    assert_eq!(
        report.before_addresses,
        cardinality(
            destination_model
                .iter()
                .filter(|&&value| value != 0)
                .count()
        )
    );
    assert_eq!(
        report.after_addresses,
        cardinality(after_model.iter().filter(|&&value| value != 0).count())
    );
    assert_eq!(
        report.unchanged_value_addresses,
        cardinality(
            after_model
                .iter()
                .zip(destination_model)
                .filter(|(after, before)| **after != 0 && **after == *before)
                .count()
        )
    );
    assert_eq!(
        report.changed_value_addresses,
        cardinality(
            after_model
                .iter()
                .zip(destination_model)
                .filter(|(after, before)| *before != 0 && **after != *before)
                .count()
        )
    );
    assert_eq!(
        report.added_addresses,
        cardinality(
            after_model
                .iter()
                .zip(destination_model)
                .filter(|(after, before)| **after != 0 && *before == 0)
                .count()
        )
    );
    assert_eq!(report.source_feed_count, 4);
    assert_eq!(report.matched_feed_count, 2);
    assert_eq!(report.created_feed_count, 2);
    prepared.commit().unwrap();
    source.close().unwrap();
    writer.close().unwrap();

    let mut reader = LiveReader::open(&destination_files.main, &CancellationToken::new()).unwrap();
    let feeds = ["alpha", "beta", "gamma", "delta", "epsilon"]
        .map(|feed_name| reader.lookup_feed(feed_name).unwrap().unwrap().index);
    for (address, expected) in after_model.into_iter().enumerate() {
        let membership = reader
            .lookup_membership_v4(Ipv4Key(address as u32))
            .unwrap();
        for (index, feed) in feeds.into_iter().enumerate() {
            let actual = match &membership {
                Some(value) => value.contains_index(feed).unwrap(),
                None => false,
            };
            assert_eq!(actual, expected & (1 << index) != 0);
        }
    }
    reader.close().unwrap();
}

#[test]
fn import_translates_sparse_feed_indexes_across_bitmap_words() {
    let source_files = TestPair::new("wide-source");
    let destination_files = TestPair::new("wide-destination");
    for files in [&source_files, &destination_files] {
        create_live(
            &files.main,
            AddressFamily::Ipv4,
            ValueKind::Membership,
            ValueTag::new(b"membership").unwrap(),
            4,
            &CancellationToken::new(),
        )
        .unwrap();
    }

    let mut source_writer =
        LiveWriter::open(&source_files.main, budget(), &CancellationToken::new()).unwrap();
    let mut source_transaction = source_writer
        .begin_membership_transaction(&iprange_livedb::CancellationToken::new())
        .unwrap();
    let mut source_membership = source_transaction.empty_membership().unwrap();
    for index in 0..70 {
        let feed = source_transaction
            .ensure_feed(FeedName::new(&format!("source-{index:03}")).unwrap())
            .unwrap();
        if [0, 63, 64, 69].contains(&index) {
            source_membership = source_transaction
                .add_feed(source_membership, feed)
                .unwrap();
        }
    }
    source_transaction
        .apply_v4(
            Ipv4Key(7),
            Ipv4Key(7),
            source_membership,
            MembershipOperation::Union,
        )
        .unwrap();
    source_transaction.commit().unwrap();
    source_writer.close().unwrap();

    let mut writer =
        LiveWriter::open(&destination_files.main, budget(), &CancellationToken::new()).unwrap();
    let mut destination_transaction = writer
        .begin_membership_transaction(&iprange_livedb::CancellationToken::new())
        .unwrap();
    let mut destination_membership = destination_transaction.empty_membership().unwrap();
    for index in 0..70 {
        let feed = destination_transaction
            .ensure_feed(FeedName::new(&format!("destination-{index:03}")).unwrap())
            .unwrap();
        if index == 0 {
            destination_membership = destination_transaction
                .add_feed(destination_membership, feed)
                .unwrap();
        }
    }
    for index in (0..70).rev() {
        destination_transaction
            .ensure_feed(FeedName::new(&format!("source-{index:03}")).unwrap())
            .unwrap();
    }
    destination_transaction
        .apply_v4(
            Ipv4Key(10),
            Ipv4Key(10),
            destination_membership,
            MembershipOperation::Union,
        )
        .unwrap();
    destination_transaction.commit().unwrap();

    let mut source = LiveReader::open(&source_files.main, &CancellationToken::new()).unwrap();
    let cancellation = CancellationToken::new();
    let prepared = match writer
        .begin_membership_import(MembershipImportSource::Live(&source), &cancellation)
        .unwrap()
        .finish_input()
        .unwrap()
    {
        FinishedWorkflow::Changed(prepared) => prepared,
        FinishedWorkflow::NoChange(_) => panic!("wide import unexpectedly did nothing"),
    };
    assert_eq!(prepared.report().source_feed_count, 70);
    assert_eq!(prepared.report().matched_feed_count, 70);
    assert_eq!(prepared.report().created_feed_count, 0);
    assert_eq!(prepared.report().source_distinct_membership_count, 1);
    assert_eq!(prepared.report().translated_membership_count, 1);
    prepared.commit().unwrap();
    source.close().unwrap();
    writer.close().unwrap();

    let mut reader = LiveReader::open(&destination_files.main, &CancellationToken::new()).unwrap();
    let imported = reader.lookup_membership_v4(Ipv4Key(7)).unwrap().unwrap();
    for index in 0..70 {
        let feed = reader
            .lookup_feed(&format!("source-{index:03}"))
            .unwrap()
            .unwrap();
        assert_eq!(
            imported.contains_index(feed.index).unwrap(),
            [0, 63, 64, 69].contains(&index)
        );
    }
    let preserved = reader.lookup_membership_v4(Ipv4Key(10)).unwrap().unwrap();
    let destination_only = reader.lookup_feed("destination-000").unwrap().unwrap();
    assert!(preserved.contains_index(destination_only.index).unwrap());
    reader.close().unwrap();
}
