use std::ffi::c_void;
use std::mem::size_of;

use iprange_livedb::{
    create_live, AddressFamily, CancellationToken, DirectRange, FinishedWorkflow, Ipv4Key,
    LiveWriter, StructureKind, ValueKind, ValueTag,
};

use crate::abi::{ByteSlice, CallbackFailure, Cancellation};
use crate::abi_sdk::{
    DirectJoinBudget, DirectJoinCell, DirectJoinReport, FeedCardinality, FeedOverlap,
    MatchingFeedsReport, MembershipAggregationReport, MembershipCrossCell, MembershipJoinReport,
    UncoveredFeed,
};
use crate::registry;

use super::{all_scope, assert_ok, create_membership, open_live, transaction_budget, Files};

unsafe extern "C" fn collect_feed_names(
    context: *mut c_void,
    records: *const crate::abi_sdk::FeedNameValue,
    count: u64,
    _failure: *mut CallbackFailure,
) -> u32 {
    // SAFETY: the test supplies this context and the ABI lends this exact batch.
    let output = unsafe { &mut *context.cast::<Vec<String>>() };
    let records = unsafe { std::slice::from_raw_parts(records, count as usize) };
    for record in records {
        output.push(name(&record.bytes, record.length));
    }
    registry::SINK_OUTCOME_CONTINUE
}

unsafe extern "C" fn collect_cardinalities(
    context: *mut c_void,
    records: *const FeedCardinality,
    count: u64,
    _failure: *mut CallbackFailure,
) -> u32 {
    // SAFETY: the test supplies this context and the ABI lends this exact batch.
    let output = unsafe { &mut *context.cast::<AggregateOutput>() };
    output
        .cardinalities
        .extend_from_slice(unsafe { std::slice::from_raw_parts(records, count as usize) });
    registry::SINK_OUTCOME_CONTINUE
}

unsafe extern "C" fn collect_overlaps(
    context: *mut c_void,
    records: *const FeedOverlap,
    count: u64,
    _failure: *mut CallbackFailure,
) -> u32 {
    // SAFETY: the test supplies this context and the ABI lends this exact batch.
    let output = unsafe { &mut *context.cast::<AggregateOutput>() };
    output
        .pairs
        .extend_from_slice(unsafe { std::slice::from_raw_parts(records, count as usize) });
    registry::SINK_OUTCOME_CONTINUE
}

unsafe extern "C" fn collect_direct(
    context: *mut c_void,
    records: *const DirectJoinCell,
    count: u64,
    _failure: *mut CallbackFailure,
) -> u32 {
    // SAFETY: the test supplies this context and the ABI lends this exact batch.
    let output = unsafe { &mut *context.cast::<Vec<DirectJoinCell>>() };
    output.extend_from_slice(unsafe { std::slice::from_raw_parts(records, count as usize) });
    registry::SINK_OUTCOME_CONTINUE
}

unsafe extern "C" fn collect_cross(
    context: *mut c_void,
    records: *const MembershipCrossCell,
    count: u64,
    _failure: *mut CallbackFailure,
) -> u32 {
    // SAFETY: the test supplies this context and the ABI lends this exact batch.
    let output = unsafe { &mut *context.cast::<JoinOutput>() };
    output
        .cross
        .extend_from_slice(unsafe { std::slice::from_raw_parts(records, count as usize) });
    registry::SINK_OUTCOME_CONTINUE
}

unsafe extern "C" fn collect_uncovered(
    context: *mut c_void,
    records: *const UncoveredFeed,
    count: u64,
    _failure: *mut CallbackFailure,
) -> u32 {
    // SAFETY: the test supplies this context and the ABI lends this exact batch.
    let output = unsafe { &mut *context.cast::<JoinOutput>() };
    output
        .uncovered
        .extend_from_slice(unsafe { std::slice::from_raw_parts(records, count as usize) });
    registry::SINK_OUTCOME_CONTINUE
}

#[derive(Default)]
struct AggregateOutput {
    cardinalities: Vec<FeedCardinality>,
    pairs: Vec<FeedOverlap>,
}

#[derive(Default)]
struct JoinOutput {
    cross: Vec<MembershipCrossCell>,
    uncovered: Vec<UncoveredFeed>,
}

