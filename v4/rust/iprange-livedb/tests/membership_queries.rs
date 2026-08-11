#![cfg(any(target_os = "linux", target_vendor = "apple", target_os = "windows"))]

use std::fs;
use std::path::PathBuf;
use std::time::{SystemTime, UNIX_EPOCH};

use iprange_livedb::{
    create_live, AddressFamily, AddressRange, CancellationToken, Cardinality129, FeedCardinality,
    FeedName, FeedOverlap, FeedPair, FinishedWorkflow, Ipv4Key, Ipv6Key, LiveReader, LiveWriter,
    MembershipAggregateSink, MembershipAggregationMode, MembershipQueryBudget, TransactionBudget,
    ValueKind, ValueTag,
};

struct File {
    main: PathBuf,
}

impl File {
    fn new(label: &str) -> Self {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        Self {
            main: std::env::temp_dir().join(format!(
                "iprange-v4-query-{label}-{}-{unique}",
                std::process::id()
            )),
        }
    }
}

impl Drop for File {
    fn drop(&mut self) {
        let _ = fs::remove_file(&self.main);
        let mut sidecar = self.main.file_name().unwrap().to_os_string();
        sidecar.push(".readers");
        let _ = fs::remove_file(self.main.with_file_name(sidecar));
    }
}

fn transaction_budget() -> TransactionBudget {
    TransactionBudget {
        max_heap_bytes: 2 * 1024 * 1024,
        max_private_pages: 20_000,
        max_file_growth_pages: 20_000,
        max_open_files: 2,
    }
}

fn query_budget() -> MembershipQueryBudget {
    MembershipQueryBudget {
        max_heap_bytes: 4 * 1024 * 1024,
    }
}

fn create_feed_v4(writer: &mut LiveWriter, name: &str, ranges: &[AddressRange<Ipv4Key>]) {
    let cancellation = CancellationToken::new();
    let mut operation = writer
        .begin_create_feed(FeedName::new(name).unwrap(), &cancellation)
        .unwrap();
    operation.add_ranges_v4_slice(ranges).unwrap();
    match operation.finish_input().unwrap() {
        FinishedWorkflow::Changed(prepared) => {
            prepared.commit().unwrap();
        }
        FinishedWorkflow::NoChange(_) => panic!("new feed did not change catalog"),
    }
}

#[derive(Default)]
struct Collector {
    feeds: Vec<FeedCardinality>,
    pairs: Vec<FeedOverlap>,
}

impl MembershipAggregateSink for Collector {
    fn feed_cardinalities(&mut self, batch: &[FeedCardinality]) -> iprange_livedb::Result<()> {
        self.feeds.extend_from_slice(batch);
        Ok(())
    }

    fn feed_overlaps(&mut self, batch: &[FeedOverlap]) -> iprange_livedb::Result<()> {
        self.pairs.extend_from_slice(batch);
        Ok(())
    }
}

