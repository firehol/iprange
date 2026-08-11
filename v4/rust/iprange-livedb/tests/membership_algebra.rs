#![cfg(any(target_os = "linux", target_vendor = "apple", target_os = "windows"))]

use std::fs;
use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use iprange_livedb::{
    create_live, AddressFamily, AddressRange, AlgebraOutputBudget, AlgebraOutputMode,
    AlgebraSetOperation, CancellationToken, Cardinality129, FeedName, FeedSelection,
    FinishedWorkflow, ImmutableReader, Ipv4Key, LiveReader, LiveWriter, MembershipAlgebra,
    MembershipAlgebraBudget, MembershipQueryBudget, PublicationPolicy, TransactionBudget,
    ValueKind, ValueTag,
};

struct Files(Vec<PathBuf>);

impl Files {
    fn new() -> Self {
        Self(Vec::new())
    }

    fn path(&mut self, label: &str) -> PathBuf {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let path = std::env::temp_dir().join(format!(
            "iprange-v4-algebra-{label}-{}-{unique}",
            std::process::id()
        ));
        self.0.push(path.clone());
        path
    }
}

impl Drop for Files {
    fn drop(&mut self) {
        for path in &self.0 {
            let _ = fs::remove_file(path);
            let mut sidecar = path.file_name().unwrap().to_os_string();
            sidecar.push(".readers");
            let _ = fs::remove_file(path.with_file_name(sidecar));
        }
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
        max_heap_bytes: 2 * 1024 * 1024,
    }
}

fn output_budget() -> AlgebraOutputBudget {
    AlgebraOutputBudget {
        max_output_pages: 20_000,
        max_open_files: 3,
    }
}

fn create_membership(path: &Path) {
    create_live(
        path,
        AddressFamily::Ipv4,
        ValueKind::Membership,
        iprange_livedb::StructureKind::None,
        ValueTag::new(b"feeds").unwrap(),
        1,
        &CancellationToken::new(),
    )
    .unwrap();
}

fn add_feed(writer: &mut LiveWriter, name: &str, ranges: &[(u32, u32)]) {
    let ranges: Vec<_> = ranges
        .iter()
        .map(|&(from, to)| AddressRange {
            from: Ipv4Key(from),
            to: Ipv4Key(to),
        })
        .collect();
    let cancellation = CancellationToken::new();
    let mut workflow = writer
        .begin_create_feed(FeedName::new(name).unwrap(), &cancellation)
        .unwrap();
    workflow.add_ranges_v4_slice(&ranges).unwrap();
    match workflow.finish_input().unwrap() {
        FinishedWorkflow::Changed(prepared) => {
            prepared.commit().unwrap();
        }
        FinishedWorkflow::NoChange(report) => panic!("expected new feed: {report:?}"),
    }
}

