#![cfg(any(target_os = "linux", target_vendor = "apple", target_os = "windows"))]

use std::fs;
use std::path::PathBuf;
use std::time::{SystemTime, UNIX_EPOCH};

use iprange_livedb::{
    create_live, AddressFamily, AddressRange, AlgebraOutputBudget, AlgebraOutputMode,
    AlgebraSetOperation, CancellationToken, Cardinality129, FeedName, FeedSelection,
    FinishedWorkflow, ImmutableReader, Ipv6Key, LiveReader, LiveWriter, MembershipAlgebra,
    MembershipAlgebraBudget, MembershipQueryBudget, PublicationPolicy, TransactionBudget,
    ValueKind, ValueTag,
};

struct Files(Vec<PathBuf>);

impl Files {
    fn path(&mut self, label: &str) -> PathBuf {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let path = std::env::temp_dir().join(format!(
            "iprange-v4-algebra-v6-{label}-{}-{unique}",
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

#[test]
fn global_algebra_counts_and_publishes_the_full_ipv6_space_exactly() {
    let cancellation = CancellationToken::new();
    let mut files = Files(Vec::new());
    let left_path = files.path("left");
    let right_path = files.path("right");
    let output_path = files.path("output");
    for path in [&left_path, &right_path] {
        create_live(
            path,
            AddressFamily::Ipv6,
            ValueKind::Membership,
            iprange_livedb::StructureKind::None,
            ValueTag::new(b"feeds").unwrap(),
            1,
            &cancellation,
        )
        .unwrap();
    }
    let mut left_writer =
        LiveWriter::open(&left_path, transaction_budget(), &cancellation).unwrap();
    add_full(&mut left_writer, "x", &cancellation);
    left_writer.close().unwrap();
    let mut right_writer =
        LiveWriter::open(&right_path, transaction_budget(), &cancellation).unwrap();
    add_full(&mut right_writer, "x", &cancellation);
    add_full(&mut right_writer, "y", &cancellation);
    right_writer.close().unwrap();

    let mut left_reader = LiveReader::open(&left_path, &cancellation).unwrap();
    let mut right_reader = LiveReader::open(&right_path, &cancellation).unwrap();
    let left_scope = left_reader
        .membership_query()
        .unwrap()
        .all_feeds(
            MembershipQueryBudget {
                max_heap_bytes: 1024 * 1024,
            },
            &cancellation,
        )
        .unwrap();
    let right_scope = right_reader
        .membership_query()
        .unwrap()
        .all_feeds(
            MembershipQueryBudget {
                max_heap_bytes: 1024 * 1024,
            },
            &cancellation,
        )
        .unwrap();
    let scopes = [&left_scope, &right_scope];
    let algebra = MembershipAlgebra::new(
        &scopes,
        MembershipAlgebraBudget {
            max_heap_bytes: 4 * 1024 * 1024,
            max_sources: 2,
        },
        &cancellation,
    )
    .unwrap();
    assert_eq!(
        algebra
            .count(FeedSelection::All, &cancellation)
            .unwrap()
            .addresses,
        Cardinality129::FULL_IPV6_SPACE
    );
    let x = [FeedName::new("x").unwrap()];
    let y = [FeedName::new("y").unwrap()];
    let comparison = algebra
        .compare(
            FeedSelection::Named(&x),
            FeedSelection::Named(&y),
            &cancellation,
        )
        .unwrap();
    assert!(comparison.equal);
    assert_eq!(
        comparison.overlap_addresses,
        Cardinality129::FULL_IPV6_SPACE
    );

    let selected = [x[0], y[0]];
    let result = algebra
        .publish_set(
            &output_path,
            ValueTag::new(b"intersection").unwrap(),
            AlgebraSetOperation::Intersection(FeedSelection::Named(&selected)),
            AlgebraOutputMode::PreserveFeeds,
            None,
            PublicationPolicy::FailIfExists,
            AlgebraOutputBudget {
                max_output_pages: 10_000,
                max_open_files: 3,
            },
            &cancellation,
        )
        .unwrap();
    assert_eq!(result.report.output_range_count, 1);
    assert_eq!(
        result.report.output_addresses,
        Cardinality129::FULL_IPV6_SPACE
    );
    let output = ImmutableReader::open(&output_path).unwrap();
    let mut names = Vec::new();
    output
        .membership_query()
        .unwrap()
        .matching_feeds_v6(
            Ipv6Key::MAX,
            &mut |name: FeedName| {
                names.push(name.as_str().to_owned());
                Ok(())
            },
            &cancellation,
        )
        .unwrap();
    assert_eq!(names, ["x", "y"]);

    drop(algebra);
    drop(right_scope);
    drop(left_scope);
    right_reader.close().unwrap();
    left_reader.close().unwrap();
}

fn add_full(writer: &mut LiveWriter, name: &str, cancellation: &CancellationToken) {
    let mut feed = writer
        .begin_create_feed(FeedName::new(name).unwrap(), cancellation)
        .unwrap();
    feed.add_ranges_v6_slice(&[AddressRange {
        from: Ipv6Key::MIN,
        to: Ipv6Key::MAX,
    }])
    .unwrap();
    match feed.finish_input().unwrap() {
        FinishedWorkflow::Changed(prepared) => {
            prepared.commit().unwrap();
        }
        FinishedWorkflow::NoChange(report) => panic!("expected change: {report:?}"),
    }
}