#[test]
fn point_names_and_all_pair_aggregation_are_exact() {
    let file = File::new("all");
    let cancellation = CancellationToken::new();
    create_live(
        &file.main,
        AddressFamily::Ipv4,
        ValueKind::Membership,
        iprange_livedb::StructureKind::None,
        ValueTag::new(b"feeds").unwrap(),
        1,
        &cancellation,
    )
    .unwrap();
    let mut writer = LiveWriter::open(&file.main, transaction_budget(), &cancellation).unwrap();
    create_feed_v4(
        &mut writer,
        "a",
        &[
            AddressRange {
                from: Ipv4Key(0),
                to: Ipv4Key(9),
            },
            AddressRange {
                from: Ipv4Key(20),
                to: Ipv4Key(29),
            },
        ],
    );
    create_feed_v4(
        &mut writer,
        "b",
        &[AddressRange {
            from: Ipv4Key(5),
            to: Ipv4Key(24),
        }],
    );
    create_feed_v4(
        &mut writer,
        "c",
        &[
            AddressRange {
                from: Ipv4Key(8),
                to: Ipv4Key(12),
            },
            AddressRange {
                from: Ipv4Key(27),
                to: Ipv4Key(35),
            },
        ],
    );
    create_feed_v4(&mut writer, "empty", &[]);
    writer.close().unwrap();

    let mut reader = LiveReader::open(&file.main, &cancellation).unwrap();
    let query = reader.membership_query().unwrap();
    let mut names = Vec::new();
    let report = query
        .matching_feeds_v4(
            Ipv4Key(8),
            &mut |name| {
                names.push(name);
                Ok(())
            },
            &cancellation,
        )
        .unwrap();
    assert_eq!(report.matching_feed_count, 3);
    assert_eq!(
        names.iter().map(FeedName::as_str).collect::<Vec<_>>(),
        ["a", "b", "c"]
    );
    names.clear();
    assert_eq!(
        query
            .matching_feeds_v4(
                Ipv4Key(100),
                &mut |name| {
                    names.push(name);
                    Ok(())
                },
                &cancellation
            )
            .unwrap()
            .matching_feed_count,
        0
    );

    {
        let scope = query.all_feeds(query_budget(), &cancellation).unwrap();
        assert_eq!(scope.feed_count(), 4);
        let mut output = Collector::default();
        let report = scope
            .aggregate(
                MembershipAggregationMode::AllPairs,
                &mut output,
                &cancellation,
            )
            .unwrap();
        assert_eq!(report.scanned_addresses, Cardinality129::from_u64(36));
        assert_eq!(report.feed_result_count, 4);
        assert_eq!(report.pair_result_count, 6);
        assert_eq!(
            output
                .feeds
                .iter()
                .map(|result| (result.feed.as_str(), result.addresses))
                .collect::<Vec<_>>(),
            [
                ("a", Cardinality129::from_u64(20)),
                ("b", Cardinality129::from_u64(20)),
                ("c", Cardinality129::from_u64(14)),
                ("empty", Cardinality129::ZERO),
            ]
        );
        assert_eq!(pair(&output, "a", "b"), Cardinality129::from_u64(10));
        assert_eq!(pair(&output, "a", "c"), Cardinality129::from_u64(5));
        assert_eq!(pair(&output, "b", "c"), Cardinality129::from_u64(5));
        assert_eq!(pair(&output, "a", "empty"), Cardinality129::ZERO);
        assert_eq!(pair(&output, "b", "empty"), Cardinality129::ZERO);
        assert_eq!(pair(&output, "c", "empty"), Cardinality129::ZERO);
    }
    reader.close().unwrap();
}

