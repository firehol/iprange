use std::fs::{self, OpenOptions};
use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use super::*;
use crate::cancellation::CancellationToken;
use crate::database::ImmutableReader;
use crate::key::IpKey;
use crate::range_cursor::RangeDirection;
use crate::range_tree;
use crate::slotted_page;
use crate::test_alloc::count_thread_allocations;
use crate::validation::{validate, ValidationBudget, ValidationMode, ValidationSinkControl};

struct TestPath(PathBuf);

impl TestPath {
    fn new(label: &str) -> Self {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        Self(std::env::temp_dir().join(format!(
            "iprange-v4-output-{label}-{}-{unique}",
            std::process::id()
        )))
    }
}

impl Drop for TestPath {
    fn drop(&mut self) {
        let _ = fs::remove_file(&self.0);
    }
}

struct Words(Vec<u64>);

impl MembershipWords for Words {
    fn word_count(&self) -> u32 {
        self.0.len() as u32
    }

    fn read_words(&self, start: u32, output: &mut [u64]) -> Result<()> {
        let start = start as usize;
        let end = start
            .checked_add(output.len())
            .ok_or(Error::ArithmeticOverflow("test membership words"))?;
        let source = self.0.get(start..end).ok_or(Error::InvalidArgument(
            "test membership read is outside bounds",
        ))?;
        output.copy_from_slice(source);
        Ok(())
    }
}

#[test]
fn direct_output_reopens_and_validates_with_exact_identity_and_metadata() {
    let path = TestPath::new("direct");
    let mut output = builder(&path.0, direct_spec(AddressFamily::Ipv4), generous_budget());
    output.push_direct_v4(Ipv4Key::MIN, Ipv4Key(9), 11).unwrap();
    output.push_direct_v4(Ipv4Key(10), Ipv4Key(99), 12).unwrap();
    output
        .push_direct_v4(Ipv4Key(1_000), Ipv4Key::MAX, 13)
        .unwrap();
    let metadata = br#"{"source":"immutable-output"}"#;
    output
        .write_metadata_with_budget(metadata, 2 * 1024 * 1024)
        .unwrap();
    let finished = output.finish_owned().unwrap();

    assert_eq!(finished.meta.database_id, [3; 16]);
    assert_eq!(finished.meta.txn_id, 7);
    assert_eq!(finished.meta.commit_nonce, [4; 16]);
    assert_eq!(finished.meta.free_bitmap_root, 0);
    assert_eq!(finished.meta.retirement_root, 0);
    assert_eq!(finished.meta.retired_extent_count, 0);
    assert_eq!(finished.meta.allocator_reserve, [0; 4]);
    drop(finished.file);

    let bytes = fs::read(&path.0).unwrap();
    assert_eq!(&bytes[..PAGE_SIZE], &bytes[PAGE_SIZE..2 * PAGE_SIZE]);
    let reader = ImmutableReader::open(&path.0).unwrap();
    let info = reader.info();
    assert_eq!(info.database_id, [3; 16]);
    assert_eq!(info.transaction_id, 7);
    assert_eq!(info.commit_nonce, [4; 16]);
    assert_eq!(info.range_record_count, 3);
    assert_eq!(
        reader.metadata_json().unwrap().as_deref(),
        Some(metadata.as_slice())
    );

    let mut cursor = reader.direct_cursor_v4(RangeDirection::Forward).unwrap();
    let mut ranges = Vec::new();
    while let Some(range) = cursor.next_range().unwrap() {
        ranges.push((range.from.0, range.to.0, range.value));
    }
    assert_eq!(
        ranges,
        vec![(0, 9, 11), (10, 99, 12), (1_000, u32::MAX, 13)]
    );
    validate_clean(&path.0);
}

#[test]
fn full_space_ipv6_output_is_valid() {
    let path = TestPath::new("ipv6");
    let mut output = builder(&path.0, direct_spec(AddressFamily::Ipv6), generous_budget());
    output
        .push_direct_v6(Ipv6Key::MIN, Ipv6Key::MAX, 42)
        .unwrap();
    drop(output.finish_owned().unwrap().file);

    let reader = ImmutableReader::open(&path.0).unwrap();
    assert_eq!(reader.lookup_direct_v6(Ipv6Key::MIN).unwrap(), Some(42));
    assert_eq!(reader.lookup_direct_v6(Ipv6Key::MAX).unwrap(), Some(42));
    validate_clean(&path.0);
}

