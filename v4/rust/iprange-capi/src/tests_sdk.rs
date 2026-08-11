use std::fs;
use std::os::unix::ffi::OsStrExt;
use std::path::{Path as FsPath, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

#[cfg(not(target_os = "freebsd"))]
use std::{ffi::c_void, mem::size_of};

#[cfg(not(target_os = "freebsd"))]
use iprange_livedb::{
    create_live, AddressFamily, AddressRange, CancellationToken, Cardinality129, FeedName,
    FinishedWorkflow, ImmutableReader, Ipv4Key, LiveWriter, StructureKind,
    TransactionBudget as CoreTransactionBudget, ValueKind, ValueTag,
};

use crate::abi::{ByteSlice, Path};
#[cfg(not(target_os = "freebsd"))]
use crate::abi::{CallbackFailure, Cancellation};
#[cfg(not(target_os = "freebsd"))]
use crate::abi_sdk::{
    AlgebraComparisonReport, AlgebraCountReport, AlgebraOutputBudget, AlgebraOutputModeInput,
    AlgebraSetOperationInput, AlgebraSetReport, FeedNameValue, FeedSelectionInput,
    MembershipAlgebraBudget as AbiAlgebraBudget, MembershipQueryBudget, OptionalByteSlice,
};
use crate::error::ErrorHandle;
#[cfg(not(target_os = "freebsd"))]
use crate::handle::{MembershipAlgebraHandle, MembershipScopeHandle, ReaderHandle};
use crate::registry;
#[cfg(not(target_os = "freebsd"))]
use crate::report::ReportHandle;

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
            "iprange-capi-sdk-{label}-{}-{unique}",
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

#[cfg(not(target_os = "freebsd"))]
fn transaction_budget() -> CoreTransactionBudget {
    CoreTransactionBudget {
        max_heap_bytes: 2 * 1024 * 1024,
        max_private_pages: 20_000,
        max_file_growth_pages: 20_000,
        max_open_files: 2,
    }
}

#[cfg(not(target_os = "freebsd"))]
fn create_membership(path: &FsPath, feeds: &[(&str, &[(u32, u32)])]) {
    let cancellation = CancellationToken::new();
    create_live(
        path,
        AddressFamily::Ipv4,
        ValueKind::Membership,
        StructureKind::None,
        ValueTag::new(b"feeds").unwrap(),
        1,
        &cancellation,
    )
    .unwrap();
    let mut writer = LiveWriter::open(path, transaction_budget(), &cancellation).unwrap();
    for &(name, ranges) in feeds {
        let ranges = ranges
            .iter()
            .map(|&(from, to)| AddressRange {
                from: Ipv4Key(from),
                to: Ipv4Key(to),
            })
            .collect::<Vec<_>>();
        let mut operation = writer
            .begin_create_feed(FeedName::new(name).unwrap(), &cancellation)
            .unwrap();
        operation.add_ranges_v4_slice(&ranges).unwrap();
        match operation.finish_input().unwrap() {
            FinishedWorkflow::Changed(prepared) => {
                prepared.commit().unwrap();
            }
            FinishedWorkflow::NoChange(_) => panic!("new feed did not change catalog"),
        }
    }
    writer.close().unwrap();
}

fn abi_path(path: &FsPath) -> Path {
    let bytes = path.as_os_str().as_bytes();
    Path {
        kind: registry::PATH_POSIX_BYTES,
        reserved: 0,
        pointer: bytes.as_ptr().cast(),
        length: bytes.len() as u64,
    }
}

fn bytes(value: &[u8]) -> ByteSlice {
    ByteSlice {
        pointer: value.as_ptr(),
        length: value.len() as u64,
    }
}

fn assert_ok(status: u32, error: *mut ErrorHandle) {
    if status == registry::STATUS_OK {
        assert!(error.is_null());
        return;
    }
    panic!("ABI call failed with status {status} and error {error:p}");
}

#[cfg(not(target_os = "freebsd"))]
unsafe fn open_live(path: &FsPath) -> *mut ReaderHandle {
    let mut reader = std::ptr::null_mut();
    let mut error = std::ptr::null_mut();
    let status = unsafe {
        crate::lifecycle::iprange_v4_abi1_open_live_reader(
            abi_path(path),
            Cancellation::default(),
            &mut reader,
            &mut error,
        )
    };
    assert_ok(status, error);
    reader
}

#[cfg(not(target_os = "freebsd"))]
unsafe fn all_scope(reader: *const ReaderHandle) -> *mut MembershipScopeHandle {
    let budget = MembershipQueryBudget {
        abi_version: 1,
        struct_size: size_of::<MembershipQueryBudget>() as u32,
        max_heap_bytes: 2 * 1024 * 1024,
    };
    let mut scope = std::ptr::null_mut();
    let mut error = std::ptr::null_mut();
    let status = unsafe {
        crate::query::iprange_v4_abi1_reader_all_feeds_scope(
            reader,
            &budget,
            Cancellation::default(),
            &mut scope,
            &mut error,
        )
    };
    assert_ok(status, error);
    scope
}

#[cfg(not(target_os = "freebsd"))]
unsafe extern "C" fn collect_names(
    context: *mut c_void,
    records: *const FeedNameValue,
    count: u64,
    _failure: *mut CallbackFailure,
) -> u32 {
    // SAFETY: the test supplies this context and the ABI lends this exact batch.
    let output = unsafe { &mut *context.cast::<Vec<String>>() };
    let records = unsafe { std::slice::from_raw_parts(records, count as usize) };
    for record in records {
        output.push(
            std::str::from_utf8(&record.bytes[..record.length as usize])
                .unwrap()
                .to_owned(),
        );
    }
    registry::SINK_OUTCOME_CONTINUE
}

#[test]
#[cfg(not(target_os = "freebsd"))]
fn c_algebra_retains_scopes_and_reuses_the_rust_scan_and_publisher() {
    let mut files = Files::new();
    let a_path = files.path("algebra-a");
    let b_path = files.path("algebra-b");
    let output_path = files.path("algebra-output");
    create_membership(&a_path, &[("x", &[(0, 9)]), ("y", &[(20, 29)])]);
    create_membership(&b_path, &[("y", &[(5, 14)]), ("z", &[(8, 22)])]);

    // SAFETY: every handle and pointer below is created and consumed by this test.
    unsafe {
        let a_reader = open_live(&a_path);
        let b_reader = open_live(&b_path);
        let a_scope = all_scope(a_reader);
        let b_scope = all_scope(b_reader);
        let scopes = [a_scope.cast_const(), b_scope.cast_const()];
        let budget = AbiAlgebraBudget {
            abi_version: 1,
            struct_size: size_of::<AbiAlgebraBudget>() as u32,
            max_heap_bytes: 8 * 1024 * 1024,
            max_sources: 2,
            reserved: 0,
        };
        let mut algebra: *mut MembershipAlgebraHandle = std::ptr::null_mut();
        let mut error = std::ptr::null_mut();
        let status = crate::algebra_ops::iprange_v4_abi1_membership_algebra_create(
            scopes.as_ptr(),
            scopes.len() as u64,
            &budget,
            Cancellation::default(),
            &mut algebra,
            &mut error,
        );
        assert_ok(status, error);

        assert_ok(
            crate::query::iprange_v4_abi1_membership_scope_close(a_scope, &mut error),
            error,
        );
        assert_ok(
            crate::query::iprange_v4_abi1_membership_scope_destroy(a_scope, &mut error),
            error,
        );
        assert_ok(
            crate::query::iprange_v4_abi1_membership_scope_close(b_scope, &mut error),
            error,
        );
        assert_ok(
            crate::query::iprange_v4_abi1_membership_scope_destroy(b_scope, &mut error),
            error,
        );

        let mut names = Vec::<String>::new();
        assert_ok(
            crate::algebra_ops::iprange_v4_abi1_membership_algebra_feeds(
                algebra,
                Some(collect_names),
                (&mut names as *mut Vec<String>).cast(),
                Cancellation::default(),
                &mut error,
            ),
            error,
        );
        assert_eq!(names, ["x", "y", "z"]);

        let y = [bytes(b"y")];
        let z = [bytes(b"z")];
        let y_selection = FeedSelectionInput {
            kind: registry::FEED_SELECTION_NAMED,
            reserved: 0,
            names: y.as_ptr(),
            name_count: 1,
        };
        let z_selection = FeedSelectionInput {
            kind: registry::FEED_SELECTION_NAMED,
            reserved: 0,
            names: z.as_ptr(),
            name_count: 1,
        };
        let mut count = AlgebraCountReport::default();
        assert_ok(
            crate::algebra_ops::iprange_v4_abi1_membership_algebra_count(
                algebra,
                y_selection,
                Cancellation::default(),
                &mut count,
                &mut error,
            ),
            error,
        );
        assert_eq!(
            count.addresses,
            crate::report::cardinality(Cardinality129::from_u64(20))
        );

        let mut comparison = AlgebraComparisonReport::default();
        assert_ok(
            crate::algebra_ops::iprange_v4_abi1_membership_algebra_compare(
                algebra,
                y_selection,
                z_selection,
                Cancellation::default(),
                &mut comparison,
                &mut error,
            ),
            error,
        );
        assert_eq!(
            comparison.overlap_addresses,
            crate::report::cardinality(Cardinality129::from_u64(10))
        );
        assert_eq!(comparison.equal, 0);

        let operation = AlgebraSetOperationInput {
            kind: registry::ALGEBRA_SET_UNION,
            reserved: 0,
            included: FeedSelectionInput {
                kind: registry::FEED_SELECTION_ALL,
                reserved: 0,
                names: std::ptr::null(),
                name_count: 0,
            },
            excluded: FeedSelectionInput::default(),
        };
        let mode = AlgebraOutputModeInput {
            kind: registry::ALGEBRA_OUTPUT_PRESERVE_FEEDS,
            reserved: 0,
            flat_name: ByteSlice::default(),
        };
        let output_budget = AlgebraOutputBudget {
            abi_version: 1,
            struct_size: size_of::<AlgebraOutputBudget>() as u32,
            max_output_pages: 20_000,
            max_open_files: 3,
            reserved: 0,
        };
        let mut semantic = AlgebraSetReport::default();
        let mut publication: *mut ReportHandle = std::ptr::null_mut();
        assert_ok(
            crate::algebra_ops::iprange_v4_abi1_membership_algebra_publish_set(
                algebra,
                abi_path(&output_path),
                bytes(b"union"),
                operation,
                mode,
                OptionalByteSlice::default(),
                registry::DESTINATION_POLICY_FAIL_IF_EXISTS,
                &output_budget,
                Cancellation::default(),
                &mut semantic,
                &mut publication,
                &mut error,
            ),
            error,
        );
        assert_eq!(semantic.output_feed_count, 3);
        assert_eq!(
            semantic.output_addresses,
            crate::report::cardinality(Cardinality129::from_u64(30))
        );
        assert_ok(
            crate::report::iprange_v4_abi1_report_destroy(publication, &mut error),
            error,
        );

        let output = ImmutableReader::open(&output_path).unwrap();
        assert_eq!(output.info().active_feed_count, 3);

        assert_ok(
            crate::algebra_ops::iprange_v4_abi1_membership_algebra_close(algebra, &mut error),
            error,
        );
        assert_ok(
            crate::algebra_ops::iprange_v4_abi1_membership_algebra_destroy(algebra, &mut error),
            error,
        );
        assert_ok(
            crate::lifecycle::iprange_v4_abi1_reader_close(a_reader, &mut error),
            error,
        );
        assert_ok(
            crate::lifecycle::iprange_v4_abi1_reader_destroy(a_reader, &mut error),
            error,
        );
        assert_ok(
            crate::lifecycle::iprange_v4_abi1_reader_close(b_reader, &mut error),
            error,
        );
        assert_ok(
            crate::lifecycle::iprange_v4_abi1_reader_destroy(b_reader, &mut error),
            error,
        );
    }
}

#[path = "tests_sdk/query.rs"]
#[cfg(not(target_os = "freebsd"))]
mod query;
#[path = "tests_sdk/workflows.rs"]
mod workflows;