#[test]
fn c_queries_and_provider_joins_report_exact_named_results() {
    let mut files = Files::new();
    let membership_path = files.path("query-membership");
    let right_path = files.path("query-right");
    let direct_path = files.path("query-direct");
    create_membership(&membership_path, &[("x", &[(0, 9)]), ("y", &[(5, 14)])]);
    create_membership(&right_path, &[("y", &[(10, 19)]), ("z", &[(12, 17)])]);
    create_direct(&direct_path);

    // SAFETY: every handle and pointer below is created and consumed by this test.
    unsafe {
        let reader = open_live(&membership_path);
        let right_reader = open_live(&right_path);
        let direct_reader = open_live(&direct_path);
        let scope = all_scope(reader);
        let right_scope = all_scope(right_reader);
        let mut error = std::ptr::null_mut();

        let mut matching = Vec::<String>::new();
        let mut matching_report = MatchingFeedsReport::default();
        assert_ok(
            crate::query::iprange_v4_abi1_reader_matching_feeds(
                reader,
                crate::ip::encode(crate::ip::Key::V4(Ipv4Key(7))),
                Some(collect_feed_names),
                (&mut matching as *mut Vec<String>).cast(),
                Cancellation::default(),
                &mut matching_report,
                &mut error,
            ),
            error,
        );
        assert_eq!(matching, ["x", "y"]);
        assert_eq!(matching_report.matching_feed_count, 2);

        let mut aggregate = AggregateOutput::default();
        let mut aggregate_report = MembershipAggregationReport::default();
        assert_ok(
            crate::query::iprange_v4_abi1_membership_scope_aggregate(
                scope,
                registry::MEMBERSHIP_AGGREGATION_ALL_PAIRS,
                ByteSlice::default(),
                std::ptr::null(),
                0,
                Some(collect_cardinalities),
                Some(collect_overlaps),
                (&mut aggregate as *mut AggregateOutput).cast(),
                Cancellation::default(),
                &mut aggregate_report,
                &mut error,
            ),
            error,
        );
        assert_eq!(aggregate_report.feed_result_count, 2);
        assert_eq!(aggregate_report.pair_result_count, 1);
        assert_eq!(cardinality(&aggregate.cardinalities, "x"), 10);
        assert_eq!(cardinality(&aggregate.cardinalities, "y"), 10);
        assert_eq!(pair(&aggregate.pairs, "x", "y"), 5);

        let direct_budget = DirectJoinBudget {
            abi_version: 1,
            struct_size: size_of::<DirectJoinBudget>() as u32,
            max_result_cells: 16,
        };
        let mut direct = Vec::<DirectJoinCell>::new();
        let mut direct_report = DirectJoinReport::default();
        assert_ok(
            crate::query::iprange_v4_abi1_membership_scope_join_direct(
                scope,
                direct_reader,
                &direct_budget,
                Some(collect_direct),
                (&mut direct as *mut Vec<DirectJoinCell>).cast(),
                Cancellation::default(),
                &mut direct_report,
                &mut error,
            ),
            error,
        );
        assert_eq!(direct_report.mapped_addresses.lo, 10);
        assert_eq!(direct_report.unmapped_addresses.lo, 5);
        assert_eq!(direct_cell(&direct, "x", Some(1)), 5);
        assert_eq!(direct_cell(&direct, "x", Some(2)), 2);
        assert_eq!(direct_cell(&direct, "x", None), 3);
        assert_eq!(direct_cell(&direct, "y", Some(2)), 5);
        assert_eq!(direct_cell(&direct, "y", None), 5);

        let mut joined = JoinOutput::default();
        let mut join_report = MembershipJoinReport::default();
        assert_ok(
            crate::query::iprange_v4_abi1_membership_scope_join_membership(
                scope,
                right_scope,
                Some(collect_cross),
                Some(collect_uncovered),
                (&mut joined as *mut JoinOutput).cast(),
                Cancellation::default(),
                &mut join_report,
                &mut error,
            ),
            error,
        );
        assert_eq!(cross(&joined.cross, "y", "y"), 5);
        assert_eq!(cross(&joined.cross, "y", "z"), 3);
        assert_eq!(
            uncovered(&joined.uncovered, registry::UNCOVERED_SIDE_LEFT, "x"),
            10
        );
        assert_eq!(
            uncovered(&joined.uncovered, registry::UNCOVERED_SIDE_LEFT, "y"),
            5
        );
        assert_eq!(
            uncovered(&joined.uncovered, registry::UNCOVERED_SIDE_RIGHT, "y"),
            5
        );
        assert_eq!(
            uncovered(&joined.uncovered, registry::UNCOVERED_SIDE_RIGHT, "z"),
            3
        );

        close_scope(scope, &mut error);
        close_scope(right_scope, &mut error);
        close_reader(reader, &mut error);
        close_reader(right_reader, &mut error);
        close_reader(direct_reader, &mut error);
    }
}