#[test]
fn same_names_merge_globally_and_all_set_outputs_are_exact_v4_files() {
    let mut files = Files::new();
    let a_path = files.path("a");
    let b_path = files.path("b");
    let union_path = files.path("union");
    let intersection_path = files.path("intersection");
    let exclusion_path = files.path("exclusion");
    let flat_path = files.path("flat");
    let cancellation = CancellationToken::new();

    create_membership(&a_path);
    let mut writer = LiveWriter::open(&a_path, transaction_budget(), &cancellation).unwrap();
    add_feed(&mut writer, "x", &[(0, 9)]);
    add_feed(&mut writer, "y", &[(20, 29)]);
    writer.close().unwrap();
    create_membership(&b_path);
    let mut writer = LiveWriter::open(&b_path, transaction_budget(), &cancellation).unwrap();
    add_feed(&mut writer, "y", &[(5, 14)]);
    add_feed(&mut writer, "z", &[(8, 22)]);
    writer.close().unwrap();

    let mut a = LiveReader::open(&a_path, &cancellation).unwrap();
    let mut b = LiveReader::open(&b_path, &cancellation).unwrap();
    let a_scope = a
        .membership_query()
        .unwrap()
        .all_feeds(query_budget(), &cancellation)
        .unwrap();
    let b_scope = b
        .membership_query()
        .unwrap()
        .all_feeds(query_budget(), &cancellation)
        .unwrap();
    let scopes = [&a_scope, &b_scope];
    assert!(MembershipAlgebra::new(
        &scopes,
        MembershipAlgebraBudget {
            max_heap_bytes: 8 * 1024 * 1024,
            max_sources: 1,
        },
        &cancellation,
    )
    .is_err());
    assert!(MembershipAlgebra::new(
        &scopes,
        MembershipAlgebraBudget {
            max_heap_bytes: 1,
            max_sources: 2,
        },
        &cancellation,
    )
    .is_err());
    let algebra = MembershipAlgebra::new(
        &scopes,
        MembershipAlgebraBudget {
            max_heap_bytes: 8 * 1024 * 1024,
            max_sources: 2,
        },
        &cancellation,
    )
    .unwrap();
    assert_eq!(
        algebra
            .feeds()
            .map(|name| name.as_str().to_owned())
            .collect::<Vec<_>>(),
        ["x", "y", "z"]
    );

    let y = [FeedName::new("y").unwrap()];
    let z = [FeedName::new("z").unwrap()];
    let duplicate_y = [y[0], y[0]];
    let missing = [FeedName::new("missing").unwrap()];
    assert!(algebra
        .count(FeedSelection::Named(&duplicate_y), &cancellation)
        .is_err());
    assert!(algebra
        .count(FeedSelection::Named(&missing), &cancellation)
        .is_err());
    assert_eq!(
        algebra
            .count(FeedSelection::Named(&y), &cancellation)
            .unwrap()
            .addresses,
        Cardinality129::from_u64(20)
    );
    let comparison = algebra
        .compare(
            FeedSelection::Named(&y),
            FeedSelection::Named(&z),
            &cancellation,
        )
        .unwrap();
    assert_eq!(comparison.left_addresses, Cardinality129::from_u64(20));
    assert_eq!(comparison.right_addresses, Cardinality129::from_u64(15));
    assert_eq!(comparison.overlap_addresses, Cardinality129::from_u64(10));
    assert_eq!(comparison.left_only_addresses, Cardinality129::from_u64(10));
    assert_eq!(comparison.right_only_addresses, Cardinality129::from_u64(5));
    assert_eq!(comparison.union_addresses, Cardinality129::from_u64(25));
    assert!(!comparison.equal);

    let union = algebra
        .publish_set(
            &union_path,
            ValueTag::new(b"union").unwrap(),
            AlgebraSetOperation::Union(FeedSelection::All),
            AlgebraOutputMode::PreserveFeeds,
            Some(b"{\"kind\":\"union\"}"),
            PublicationPolicy::FailIfExists,
            output_budget(),
            &cancellation,
        )
        .unwrap();
    assert_eq!(union.report.output_feed_count, 3);
    assert_eq!(union.report.output_addresses, Cardinality129::from_u64(30));
    assert_eq!(union.report.output_range_count, 7);
    let union_reader = ImmutableReader::open(&union_path).unwrap();
    assert_eq!(
        union_reader.metadata_json().unwrap().as_deref(),
        Some(&b"{\"kind\":\"union\"}"[..])
    );
    assert_names(&union_reader, 6, &["x", "y"]);
    assert_names(&union_reader, 9, &["x", "y", "z"]);
    assert_names(&union_reader, 18, &["z"]);
    assert_names(&union_reader, 21, &["y", "z"]);
    assert_names(&union_reader, 27, &["y"]);
    assert!(algebra
        .publish_set(
            &union_path,
            ValueTag::new(b"union").unwrap(),
            AlgebraSetOperation::Union(FeedSelection::All),
            AlgebraOutputMode::PreserveFeeds,
            None,
            PublicationPolicy::FailIfExists,
            output_budget(),
            &cancellation,
        )
        .is_err());
    assert_names(&union_reader, 9, &["x", "y", "z"]);

    let rejected_path = files.path("rejected");
    assert!(algebra
        .publish_set(
            &rejected_path,
            ValueTag::new(b"union").unwrap(),
            AlgebraSetOperation::Union(FeedSelection::All),
            AlgebraOutputMode::PreserveFeeds,
            None,
            PublicationPolicy::FailIfExists,
            AlgebraOutputBudget {
                max_output_pages: 1,
                max_open_files: 3,
            },
            &cancellation,
        )
        .is_err());
    assert!(!rejected_path.exists());
    let cancelled_path = files.path("cancelled");
    let cancelled = CancellationToken::new();
    cancelled.cancel();
    assert!(algebra
        .publish_set(
            &cancelled_path,
            ValueTag::new(b"union").unwrap(),
            AlgebraSetOperation::Union(FeedSelection::All),
            AlgebraOutputMode::PreserveFeeds,
            None,
            PublicationPolicy::FailIfExists,
            output_budget(),
            &cancelled,
        )
        .is_err());
    assert!(!cancelled_path.exists());

    let yz = [FeedName::new("y").unwrap(), FeedName::new("z").unwrap()];
    let intersection = algebra
        .publish_set(
            &intersection_path,
            ValueTag::new(b"intersection").unwrap(),
            AlgebraSetOperation::Intersection(FeedSelection::Named(&yz)),
            AlgebraOutputMode::PreserveFeeds,
            None,
            PublicationPolicy::FailIfExists,
            output_budget(),
            &cancellation,
        )
        .unwrap();
    assert_eq!(intersection.report.output_feed_count, 2);
    assert_eq!(
        intersection.report.output_addresses,
        Cardinality129::from_u64(10)
    );
    assert_eq!(intersection.report.output_range_count, 2);
    let intersection_reader = ImmutableReader::open(&intersection_path).unwrap();
    assert_names(&intersection_reader, 10, &["y", "z"]);
    assert_names(&intersection_reader, 18, &[]);
    assert_names(&intersection_reader, 21, &["y", "z"]);

    let exclusion = algebra
        .publish_set(
            &exclusion_path,
            ValueTag::new(b"exclude").unwrap(),
            AlgebraSetOperation::Exclusion {
                included: FeedSelection::Named(&y),
                excluded: FeedSelection::Named(&z),
            },
            AlgebraOutputMode::PreserveFeeds,
            None,
            PublicationPolicy::FailIfExists,
            output_budget(),
            &cancellation,
        )
        .unwrap();
    assert_eq!(exclusion.report.output_feed_count, 1);
    assert_eq!(
        exclusion.report.output_addresses,
        Cardinality129::from_u64(10)
    );
    assert_eq!(exclusion.report.output_range_count, 2);
    let exclusion_reader = ImmutableReader::open(&exclusion_path).unwrap();
    assert_names(&exclusion_reader, 6, &["y"]);
    assert_names(&exclusion_reader, 10, &[]);
    assert_names(&exclusion_reader, 25, &["y"]);

    let flat = algebra
        .publish_set(
            &flat_path,
            ValueTag::new(b"flat").unwrap(),
            AlgebraSetOperation::Union(FeedSelection::All),
            AlgebraOutputMode::Flat(FeedName::new("combined").unwrap()),
            None,
            PublicationPolicy::FailIfExists,
            output_budget(),
            &cancellation,
        )
        .unwrap();
    assert_eq!(flat.report.output_feed_count, 1);
    assert_eq!(flat.report.output_range_count, 1);
    assert_eq!(flat.report.output_addresses, Cardinality129::from_u64(30));
    let flat_reader = ImmutableReader::open(&flat_path).unwrap();
    assert_names(&flat_reader, 0, &["combined"]);
    assert_names(&flat_reader, 29, &["combined"]);
    assert_names(&flat_reader, 30, &[]);

    drop(algebra);
    drop(b_scope);
    drop(a_scope);
    b.close().unwrap();
    a.close().unwrap();
}