#[test]
fn named_target_and_selected_pair_modes_keep_indexes_inside_the_sdk() {
    let file = File::new("selected");
    let cancellation = CancellationToken::new();
    create_live(
        &file.main,
        AddressFamily::Ipv4,
        ValueKind::Membership,
        iprange_livedb::StructureKind::None,
        ValueTag::new(b"feeds").unwrap(),
        1,
        &cancellation,
    )
    .unwrap();
    let mut writer = LiveWriter::open(&file.main, transaction_budget(), &cancellation).unwrap();
    create_feed_v4(
        &mut writer,
        "a",
        &[AddressRange {
            from: Ipv4Key(0),
            to: Ipv4Key(9),
        }],
    );
    create_feed_v4(
        &mut writer,
        "b",
        &[AddressRange {
            from: Ipv4Key(5),
            to: Ipv4Key(14),
        }],
    );
    create_feed_v4(
        &mut writer,
        "c",
        &[AddressRange {
            from: Ipv4Key(8),
            to: Ipv4Key(17),
        }],
    );
    writer.close().unwrap();

    let mut reader = LiveReader::open(&file.main, &cancellation).unwrap();
    let query = reader.membership_query().unwrap();
    let scope = query
        .named_feeds(
            &[
                FeedName::new("c").unwrap(),
                FeedName::new("a").unwrap(),
                FeedName::new("b").unwrap(),
            ],
            query_budget(),
            &cancellation,
        )
        .unwrap();

    let mut target = Collector::default();
    scope
        .aggregate(
            MembershipAggregationMode::TargetAgainstScope(FeedName::new("b").unwrap()),
            &mut target,
            &cancellation,
        )
        .unwrap();
    assert_eq!(target.pairs.len(), 2);
    assert_eq!(pair(&target, "a", "b"), Cardinality129::from_u64(5));
    assert_eq!(pair(&target, "b", "c"), Cardinality129::from_u64(7));

    let requested = [FeedPair {
        left: FeedName::new("c").unwrap(),
        right: FeedName::new("a").unwrap(),
    }];
    let mut selected = Collector::default();
    let report = scope
        .aggregate(
            MembershipAggregationMode::SelectedPairs(&requested),
            &mut selected,
            &cancellation,
        )
        .unwrap();
    assert_eq!(report.pair_result_count, 1);
    assert_eq!(pair(&selected, "a", "c"), Cardinality129::from_u64(2));

    let duplicates = [
        requested[0],
        FeedPair {
            left: requested[0].right,
            right: requested[0].left,
        },
    ];
    assert!(scope
        .aggregate(
            MembershipAggregationMode::SelectedPairs(&duplicates),
            &mut Collector::default(),
            &cancellation,
        )
        .is_err());
    assert!(query
        .named_feeds(
            &[FeedName::new("a").unwrap(), FeedName::new("a").unwrap()],
            query_budget(),
            &cancellation,
        )
        .is_err());
    assert!(query
        .all_feeds(MembershipQueryBudget { max_heap_bytes: 1 }, &cancellation,)
        .is_err());
    drop(scope);
    reader.close().unwrap();
}

#[test]
fn full_ipv6_cardinality_and_overlap_do_not_wrap() {
    let file = File::new("v6");
    let cancellation = CancellationToken::new();
    create_live(
        &file.main,
        AddressFamily::Ipv6,
        ValueKind::Membership,
        iprange_livedb::StructureKind::None,
        ValueTag::new(b"feeds").unwrap(),
        1,
        &cancellation,
    )
    .unwrap();
    let mut writer = LiveWriter::open(&file.main, transaction_budget(), &cancellation).unwrap();
    for name in ["all-a", "all-b"] {
        let mut operation = writer
            .begin_create_feed(FeedName::new(name).unwrap(), &cancellation)
            .unwrap();
        operation
            .add_ranges_v6_slice(&[AddressRange {
                from: Ipv6Key::MIN,
                to: Ipv6Key::MAX,
            }])
            .unwrap();
        match operation.finish_input().unwrap() {
            FinishedWorkflow::Changed(prepared) => {
                prepared.commit().unwrap();
            }
            FinishedWorkflow::NoChange(_) => panic!("new feed did not change catalog"),
        }
    }
    writer.close().unwrap();

    let mut reader = LiveReader::open(&file.main, &cancellation).unwrap();
    let scope = reader
        .membership_query()
        .unwrap()
        .all_feeds(query_budget(), &cancellation)
        .unwrap();
    let mut output = Collector::default();
    let report = scope
        .aggregate(
            MembershipAggregationMode::AllPairs,
            &mut output,
            &cancellation,
        )
        .unwrap();
    assert_eq!(report.scanned_addresses, Cardinality129::FULL_IPV6_SPACE);
    assert!(output
        .feeds
        .iter()
        .all(|result| result.addresses == Cardinality129::FULL_IPV6_SPACE));
    assert_eq!(output.pairs[0].addresses, Cardinality129::FULL_IPV6_SPACE);
    drop(scope);
    reader.close().unwrap();
}

fn pair(output: &Collector, left: &str, right: &str) -> Cardinality129 {
    output
        .pairs
        .iter()
        .find(|result| result.left.as_str() == left && result.right.as_str() == right)
        .unwrap()
        .addresses
}