fn create_direct(path: &std::path::Path) {
    let cancellation = CancellationToken::new();
    create_live(
        path,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        StructureKind::None,
        ValueTag::new(b"asn").unwrap(),
        1,
        &cancellation,
    )
    .unwrap();
    let mut writer = LiveWriter::open(path, transaction_budget(), &cancellation).unwrap();
    let mut operation = writer.begin_direct_replacement(&cancellation).unwrap();
    operation
        .add_ranges_v4_slice(&[
            DirectRange {
                from: Ipv4Key(0),
                to: Ipv4Key(4),
                value: 1,
            },
            DirectRange {
                from: Ipv4Key(8),
                to: Ipv4Key(12),
                value: 2,
            },
        ])
        .unwrap();
    match operation.finish_input().unwrap() {
        FinishedWorkflow::Changed(prepared) => {
            prepared.commit().unwrap();
        }
        FinishedWorkflow::NoChange(_) => panic!("direct replacement did not change"),
    }
    writer.close().unwrap();
}

unsafe fn close_scope(
    scope: *mut crate::MembershipScopeHandle,
    error: &mut *mut crate::ErrorHandle,
) {
    assert_ok(
        unsafe { crate::query::iprange_v4_abi1_membership_scope_close(scope, error) },
        *error,
    );
    assert_ok(
        unsafe { crate::query::iprange_v4_abi1_membership_scope_destroy(scope, error) },
        *error,
    );
}

unsafe fn close_reader(reader: *mut crate::ReaderHandle, error: &mut *mut crate::ErrorHandle) {
    assert_ok(
        unsafe { crate::lifecycle::iprange_v4_abi1_reader_close(reader, error) },
        *error,
    );
    assert_ok(
        unsafe { crate::lifecycle::iprange_v4_abi1_reader_destroy(reader, error) },
        *error,
    );
}

fn name(bytes: &[u8; 255], length: u32) -> String {
    std::str::from_utf8(&bytes[..length as usize])
        .unwrap()
        .to_owned()
}

fn cardinality(cells: &[FeedCardinality], feed: &str) -> u64 {
    cells
        .iter()
        .find(|cell| name(&cell.feed.bytes, cell.feed.length) == feed)
        .unwrap()
        .addresses
        .lo
}

fn pair(cells: &[FeedOverlap], left: &str, right: &str) -> u64 {
    cells
        .iter()
        .find(|cell| {
            name(&cell.left.bytes, cell.left.length) == left
                && name(&cell.right.bytes, cell.right.length) == right
        })
        .unwrap()
        .addresses
        .lo
}

fn direct_cell(cells: &[DirectJoinCell], feed: &str, value: Option<u32>) -> u64 {
    cells
        .iter()
        .find(|cell| {
            name(&cell.feed.bytes, cell.feed.length) == feed
                && (cell.direct_present != 0).then_some(cell.direct_value) == value
        })
        .unwrap()
        .addresses
        .lo
}

fn cross(cells: &[MembershipCrossCell], left: &str, right: &str) -> u64 {
    cells
        .iter()
        .find(|cell| {
            name(&cell.left.bytes, cell.left.length) == left
                && name(&cell.right.bytes, cell.right.length) == right
        })
        .unwrap()
        .addresses
        .lo
}

fn uncovered(cells: &[UncoveredFeed], side: u32, feed: &str) -> u64 {
    cells
        .iter()
        .find(|cell| cell.side == side && name(&cell.feed.bytes, cell.feed.length) == feed)
        .unwrap()
        .addresses
        .lo
}
