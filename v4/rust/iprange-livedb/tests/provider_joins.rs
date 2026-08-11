#![cfg(any(target_os = "linux", target_vendor = "apple", target_os = "windows"))]

use std::fs;
use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use iprange_livedb::{
    create_live, AddressFamily, AddressRange, CancellationToken, Cardinality129, DirectJoinBudget,
    DirectJoinCell, DirectJoinSink, DirectJoinSource, DirectRange, FeedName, FinishedWorkflow,
    Ipv4Key, Ipv6Key, LiveReader, LiveWriter, MembershipCrossCell, MembershipJoinSink,
    MembershipQueryBudget, TransactionBudget, UncoveredFeed, UncoveredSide, ValueKind, ValueTag,
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
            "iprange-v4-join-{label}-{}-{unique}",
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
        max_heap_bytes: 4 * 1024 * 1024,
    }
}

fn create(path: &Path, family: AddressFamily, kind: ValueKind, tag: ValueTag) {
    create_live(
        path,
        family,
        kind,
        iprange_livedb::StructureKind::None,
        tag,
        1,
        &CancellationToken::new(),
    )
    .unwrap();
}

fn commit(finished: FinishedWorkflow<'_>) {
    match finished {
        FinishedWorkflow::Changed(prepared) => {
            prepared.commit().unwrap();
        }
        FinishedWorkflow::NoChange(report) => panic!("expected a change: {report:?}"),
    }
}

fn add_feed_v4(writer: &mut LiveWriter, name: &str, ranges: &[(u32, u32)]) {
    let ranges: Vec<_> = ranges
        .iter()
        .map(|&(from, to)| AddressRange {
            from: Ipv4Key(from),
            to: Ipv4Key(to),
        })
        .collect();
    let cancellation = CancellationToken::new();
    let mut feed = writer
        .begin_create_feed(FeedName::new(name).unwrap(), &cancellation)
        .unwrap();
    feed.add_ranges_v4_slice(&ranges).unwrap();
    commit(feed.finish_input().unwrap());
}

#[derive(Default)]
struct DirectOutput(Vec<DirectJoinCell>);

impl DirectJoinSink for DirectOutput {
    fn direct_join_cells(&mut self, batch: &[DirectJoinCell]) -> iprange_livedb::Result<()> {
        self.0.extend_from_slice(batch);
        Ok(())
    }
}

#[derive(Default)]
struct MembershipOutput {
    cross: Vec<MembershipCrossCell>,
    uncovered: Vec<UncoveredFeed>,
}

impl MembershipJoinSink for MembershipOutput {
    fn membership_cross_cells(
        &mut self,
        batch: &[MembershipCrossCell],
    ) -> iprange_livedb::Result<()> {
        self.cross.extend_from_slice(batch);
        Ok(())
    }

    fn uncovered_feeds(&mut self, batch: &[UncoveredFeed]) -> iprange_livedb::Result<()> {
        self.uncovered.extend_from_slice(batch);
        Ok(())
    }
}