#[test]
fn empty_direct_and_membership_outputs_preserve_valid_empty_state() {
    let direct = TestPath::new("empty-direct");
    drop(
        builder(
            &direct.0,
            direct_spec(AddressFamily::Ipv4),
            generous_budget(),
        )
        .finish_owned()
        .unwrap()
        .file,
    );
    assert_eq!(
        ImmutableReader::open(&direct.0)
            .unwrap()
            .info()
            .range_record_count,
        0
    );
    validate_clean(&direct.0);

    let membership = TestPath::new("empty-membership");
    let finished = builder(&membership.0, membership_spec(1_000), generous_budget())
        .finish_owned()
        .unwrap();
    assert_eq!(finished.meta.feed_index_limit, 1_000);
    assert_eq!(finished.meta.membership_id_limit, 1);
    drop(finished.file);
    let info = ImmutableReader::open(&membership.0).unwrap().info();
    assert_eq!(info.active_feed_count, 0);
    assert_eq!(info.range_record_count, 0);
    validate_clean(&membership.0);
}

#[test]
fn multi_level_direct_output_has_no_unreachable_build_pages() {
    let path = TestPath::new("multi-level");
    let mut output = builder(&path.0, direct_spec(AddressFamily::Ipv4), generous_budget());
    for index in 0..2_000u32 {
        let from = index * 3;
        output
            .push_direct_v4(Ipv4Key(from), Ipv4Key(from + 1), index % 3)
            .unwrap();
    }
    let finished = output.finish_owned().unwrap();
    assert_eq!(finished.meta.range_record_count, 2_000);
    drop(finished.file);
    validate_clean(&path.0);
}

#[test]
fn branch_overflow_keeps_a_valid_one_child_right_edge() {
    const LEAF_CAPACITY: usize =
        (PAGE_SIZE - slotted_page::HEADER_SIZE) / (Ipv6Key::WIDTH * 2 + 4 + 2);
    const BRANCH_CAPACITY: usize =
        (PAGE_SIZE - slotted_page::HEADER_SIZE) / (Ipv6Key::WIDTH + 4 + 2);
    const RECORD_COUNT: usize = LEAF_CAPACITY * BRANCH_CAPACITY + 1;

    let path = TestPath::new("one-child-right-edge");
    let mut output = builder(&path.0, direct_spec(AddressFamily::Ipv6), generous_budget());
    for index in 0..RECORD_COUNT {
        let address = Ipv6Key::from_u128((index as u128) * 2);
        output
            .push_direct_v6(address, address, index as u32)
            .unwrap();
    }
    let finished = output.finish_owned().unwrap();

    let mut root_page = [0; PAGE_SIZE];
    file_io::read_page(
        &finished.file,
        finished.meta.range_root,
        finished.meta.page_count,
        &mut root_page,
    )
    .unwrap();
    let root =
        range_tree::parse_header::<Ipv6Key>(&root_page, finished.meta.txn_id, Some(2)).unwrap();
    assert_eq!(root.item_count, 2);

    let right = range_tree::branch_child::<Ipv6Key>(&root_page, &root, 1).unwrap();
    let mut right_page = [0; PAGE_SIZE];
    file_io::read_page(
        &finished.file,
        right,
        finished.meta.page_count,
        &mut right_page,
    )
    .unwrap();
    let right =
        range_tree::parse_header::<Ipv6Key>(&right_page, finished.meta.txn_id, Some(1)).unwrap();
    assert_eq!(right.item_count, 1);

    drop(finished.file);
    validate_clean(&path.0);
}

