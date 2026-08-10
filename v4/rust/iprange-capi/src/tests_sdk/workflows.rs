use std::ffi::c_void;
use std::mem::size_of;

use iprange_livedb::{
    create_live, AddressFamily, AddressRange, CancellationToken, DirectRange, FinishedWorkflow,
    ImmutableReader, Ipv4Key, LiveReader, LiveWriter, RangeDirection, ValueKind, ValueTag,
};

use crate::abi::{CallbackFailure, Cancellation, Range, TransactionBudget};
use crate::abi_sdk::{
    HistoryProjectionReport, HistoryWindowInput, HistoryWindowReport, ImmutableFeedBudget,
    ImmutableFeedReport, OptionalByteSlice,
};
use crate::registry;

use super::{abi_path, assert_ok, bytes, transaction_budget, Files};

struct CoverageSource {
    emitted: bool,
}

unsafe extern "C" fn coverage_source(
    context: *mut c_void,
    records: *mut Range,
    capacity: u64,
    count: *mut u64,
    _failure: *mut CallbackFailure,
) -> u32 {
    // SAFETY: the test supplies this context and the ABI lends these exact outputs.
    let source = unsafe { &mut *context.cast::<CoverageSource>() };
    if source.emitted {
        unsafe { *count = 0 };
        return registry::SOURCE_OUTCOME_END;
    }
    assert!(capacity >= 3);
    let records = unsafe { std::slice::from_raw_parts_mut(records, capacity as usize) };
    for (output, (from, to)) in records.iter_mut().zip([(10, 19), (0, 4), (3, 12)]) {
        *output = Range {
            from: crate::ip::encode(crate::ip::Key::V4(Ipv4Key(from))),
            to: crate::ip::encode(crate::ip::Key::V4(Ipv4Key(to))),
        };
    }
    source.emitted = true;
    unsafe { *count = 3 };
    registry::SOURCE_OUTCOME_BATCH
}

#[test]
fn c_immutable_builder_ingests_unordered_ranges_into_the_final_inode() {
    let mut files = Files::new();
    let output_path = files.path("immutable-workflow");
    let budget = ImmutableFeedBudget {
        abi_version: 1,
        struct_size: size_of::<ImmutableFeedBudget>() as u32,
        max_heap_bytes: 4 * 1024 * 1024,
        max_output_pages: 20_000,
        max_workspace_pages: 20_000,
        max_open_files: 3,
        reserved: 0,
    };
    let metadata = br#"{"source":"test"}"#;
    let mut source = CoverageSource { emitted: false };
    let mut semantic = ImmutableFeedReport::default();
    let mut publication = std::ptr::null_mut();
    let mut error = std::ptr::null_mut();
    // SAFETY: all pointers remain valid for the synchronous ABI call.
    let status = unsafe {
        crate::immutable_feed_ops::iprange_v4_abi1_create_immutable_feed(
            abi_path(&output_path),
            registry::ADDRESS_FAMILY_IPV4,
            bytes(b"download"),
            bytes(b"downloaded"),
            OptionalByteSlice {
                present: 1,
                reserved: [0; 7],
                value: bytes(metadata),
            },
            registry::DESTINATION_POLICY_FAIL_IF_EXISTS,
            Some(coverage_source),
            (&mut source as *mut CoverageSource).cast(),
            &budget,
            Cancellation::default(),
            &mut semantic,
            &mut publication,
            &mut error,
        )
    };
    assert_ok(status, error);
    assert_eq!(semantic.input_record_count, 3);
    assert_eq!(semantic.normalized_interval_count, 1);
    assert_eq!(semantic.addresses.lo, 20);
    // SAFETY: publication is the owned report returned above.
    assert_ok(
        unsafe { crate::report::iprange_v4_abi1_report_destroy(publication, &mut error) },
        error,
    );

    let reader = ImmutableReader::open(&output_path).unwrap();
    assert_eq!(
        reader.metadata_json().unwrap().as_deref(),
        Some(metadata.as_slice())
    );
    let mut cursor = reader
        .feed_range_cursor_v4("downloaded", RangeDirection::Forward)
        .unwrap();
    assert_eq!(
        cursor.next_range().unwrap(),
        Some(AddressRange {
            from: Ipv4Key(0),
            to: Ipv4Key(19),
        })
    );
    assert_eq!(cursor.next_range().unwrap(), None);
}