#[test]
fn ordered_provider_joins_are_exact_and_bounded() {
    let mut files = Files::new();
    let membership_path = files.path("membership");
    let provider_path = files.path("direct");
    let other_path = files.path("other");
    let cancellation = CancellationToken::new();

    create(
        &membership_path,
        AddressFamily::Ipv4,
        ValueKind::Membership,
        ValueTag::new(b"feeds").unwrap(),
    );
    let mut writer =
        LiveWriter::open(&membership_path, transaction_budget(), &cancellation).unwrap();
    add_feed_v4(&mut writer, "a", &[(0, 19)]);
    add_feed_v4(&mut writer, "b", &[(10, 29)]);
    writer.close().unwrap();

    create(
        &provider_path,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::new(b"asn").unwrap(),
    );
    let mut writer = LiveWriter::open(&provider_path, transaction_budget(), &cancellation).unwrap();
    let mut replacement = writer.begin_direct_replacement(&cancellation).unwrap();
    replacement
        .add_ranges_v4_slice(&[
            DirectRange {
                from: Ipv4Key(5),
                to: Ipv4Key(14),
                value: 100,
            },
            DirectRange {
                from: Ipv4Key(20),
                to: Ipv4Key(24),
                value: 200,
            },
        ])
        .unwrap();
    commit(replacement.finish_input().unwrap());
    writer.close().unwrap();

    create(
        &other_path,
        AddressFamily::Ipv4,
        ValueKind::Membership,
        ValueTag::new(b"providers").unwrap(),
    );
    let mut writer = LiveWriter::open(&other_path, transaction_budget(), &cancellation).unwrap();
    add_feed_v4(&mut writer, "x", &[(5, 14)]);
    add_feed_v4(&mut writer, "y", &[(20, 24)]);
    writer.close().unwrap();

    let mut membership = LiveReader::open(&membership_path, &cancellation).unwrap();
    let mut provider = LiveReader::open(&provider_path, &cancellation).unwrap();
    let mut other = LiveReader::open(&other_path, &cancellation).unwrap();
    let left = membership
        .membership_query()
        .unwrap()
        .all_feeds(query_budget(), &cancellation)
        .unwrap();

    let mut direct = DirectOutput::default();
    let report = left
        .join_direct(
            DirectJoinSource::Live(&provider),
            DirectJoinBudget {
                max_result_cells: 5,
            },
            &mut direct,
            &cancellation,
        )
        .unwrap();
    assert_eq!(report.membership_range_count, 3);
    assert_eq!(report.direct_ranges_visited, 2);
    assert_eq!(report.joined_segment_count, 6);
    assert_eq!(report.selected_addresses, Cardinality129::from_u64(30));
    assert_eq!(report.mapped_addresses, Cardinality129::from_u64(15));
    assert_eq!(report.unmapped_addresses, Cardinality129::from_u64(15));
    assert_eq!(report.result_cell_count, 5);
    assert_eq!(direct_cell(&direct, "a", None), 10);
    assert_eq!(direct_cell(&direct, "a", Some(100)), 10);
    assert_eq!(direct_cell(&direct, "b", None), 10);
    assert_eq!(direct_cell(&direct, "b", Some(100)), 5);
    assert_eq!(direct_cell(&direct, "b", Some(200)), 5);

    assert!(left
        .join_direct(
            DirectJoinSource::Live(&provider),
            DirectJoinBudget {
                max_result_cells: 4,
            },
            &mut DirectOutput::default(),
            &cancellation,
        )
        .is_err());

    let right = other
        .membership_query()
        .unwrap()
        .all_feeds(query_budget(), &cancellation)
        .unwrap();
    let mut cross = MembershipOutput::default();
    let report = left
        .join_membership(&right, &mut cross, &cancellation)
        .unwrap();
    assert_eq!(report.left_range_count, 3);
    assert_eq!(report.right_range_count, 2);
    assert_eq!(report.joined_segment_count, 6);
    assert_eq!(report.left_addresses, Cardinality129::from_u64(30));
    assert_eq!(report.right_addresses, Cardinality129::from_u64(15));
    assert_eq!(report.overlap_addresses, Cardinality129::from_u64(15));
    assert_eq!(
        report.left_uncovered_addresses,
        Cardinality129::from_u64(15)
    );
    assert_eq!(report.right_uncovered_addresses, Cardinality129::ZERO);
    assert_eq!(cross_cell(&cross, "a", "x"), 10);
    assert_eq!(cross_cell(&cross, "a", "y"), 0);
    assert_eq!(cross_cell(&cross, "b", "x"), 5);
    assert_eq!(cross_cell(&cross, "b", "y"), 5);
    assert_eq!(uncovered(&cross, UncoveredSide::Left, "a"), 10);
    assert_eq!(uncovered(&cross, UncoveredSide::Left, "b"), 10);
    assert_eq!(uncovered(&cross, UncoveredSide::Right, "x"), 0);
    assert_eq!(uncovered(&cross, UncoveredSide::Right, "y"), 0);

    drop(right);
    drop(left);
    other.close().unwrap();
    provider.close().unwrap();
    membership.close().unwrap();
}