#[test]
fn membership_output_streams_sparse_words_and_rebuilds_derived_state() {
    let path = TestPath::new("membership");
    let spec = OutputSpec {
        address_family: AddressFamily::Ipv4,
        value_kind: ValueKind::Membership,
        value_tag: ValueTag::new(b"feeds").unwrap(),
        database_id: [8; 16],
        transaction_id: 19,
        commit_nonce: [9; 16],
        feed_index_limit: 32_002,
    };
    let mut output = builder(&path.0, spec, generous_budget());
    output
        .push_feed(FeedName::new("alpha").unwrap(), 3)
        .unwrap();
    output
        .push_feed(FeedName::new("middle").unwrap(), 31_999)
        .unwrap();
    output
        .push_feed(FeedName::new("omega").unwrap(), 32_001)
        .unwrap();

    let mut wide = vec![0u64; 501];
    wide[0] = 1 << 3;
    wide[499] = 1 << 63;
    wide[500] = 1 << 1;
    let wide = Words(wide);
    let alpha = Words(vec![1 << 3]);
    output
        .push_membership_v4(Ipv4Key(0), Ipv4Key(9), &wide)
        .unwrap();
    output
        .push_membership_v4(Ipv4Key(10), Ipv4Key(19), &alpha)
        .unwrap();
    let (result, allocations) =
        count_thread_allocations(|| output.push_membership_v4(Ipv4Key(30), Ipv4Key(39), &wide));
    result.unwrap();
    assert_eq!(allocations, 0);
    let finished = output.finish_owned().unwrap();

    assert_eq!(finished.meta.feed_index_limit, 32_002);
    assert_eq!(finished.meta.active_feed_count, 3);
    assert_eq!(finished.meta.membership_entry_count, 2);
    assert_eq!(finished.meta.membership_id_limit, 3);
    assert_eq!(finished.meta.range_record_count, 3);
    drop(finished.file);

    let reader = ImmutableReader::open(&path.0).unwrap();
    let mut feeds = reader.feed_cursor().unwrap();
    assert_eq!(feeds.next_feed().unwrap().unwrap().index, 3);
    assert_eq!(feeds.next_feed().unwrap().unwrap().index, 31_999);
    assert_eq!(feeds.next_feed().unwrap().unwrap().index, 32_001);
    assert!(feeds.next_feed().unwrap().is_none());

    let first = reader.lookup_membership_v4(Ipv4Key(5)).unwrap().unwrap();
    assert_eq!(first.word_count().unwrap(), 501);
    assert!(first.contains_index(3).unwrap());
    assert!(first.contains_index(31_999).unwrap());
    assert!(first.contains_index(32_001).unwrap());
    assert!(!first.contains_index(4).unwrap());
    let second = reader.lookup_membership_v4(Ipv4Key(15)).unwrap().unwrap();
    assert_eq!(second.word_count().unwrap(), 1);
    assert!(second.contains_index(3).unwrap());
    assert!(!second.contains_index(31_999).unwrap());
    validate_clean(&path.0);
}

#[test]
fn present_empty_metadata_is_distinct_from_absent_metadata() {
    let path = TestPath::new("empty-metadata");
    let mut output = builder(&path.0, direct_spec(AddressFamily::Ipv4), generous_budget());
    output
        .write_metadata_with_budget(b"", 2 * 1024 * 1024)
        .unwrap();
    drop(output.finish_owned().unwrap().file);

    let reader = ImmutableReader::open(&path.0).unwrap();
    assert_eq!(reader.metadata_json_len(), Some(0));
    assert_eq!(reader.metadata_json().unwrap(), Some(Vec::new()));
    validate_clean(&path.0);
}

#[test]
fn page_budget_refuses_growth_and_poisoned_output_cannot_finish() {
    let path = TestPath::new("page-budget");
    let budget = OutputBudget {
        max_output_pages: 2,
    };
    let mut output = builder(&path.0, direct_spec(AddressFamily::Ipv4), budget);
    assert!(matches!(
        output.push_direct_v4(Ipv4Key(1), Ipv4Key(2), 3),
        Err(Error::BudgetExceeded("immutable output pages"))
    ));
    assert_eq!(fs::metadata(&path.0).unwrap().len(), (2 * PAGE_SIZE) as u64);
    assert!(matches!(
        output.finish_owned(),
        Err(FinishFailure {
            cause: Error::WrongState(_),
            ..
        })
    ));
}