#[test]
fn c_history_projection_prepares_exact_windows_for_normal_commit() {
    let mut files = Files::new();
    let source_path = files.path("history-source");
    let destination_path = files.path("history-destination");
    create_last_seen(&source_path);
    create_live(
        &destination_path,
        AddressFamily::Ipv4,
        ValueKind::Membership,
        ValueTag::new(b"history").unwrap(),
        1,
        &CancellationToken::new(),
    )
    .unwrap();

    let budget = TransactionBudget {
        abi_version: 1,
        struct_size: size_of::<TransactionBudget>() as u32,
        max_heap_bytes: 4 * 1024 * 1024,
        max_private_pages: 20_000,
        max_file_growth_pages: 20_000,
        max_open_files: 2,
        reserved: 0,
    };
    let mut source = std::ptr::null_mut();
    let mut writer = std::ptr::null_mut();
    let mut error = std::ptr::null_mut();
    // SAFETY: all handles and reports are owned and consumed in this test.
    unsafe {
        assert_ok(
            crate::lifecycle::iprange_v4_abi1_open_live_reader(
                abi_path(&source_path),
                Cancellation::default(),
                &mut source,
                &mut error,
            ),
            error,
        );
        assert_ok(
            crate::lifecycle::iprange_v4_abi1_open_live_writer(
                abi_path(&destination_path),
                &budget,
                Cancellation::default(),
                &mut writer,
                &mut error,
            ),
            error,
        );
        let windows = [
            HistoryWindowInput {
                feed_name: bytes(b"recent"),
                cutoff: 15,
                reserved: 0,
            },
            HistoryWindowInput {
                feed_name: bytes(b"month"),
                cutoff: 5,
                reserved: 0,
            },
        ];
        let mut history = std::ptr::null_mut();
        assert_ok(
            crate::history_ops::iprange_v4_abi1_writer_project_history(
                writer,
                source,
                windows.as_ptr(),
                windows.len() as u64,
                Cancellation::default(),
                &mut history,
                &mut error,
            ),
            error,
        );
        let mut fixed = HistoryProjectionReport::default();
        assert_ok(
            crate::report::iprange_v4_abi1_report_get_history_projection(
                history, &mut fixed, &mut error,
            ),
            error,
        );
        assert_eq!(fixed.source_range_count, 2);
        assert_eq!(fixed.created_feed_count, 2);
        assert_eq!(fixed.window_count, 2);
        let mut recent = HistoryWindowReport::default();
        assert_ok(
            crate::report::iprange_v4_abi1_report_get_history_window(
                history,
                0,
                &mut recent,
                &mut error,
            ),
            error,
        );
        assert_eq!(recent.after_addresses.lo, 10);
        assert_ok(
            crate::report::iprange_v4_abi1_report_destroy(history, &mut error),
            error,
        );

        let mut commit = std::ptr::null_mut();
        assert_ok(
            crate::writer::iprange_v4_abi1_writer_commit(writer, &mut commit, &mut error),
            error,
        );
        assert_ok(
            crate::report::iprange_v4_abi1_report_destroy(commit, &mut error),
            error,
        );
        let mut close = std::ptr::null_mut();
        assert_ok(
            crate::writer::iprange_v4_abi1_writer_close(writer, &mut close, &mut error),
            error,
        );
        assert_ok(
            crate::report::iprange_v4_abi1_report_destroy(close, &mut error),
            error,
        );
        assert_ok(
            crate::lifecycle::iprange_v4_abi1_writer_destroy(writer, &mut error),
            error,
        );
        assert_ok(
            crate::lifecycle::iprange_v4_abi1_reader_close(source, &mut error),
            error,
        );
        assert_ok(
            crate::lifecycle::iprange_v4_abi1_reader_destroy(source, &mut error),
            error,
        );
    }

    let cancellation = CancellationToken::new();
    let mut reader = LiveReader::open(&destination_path, &cancellation).unwrap();
    assert_feed(&reader, "recent", 10, 19);
    assert_feed(&reader, "month", 0, 19);
    reader.close().unwrap();
}

fn create_last_seen(path: &std::path::Path) {
    let cancellation = CancellationToken::new();
    create_live(
        path,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::LAST_SEEN,
        1,
        &cancellation,
    )
    .unwrap();
    let mut writer = LiveWriter::open(path, transaction_budget(), &cancellation).unwrap();
    let mut replacement = writer.begin_direct_replacement(&cancellation).unwrap();
    replacement
        .add_ranges_v4_slice(&[
            DirectRange {
                from: Ipv4Key(0),
                to: Ipv4Key(9),
                value: 10,
            },
            DirectRange {
                from: Ipv4Key(10),
                to: Ipv4Key(19),
                value: 20,
            },
        ])
        .unwrap();
    match replacement.finish_input().unwrap() {
        FinishedWorkflow::Changed(prepared) => {
            prepared.commit().unwrap();
        }
        FinishedWorkflow::NoChange(_) => panic!("last-seen replacement did not change"),
    }
    writer.close().unwrap();
}

fn assert_feed(reader: &LiveReader, name: &str, from: u32, to: u32) {
    let mut cursor = reader
        .feed_range_cursor_v4(name, RangeDirection::Forward)
        .unwrap();
    assert_eq!(
        cursor.next_range().unwrap(),
        Some(AddressRange {
            from: Ipv4Key(from),
            to: Ipv4Key(to),
        })
    );
    assert_eq!(cursor.next_range().unwrap(), None);
}