#[test]
fn membership_join_ignores_unselected_range_boundaries() {
    let mut files = Files::new();
    let left_path = files.path("selected-left");
    let right_path = files.path("selected-right");
    let cancellation = CancellationToken::new();

    for path in [&left_path, &right_path] {
        create(
            path,
            AddressFamily::Ipv4,
            ValueKind::Membership,
            ValueTag::new(b"feeds").unwrap(),
        );
    }
    let mut left_writer =
        LiveWriter::open(&left_path, transaction_budget(), &cancellation).unwrap();
    add_feed_v4(&mut left_writer, "selected", &[(0, 9), (30, 39)]);
    add_feed_v4(&mut left_writer, "noise-a", &[(10, 19)]);
    add_feed_v4(&mut left_writer, "noise-b", &[(20, 29)]);
    left_writer.close().unwrap();

    let mut right_writer =
        LiveWriter::open(&right_path, transaction_budget(), &cancellation).unwrap();
    add_feed_v4(&mut right_writer, "provider", &[(0, 39)]);
    right_writer.close().unwrap();

    let mut left_reader = LiveReader::open(&left_path, &cancellation).unwrap();
    let mut right_reader = LiveReader::open(&right_path, &cancellation).unwrap();
    let left = left_reader
        .membership_query()
        .unwrap()
        .named_feeds(
            &[FeedName::new("selected").unwrap()],
            query_budget(),
            &cancellation,
        )
        .unwrap();
    let right = right_reader
        .membership_query()
        .unwrap()
        .named_feeds(
            &[FeedName::new("provider").unwrap()],
            query_budget(),
            &cancellation,
        )
        .unwrap();

    let mut output = MembershipOutput::default();
    let report = left
        .join_membership(&right, &mut output, &cancellation)
        .unwrap();
    assert_eq!(report.left_range_count, 4);
    assert_eq!(report.right_range_count, 1);
    assert_eq!(report.joined_segment_count, 3);
    assert_eq!(report.overlap_addresses, Cardinality129::from_u64(20));
    assert_eq!(
        report.right_uncovered_addresses,
        Cardinality129::from_u64(20)
    );
    assert_eq!(cross_cell(&output, "selected", "provider"), 20);
    assert_eq!(uncovered(&output, UncoveredSide::Right, "provider"), 20);

    drop(right);
    drop(left);
    right_reader.close().unwrap();
    left_reader.close().unwrap();
}

#[test]
fn provider_joins_coalesce_adjacent_equal_selected_memberships() {
    let mut files = Files::new();
    let left_path = files.path("coalesced-left");
    let right_path = files.path("coalesced-right");
    let direct_path = files.path("coalesced-direct");
    let cancellation = CancellationToken::new();

    for path in [&left_path, &right_path] {
        create(
            path,
            AddressFamily::Ipv4,
            ValueKind::Membership,
            ValueTag::new(b"feeds").unwrap(),
        );
    }
    let mut left_writer =
        LiveWriter::open(&left_path, transaction_budget(), &cancellation).unwrap();
    add_feed_v4(&mut left_writer, "selected", &[(0, 39)]);
    add_feed_v4(&mut left_writer, "noise-a", &[(10, 19)]);
    add_feed_v4(&mut left_writer, "noise-b", &[(20, 29)]);
    left_writer.close().unwrap();

    let mut right_writer =
        LiveWriter::open(&right_path, transaction_budget(), &cancellation).unwrap();
    add_feed_v4(&mut right_writer, "provider", &[(0, 39)]);
    right_writer.close().unwrap();

    create(
        &direct_path,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::new(b"asn").unwrap(),
    );
    let mut direct_writer =
        LiveWriter::open(&direct_path, transaction_budget(), &cancellation).unwrap();
    let mut replacement = direct_writer
        .begin_direct_replacement(&cancellation)
        .unwrap();
    replacement
        .add_ranges_v4_slice(&[DirectRange {
            from: Ipv4Key(0),
            to: Ipv4Key(39),
            value: 64512,
        }])
        .unwrap();
    commit(replacement.finish_input().unwrap());
    direct_writer.close().unwrap();

    let mut left_reader = LiveReader::open(&left_path, &cancellation).unwrap();
    let mut right_reader = LiveReader::open(&right_path, &cancellation).unwrap();
    let mut direct_reader = LiveReader::open(&direct_path, &cancellation).unwrap();
    let left = left_reader
        .membership_query()
        .unwrap()
        .named_feeds(
            &[FeedName::new("selected").unwrap()],
            query_budget(),
            &cancellation,
        )
        .unwrap();
    let right = right_reader
        .membership_query()
        .unwrap()
        .named_feeds(
            &[FeedName::new("provider").unwrap()],
            query_budget(),
            &cancellation,
        )
        .unwrap();

    let mut membership_output = MembershipOutput::default();
    let membership_report = left
        .join_membership(&right, &mut membership_output, &cancellation)
        .unwrap();
    assert_eq!(membership_report.left_range_count, 4);
    assert_eq!(membership_report.right_range_count, 1);
    assert_eq!(membership_report.joined_segment_count, 1);
    assert_eq!(cross_cell(&membership_output, "selected", "provider"), 40);

    let mut direct_output = DirectOutput::default();
    let direct_report = left
        .join_direct(
            DirectJoinSource::Live(&direct_reader),
            DirectJoinBudget {
                max_result_cells: 1,
            },
            &mut direct_output,
            &cancellation,
        )
        .unwrap();
    assert_eq!(direct_report.membership_range_count, 4);
    assert_eq!(direct_report.direct_ranges_visited, 1);
    assert_eq!(direct_report.joined_segment_count, 1);
    assert_eq!(direct_cell(&direct_output, "selected", Some(64512)), 40);

    drop(right);
    drop(left);
    direct_reader.close().unwrap();
    right_reader.close().unwrap();
    left_reader.close().unwrap();
}