#[test]
fn malformed_order_and_metadata_budget_are_rejected_permanently() {
    let reversed = TestPath::new("reversed");
    let mut output = builder(
        &reversed.0,
        direct_spec(AddressFamily::Ipv4),
        generous_budget(),
    );
    assert!(matches!(
        output.push_direct_v4(Ipv4Key(2), Ipv4Key(1), 1),
        Err(Error::InvalidArgument(_))
    ));
    assert!(matches!(
        output.finish_owned(),
        Err(FinishFailure {
            cause: Error::WrongState(_),
            ..
        })
    ));

    let overlap = TestPath::new("overlap");
    let mut output = builder(
        &overlap.0,
        direct_spec(AddressFamily::Ipv4),
        generous_budget(),
    );
    output.push_direct_v4(Ipv4Key(0), Ipv4Key(10), 1).unwrap();
    assert!(matches!(
        output.push_direct_v4(Ipv4Key(10), Ipv4Key(20), 2),
        Err(Error::InvalidArgument(_))
    ));

    let adjacent = TestPath::new("adjacent");
    let mut output = builder(
        &adjacent.0,
        direct_spec(AddressFamily::Ipv4),
        generous_budget(),
    );
    output.push_direct_v4(Ipv4Key(0), Ipv4Key(9), 1).unwrap();
    assert!(matches!(
        output.push_direct_v4(Ipv4Key(10), Ipv4Key(20), 1),
        Err(Error::InvalidArgument(_))
    ));

    let metadata = TestPath::new("metadata-budget");
    let mut output = builder(
        &metadata.0,
        direct_spec(AddressFamily::Ipv4),
        OutputBudget {
            max_output_pages: 100,
        },
    );
    assert!(matches!(
        output.write_metadata_with_budget(b"metadata", 4),
        Err(Error::BudgetExceeded("metadata compression heap"))
    ));
    assert!(matches!(
        output.finish_owned(),
        Err(FinishFailure {
            cause: Error::WrongState(_),
            ..
        })
    ));
}

#[test]
fn membership_rejects_inactive_bits_and_trailing_zero_words() {
    let inactive = TestPath::new("inactive");
    let mut output = builder(&inactive.0, membership_spec(128), generous_budget());
    output
        .push_feed(FeedName::new("alpha").unwrap(), 3)
        .unwrap();
    assert!(matches!(
        output.push_membership_v4(Ipv4Key(0), Ipv4Key(1), &Words(vec![1 << 4])),
        Err(Error::InvalidArgument(_))
    ));

    let trailing = TestPath::new("trailing");
    let mut output = builder(&trailing.0, membership_spec(128), generous_budget());
    output
        .push_feed(FeedName::new("alpha").unwrap(), 3)
        .unwrap();
    assert!(matches!(
        output.push_membership_v4(Ipv4Key(0), Ipv4Key(1), &Words(vec![1 << 3, 0])),
        Err(Error::InvalidArgument(_))
    ));
}

#[test]
fn leaf_rollover_allocates_no_heap() {
    const LEAF_CAPACITY: usize =
        (PAGE_SIZE - slotted_page::HEADER_SIZE) / (Ipv4Key::WIDTH * 2 + 4 + 2);

    let path = TestPath::new("allocation");
    let mut output = builder(&path.0, direct_spec(AddressFamily::Ipv4), generous_budget());
    for index in 0..LEAF_CAPACITY {
        let address = Ipv4Key((index as u32) * 2);
        output
            .push_direct_v4(address, address, index as u32)
            .unwrap();
    }
    let address = Ipv4Key((LEAF_CAPACITY as u32) * 2);
    let (result, allocations) =
        count_thread_allocations(|| output.push_direct_v4(address, address, LEAF_CAPACITY as u32));
    result.unwrap();
    assert_eq!(allocations, 0);
    drop(output.finish_owned().unwrap().file);
}

fn builder(path: &Path, spec: OutputSpec, budget: OutputBudget) -> Builder {
    let file = OpenOptions::new()
        .read(true)
        .write(true)
        .create_new(true)
        .open(path)
        .unwrap();
    Builder::new_owned(file, spec, budget).unwrap()
}

fn direct_spec(address_family: AddressFamily) -> OutputSpec {
    OutputSpec {
        address_family,
        value_kind: ValueKind::Direct,
        value_tag: ValueTag::RETENTION,
        database_id: [3; 16],
        transaction_id: 7,
        commit_nonce: [4; 16],
        feed_index_limit: 0,
    }
}

fn membership_spec(feed_index_limit: u64) -> OutputSpec {
    OutputSpec {
        address_family: AddressFamily::Ipv4,
        value_kind: ValueKind::Membership,
        value_tag: ValueTag::new(b"feeds").unwrap(),
        database_id: [5; 16],
        transaction_id: 11,
        commit_nonce: [6; 16],
        feed_index_limit,
    }
}

fn generous_budget() -> OutputBudget {
    OutputBudget {
        max_output_pages: 100_000,
    }
}

fn validate_clean(path: &Path) {
    let result = validate(
        path,
        ValidationMode::ImmutableCurrent,
        &ValidationBudget::heap_only(8 * 1024 * 1024, 1),
        &CancellationToken::new(),
        &mut |_finding: &crate::validation::ValidationFinding| Ok(ValidationSinkControl::Continue),
    )
    .unwrap();
    assert!(result.valid, "{:?}", result.progress);
}