#[test]
fn algebra_ignores_boundaries_owned_only_by_unselected_feeds() {
    let mut files = Files::new();
    let source_path = files.path("selected-source");
    let output_path = files.path("selected-output");
    let cancellation = CancellationToken::new();

    create_membership(&source_path);
    let mut writer = LiveWriter::open(&source_path, transaction_budget(), &cancellation).unwrap();
    add_feed(&mut writer, "selected", &[(0, 39)]);
    add_feed(&mut writer, "noise-a", &[(10, 19)]);
    add_feed(&mut writer, "noise-b", &[(20, 29)]);
    writer.close().unwrap();

    let mut reader = LiveReader::open(&source_path, &cancellation).unwrap();
    let scope = reader
        .membership_query()
        .unwrap()
        .named_feeds(
            &[FeedName::new("selected").unwrap()],
            query_budget(),
            &cancellation,
        )
        .unwrap();
    let algebra = MembershipAlgebra::new(
        &[&scope],
        MembershipAlgebraBudget {
            max_heap_bytes: 8 * 1024 * 1024,
            max_sources: 1,
        },
        &cancellation,
    )
    .unwrap();

    let count = algebra.count(FeedSelection::All, &cancellation).unwrap();
    assert_eq!(count.source_range_count, 4);
    assert_eq!(count.joined_segment_count, 1);
    assert_eq!(count.addresses, Cardinality129::from_u64(40));

    let output = algebra
        .publish_set(
            &output_path,
            ValueTag::new(b"selected").unwrap(),
            AlgebraSetOperation::Union(FeedSelection::All),
            AlgebraOutputMode::PreserveFeeds,
            None,
            PublicationPolicy::FailIfExists,
            output_budget(),
            &cancellation,
        )
        .unwrap();
    assert_eq!(output.report.source_range_count, 4);
    assert_eq!(output.report.joined_segment_count, 1);
    assert_eq!(output.report.output_range_count, 1);
    assert_names(
        &ImmutableReader::open(&output_path).unwrap(),
        25,
        &["selected"],
    );

    drop(algebra);
    drop(scope);
    reader.close().unwrap();
}

fn assert_names(reader: &ImmutableReader, address: u32, expected: &[&str]) {
    let mut names = Vec::new();
    reader
        .membership_query()
        .unwrap()
        .matching_feeds_v4(
            Ipv4Key(address),
            &mut |name| {
                names.push(name);
                Ok(())
            },
            &CancellationToken::new(),
        )
        .unwrap();
    assert_eq!(
        names.iter().map(FeedName::as_str).collect::<Vec<_>>(),
        expected
    );
}