#[test]
fn full_ipv6_provider_joins_do_not_wrap() {
    let mut files = Files::new();
    let left_path = files.path("v6-left");
    let right_path = files.path("v6-right");
    let direct_path = files.path("v6-direct");
    let cancellation = CancellationToken::new();

    for path in [&left_path, &right_path] {
        create(
            path,
            AddressFamily::Ipv6,
            ValueKind::Membership,
            ValueTag::new(b"feeds").unwrap(),
        );
        let mut writer = LiveWriter::open(path, transaction_budget(), &cancellation).unwrap();
        let mut feed = writer
            .begin_create_feed(FeedName::new("all").unwrap(), &cancellation)
            .unwrap();
        feed.add_ranges_v6_slice(&[AddressRange {
            from: Ipv6Key::MIN,
            to: Ipv6Key::MAX,
        }])
        .unwrap();
        commit(feed.finish_input().unwrap());
        writer.close().unwrap();
    }
    create(
        &direct_path,
        AddressFamily::Ipv6,
        ValueKind::Direct,
        ValueTag::new(b"asn").unwrap(),
    );
    let mut writer = LiveWriter::open(&direct_path, transaction_budget(), &cancellation).unwrap();
    let mut replacement = writer.begin_direct_replacement(&cancellation).unwrap();
    replacement
        .add_ranges_v6_slice(&[DirectRange {
            from: Ipv6Key::MIN,
            to: Ipv6Key::MAX,
            value: u32::MAX,
        }])
        .unwrap();
    commit(replacement.finish_input().unwrap());
    writer.close().unwrap();

    let mut left_reader = LiveReader::open(&left_path, &cancellation).unwrap();
    let mut right_reader = LiveReader::open(&right_path, &cancellation).unwrap();
    let mut direct_reader = LiveReader::open(&direct_path, &cancellation).unwrap();
    let left = left_reader
        .membership_query()
        .unwrap()
        .all_feeds(query_budget(), &cancellation)
        .unwrap();
    let right = right_reader
        .membership_query()
        .unwrap()
        .all_feeds(query_budget(), &cancellation)
        .unwrap();

    let mut direct = DirectOutput::default();
    let direct_report = left
        .join_direct(
            DirectJoinSource::Live(&direct_reader),
            DirectJoinBudget {
                max_result_cells: 1,
            },
            &mut direct,
            &cancellation,
        )
        .unwrap();
    assert_eq!(
        direct_report.mapped_addresses,
        Cardinality129::FULL_IPV6_SPACE
    );
    assert_eq!(direct.0[0].direct_value, Some(u32::MAX));
    assert_eq!(direct.0[0].addresses, Cardinality129::FULL_IPV6_SPACE);

    let mut cross = MembershipOutput::default();
    let cross_report = left
        .join_membership(&right, &mut cross, &cancellation)
        .unwrap();
    assert_eq!(
        cross_report.overlap_addresses,
        Cardinality129::FULL_IPV6_SPACE
    );
    assert_eq!(cross.cross[0].addresses, Cardinality129::FULL_IPV6_SPACE);

    drop(right);
    drop(left);
    direct_reader.close().unwrap();
    right_reader.close().unwrap();
    left_reader.close().unwrap();
}

#[test]
fn provider_joins_match_an_independent_scalar_model() {
    const ADDRESSES: usize = 256;
    const LEFT_FEEDS: usize = 12;
    const RIGHT_FEEDS: usize = 9;

    let mut state = 0x7ac3_5b19_02d4_e681u64;
    let mut left_model = vec![[false; ADDRESSES]; LEFT_FEEDS];
    let mut right_model = vec![[false; ADDRESSES]; RIGHT_FEEDS];
    for feed in &mut left_model {
        for present in feed {
            *present = random(&mut state) % 5 < 2;
        }
    }
    for feed in &mut right_model {
        for present in feed {
            *present = random(&mut state) % 7 < 2;
        }
    }
    let mut direct_model = [None; ADDRESSES];
    for value in &mut direct_model {
        let draw = random(&mut state) % 8;
        *value = (draw >= 2).then_some((draw % 5) as u32);
    }

    let mut files = Files::new();
    let left_path = files.path("model-left");
    let right_path = files.path("model-right");
    let direct_path = files.path("model-direct");
    let cancellation = CancellationToken::new();
    for path in [&left_path, &right_path] {
        create(
            path,
            AddressFamily::Ipv4,
            ValueKind::Membership,
            ValueTag::new(b"feeds").unwrap(),
        );
    }
    let mut left_writer =
        LiveWriter::open(&left_path, transaction_budget(), &cancellation).unwrap();
    for (index, model) in left_model.iter().enumerate() {
        add_feed_v4(
            &mut left_writer,
            &format!("l{index:02}"),
            &boolean_ranges(model),
        );
    }
    left_writer.close().unwrap();
    let mut right_writer =
        LiveWriter::open(&right_path, transaction_budget(), &cancellation).unwrap();
    for (index, model) in right_model.iter().enumerate() {
        add_feed_v4(
            &mut right_writer,
            &format!("r{index:02}"),
            &boolean_ranges(model),
        );
    }
    right_writer.close().unwrap();

    create(
        &direct_path,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::new(b"provider").unwrap(),
    );
    let mut direct_writer =
        LiveWriter::open(&direct_path, transaction_budget(), &cancellation).unwrap();
    let mut replacement = direct_writer
        .begin_direct_replacement(&cancellation)
        .unwrap();
    replacement
        .add_ranges_v4_slice(&direct_ranges(&direct_model))
        .unwrap();
    commit(replacement.finish_input().unwrap());
    direct_writer.close().unwrap();

    let mut left_reader = LiveReader::open(&left_path, &cancellation).unwrap();
    let mut right_reader = LiveReader::open(&right_path, &cancellation).unwrap();
    let mut direct_reader = LiveReader::open(&direct_path, &cancellation).unwrap();
    let left = left_reader
        .membership_query()
        .unwrap()
        .all_feeds(query_budget(), &cancellation)
        .unwrap();
    let right = right_reader
        .membership_query()
        .unwrap()
        .all_feeds(query_budget(), &cancellation)
        .unwrap();

    let mut cross = MembershipOutput::default();
    let cross_report = left
        .join_membership(&right, &mut cross, &cancellation)
        .unwrap();
    for (left_index, left_values) in left_model.iter().enumerate() {
        for (right_index, right_values) in right_model.iter().enumerate() {
            let expected = left_values
                .iter()
                .zip(right_values)
                .filter(|&(left, right)| *left && *right)
                .count() as u64;
            assert_eq!(
                cross_cell(
                    &cross,
                    &format!("l{left_index:02}"),
                    &format!("r{right_index:02}")
                ),
                expected
            );
        }
    }
    let mut expected_overlap = 0u64;
    let mut expected_left_uncovered = 0u64;
    let mut expected_right_uncovered = 0u64;
    for address in 0..ADDRESSES {
        let any_left = left_model.iter().any(|feed| feed[address]);
        let any_right = right_model.iter().any(|feed| feed[address]);
        expected_overlap += u64::from(any_left && any_right);
        expected_left_uncovered += u64::from(any_left && !any_right);
        expected_right_uncovered += u64::from(any_right && !any_left);
    }
    assert_eq!(cross_report.overlap_addresses.lo(), expected_overlap);
    assert_eq!(
        cross_report.left_uncovered_addresses.lo(),
        expected_left_uncovered
    );
    assert_eq!(
        cross_report.right_uncovered_addresses.lo(),
        expected_right_uncovered
    );
    for (index, feed) in left_model.iter().enumerate() {
        let expected = (0..ADDRESSES)
            .filter(|&address| feed[address] && !right_model.iter().any(|right| right[address]))
            .count() as u64;
        assert_eq!(
            uncovered(&cross, UncoveredSide::Left, &format!("l{index:02}")),
            expected
        );
    }
    for (index, feed) in right_model.iter().enumerate() {
        let expected = (0..ADDRESSES)
            .filter(|&address| feed[address] && !left_model.iter().any(|left| left[address]))
            .count() as u64;
        assert_eq!(
            uncovered(&cross, UncoveredSide::Right, &format!("r{index:02}")),
            expected
        );
    }

    let mut direct = DirectOutput::default();
    let direct_report = left
        .join_direct(
            DirectJoinSource::Live(&direct_reader),
            DirectJoinBudget {
                max_result_cells: (LEFT_FEEDS * 6) as u64,
            },
            &mut direct,
            &cancellation,
        )
        .unwrap();
    let mut expected_mapped = 0u64;
    let mut expected_unmapped = 0u64;
    for address in 0..ADDRESSES {
        if left_model.iter().any(|feed| feed[address]) {
            if direct_model[address].is_some() {
                expected_mapped += 1;
            } else {
                expected_unmapped += 1;
            }
        }
    }
    assert_eq!(direct_report.mapped_addresses.lo(), expected_mapped);
    assert_eq!(direct_report.unmapped_addresses.lo(), expected_unmapped);
    for (feed_index, feed) in left_model.iter().enumerate() {
        for value in [None, Some(0), Some(1), Some(2), Some(3), Some(4)] {
            let expected = (0..ADDRESSES)
                .filter(|&address| feed[address] && direct_model[address] == value)
                .count() as u64;
            let actual = direct
                .0
                .iter()
                .find(|cell| {
                    cell.feed.as_str() == format!("l{feed_index:02}") && cell.direct_value == value
                })
                .map_or(0, |cell| cell.addresses.lo());
            assert_eq!(actual, expected);
        }
    }

    drop(right);
    drop(left);
    direct_reader.close().unwrap();
    right_reader.close().unwrap();
    left_reader.close().unwrap();
}

fn direct_cell(output: &DirectOutput, feed: &str, value: Option<u32>) -> u64 {
    output
        .0
        .iter()
        .find(|cell| cell.feed.as_str() == feed && cell.direct_value == value)
        .unwrap()
        .addresses
        .lo()
}

fn cross_cell(output: &MembershipOutput, left: &str, right: &str) -> u64 {
    output
        .cross
        .iter()
        .find(|cell| cell.left.as_str() == left && cell.right.as_str() == right)
        .unwrap()
        .addresses
        .lo()
}

fn uncovered(output: &MembershipOutput, side: UncoveredSide, feed: &str) -> u64 {
    output
        .uncovered
        .iter()
        .find(|cell| cell.side == side && cell.feed.as_str() == feed)
        .unwrap()
        .addresses
        .lo()
}

fn random(state: &mut u64) -> u64 {
    *state ^= *state << 13;
    *state ^= *state >> 7;
    *state ^= *state << 17;
    *state
}

fn boolean_ranges(values: &[bool]) -> Vec<(u32, u32)> {
    let mut ranges = Vec::new();
    let mut start = None;
    for (index, &present) in values.iter().chain(std::iter::once(&false)).enumerate() {
        match (start, present) {
            (None, true) => start = Some(index as u32),
            (Some(from), false) => {
                ranges.push((from, index as u32 - 1));
                start = None;
            }
            _ => {}
        }
    }
    ranges
}

fn direct_ranges(values: &[Option<u32>]) -> Vec<DirectRange<Ipv4Key>> {
    let mut ranges = Vec::new();
    let mut start = 0usize;
    while start < values.len() {
        let Some(value) = values[start] else {
            start += 1;
            continue;
        };
        let mut end = start;
        while end + 1 < values.len() && values[end + 1] == Some(value) {
            end += 1;
        }
        ranges.push(DirectRange {
            from: Ipv4Key(start as u32),
            to: Ipv4Key(end as u32),
            value,
        });
        start = end + 1;
    }
    ranges
}
