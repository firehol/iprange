#[cfg(any(target_os = "linux", target_vendor = "apple"))]
use std::ffi::c_void;
use std::fs;
use std::os::unix::ffi::OsStrExt;
use std::path::PathBuf;
use std::time::{SystemTime, UNIX_EPOCH};

#[cfg(any(target_os = "linux", target_vendor = "apple"))]
use iprange_livedb::{create_live, AddressFamily, CancellationToken, ValueKind, ValueTag};

#[cfg(target_os = "freebsd")]
use crate::abi::ByteSlice;
#[cfg(any(target_os = "linux", target_vendor = "apple"))]
use crate::abi::{CallbackFailure, DirectRange, FinishInputReport, TransactionBudget};
use crate::abi::{Cancellation, Ip, Path};
use crate::error::{BoundaryError, ErrorHandle};
#[cfg(any(target_os = "linux", target_vendor = "apple"))]
use crate::handle::{ReaderHandle, WriterHandle};
#[cfg(any(target_os = "linux", target_vendor = "apple"))]
use crate::ip::{self, Key};
#[cfg(any(target_os = "linux", target_vendor = "apple"))]
use crate::report::ReportHandle;

const GENERATED_HEADER: &[u8] = include_bytes!(concat!(env!("OUT_DIR"), "/iprange_v4.h"));
const COMMITTED_HEADER: &[u8] = include_bytes!("../include/iprange_v4.h");

struct Files {
    main: PathBuf,
}

impl Files {
    fn new() -> Self {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        Self {
            main: std::env::temp_dir()
                .join(format!("iprange-capi-test-{}-{unique}", std::process::id())),
        }
    }

    fn sidecar(&self) -> PathBuf {
        let mut name = self.main.file_name().unwrap().to_os_string();
        name.push(".readers");
        self.main.with_file_name(name)
    }

    fn abi_path(&self) -> Path {
        let bytes = self.main.as_os_str().as_bytes();
        Path {
            kind: 1,
            reserved: 0,
            pointer: bytes.as_ptr().cast(),
            length: bytes.len() as u64,
        }
    }
}

impl Drop for Files {
    fn drop(&mut self) {
        let _ = fs::remove_file(&self.main);
        let _ = fs::remove_file(self.sidecar());
        let _ = fs::remove_file(self.sidecar().with_extension("readers.saved"));
    }
}

#[derive(Default)]
#[cfg(any(target_os = "linux", target_vendor = "apple"))]
struct DirectSource {
    emitted: bool,
}

#[cfg(any(target_os = "linux", target_vendor = "apple"))]
unsafe extern "C" fn direct_source(
    context: *mut c_void,
    records: *mut DirectRange,
    capacity: u64,
    count: *mut u64,
    _failure: *mut CallbackFailure,
) -> u32 {
    // SAFETY: the test passes this exact context and the ABI lends valid outputs.
    let source = unsafe { &mut *context.cast::<DirectSource>() };
    if source.emitted {
        unsafe { *count = 0 };
        return 2;
    }
    assert!(capacity >= 2);
    let records = unsafe { std::slice::from_raw_parts_mut(records, capacity as usize) };
    records[0] = DirectRange {
        range: crate::abi::Range {
            from: ip::encode(Key::V4(iprange_livedb::Ipv4Key(10))),
            to: ip::encode(Key::V4(iprange_livedb::Ipv4Key(20))),
        },
        value: 7,
        reserved: 0,
    };
    records[1] = DirectRange {
        range: crate::abi::Range {
            from: ip::encode(Key::V4(iprange_livedb::Ipv4Key(15))),
            to: ip::encode(Key::V4(iprange_livedb::Ipv4Key(17))),
        },
        value: 9,
        reserved: 0,
    };
    source.emitted = true;
    unsafe { *count = 2 };
    1
}

#[cfg(any(target_os = "linux", target_vendor = "apple"))]
struct ReentrantSource {
    writer: *const WriterHandle,
    status: u32,
    changed: u8,
    code_status: u32,
    code: u32,
    destroy_status: u32,
}

#[cfg(any(target_os = "linux", target_vendor = "apple"))]
unsafe extern "C" fn reentrant_source(
    context: *mut c_void,
    _records: *mut DirectRange,
    _capacity: u64,
    count: *mut u64,
    _failure: *mut CallbackFailure,
) -> u32 {
    // SAFETY: the test passes this exact context and the ABI lends a valid count.
    let source = unsafe { &mut *context.cast::<ReentrantSource>() };
    unsafe { *count = 0 };
    let mut failure = std::ptr::null_mut();
    source.changed = u8::MAX;
    source.status = unsafe {
        crate::writer::iprange_v4_abi1_writer_clear_metadata_json(
            source.writer,
            Cancellation::default(),
            &mut source.changed,
            &mut failure,
        )
    };
    if !failure.is_null() {
        let mut caller_present = 0;
        let mut caller_code = 0;
        source.code_status = unsafe {
            crate::error::iprange_v4_abi1_error_code(
                failure,
                &mut source.code,
                &mut caller_present,
                &mut caller_code,
            )
        };
        source.destroy_status = unsafe { crate::error::iprange_v4_abi1_error_destroy(failure) };
    }
    2
}

#[cfg(any(target_os = "linux", target_vendor = "apple"))]
fn assert_ok(status: u32, error: *mut ErrorHandle) {
    if status == 0 {
        assert!(error.is_null());
        return;
    }
    panic!("ABI call failed with status {status} and error {error:p}");
}

unsafe fn error_code(error: *mut ErrorHandle) -> u32 {
    let mut code = u32::MAX;
    let mut caller_code_present = u8::MAX;
    let mut caller_code = u64::MAX;
    let status = unsafe {
        crate::error::iprange_v4_abi1_error_code(
            error,
            &mut code,
            &mut caller_code_present,
            &mut caller_code,
        )
    };
    assert_eq!(status, 0);
    assert_eq!(caller_code_present, 0);
    assert_eq!(caller_code, 0);
    code
}

unsafe fn destroy_error(error: *mut ErrorHandle) {
    assert_eq!(
        unsafe { crate::error::iprange_v4_abi1_error_destroy(error) },
        0
    );
}

#[test]
#[cfg(any(target_os = "linux", target_vendor = "apple"))]
fn exported_direct_workflow_round_trips_through_c_shapes() {
    let files = Files::new();
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::new(b"asn").unwrap(),
        2,
        &CancellationToken::new(),
    )
    .unwrap();

    let budget = TransactionBudget {
        abi_version: 1,
        struct_size: std::mem::size_of::<TransactionBudget>() as u32,
        max_heap_bytes: 2 * 1024 * 1024,
        max_private_pages: 20_000,
        max_file_growth_pages: 20_000,
        max_open_files: 2,
        reserved: 0,
    };
    let mut writer: *mut WriterHandle = std::ptr::null_mut();
    let mut error: *mut ErrorHandle = std::ptr::null_mut();
    let status = unsafe {
        crate::lifecycle::iprange_v4_abi1_open_live_writer(
            files.abi_path(),
            &budget,
            Cancellation::default(),
            &mut writer,
            &mut error,
        )
    };
    assert_ok(status, error);

    let status = unsafe {
        crate::workflow::iprange_v4_abi1_writer_begin_direct_replacement(
            writer,
            Cancellation::default(),
            &mut error,
        )
    };
    assert_ok(status, error);
    let mut source = DirectSource::default();
    let status = unsafe {
        crate::workflow::iprange_v4_abi1_writer_add_direct_ranges(
            writer,
            Some(direct_source),
            (&mut source as *mut DirectSource).cast(),
            &mut error,
        )
    };
    assert_ok(status, error);

    let mut finish: *mut ReportHandle = std::ptr::null_mut();
    let status = unsafe {
        crate::workflow::iprange_v4_abi1_writer_finish_input(writer, &mut finish, &mut error)
    };
    assert_ok(status, error);
    let mut finish_value = FinishInputReport::default();
    let status = unsafe {
        crate::report::iprange_v4_abi1_report_get_finish_input(
            finish,
            &mut finish_value,
            &mut error,
        )
    };
    assert_ok(status, error);
    assert_eq!(finish_value.input_record_count, 2);
    assert_eq!(finish_value.after_range_record_count, 3);
    unsafe { crate::report::iprange_v4_abi1_report_destroy(finish, &mut error) };

    let mut commit: *mut ReportHandle = std::ptr::null_mut();
    let status =
        unsafe { crate::writer::iprange_v4_abi1_writer_commit(writer, &mut commit, &mut error) };
    assert_ok(status, error);
    unsafe { crate::report::iprange_v4_abi1_report_destroy(commit, &mut error) };

    let mut close: *mut ReportHandle = std::ptr::null_mut();
    let status =
        unsafe { crate::writer::iprange_v4_abi1_writer_close(writer, &mut close, &mut error) };
    assert_ok(status, error);
    unsafe { crate::report::iprange_v4_abi1_report_destroy(close, &mut error) };
    let status = unsafe { crate::lifecycle::iprange_v4_abi1_writer_destroy(writer, &mut error) };
    assert_ok(status, error);

    let mut reader: *mut ReaderHandle = std::ptr::null_mut();
    let status = unsafe {
        crate::lifecycle::iprange_v4_abi1_open_live_reader(
            files.abi_path(),
            Cancellation::default(),
            &mut reader,
            &mut error,
        )
    };
    assert_ok(status, error);
    for (address, expected) in [(14, 7), (16, 9), (18, 7)] {
        let mut present = 0;
        let mut value = 0;
        let status = unsafe {
            crate::reader::iprange_v4_abi1_reader_lookup_direct(
                reader,
                ip::encode(Key::V4(iprange_livedb::Ipv4Key(address))),
                &mut present,
                &mut value,
                &mut error,
            )
        };
        assert_ok(status, error);
        assert_eq!((present, value), (1, expected));
    }
    let status = unsafe { crate::lifecycle::iprange_v4_abi1_reader_close(reader, &mut error) };
    assert_ok(status, error);
    let status = unsafe { crate::lifecycle::iprange_v4_abi1_reader_destroy(reader, &mut error) };
    assert_ok(status, error);
}

#[test]
fn fixed_cardinality_layout_and_version_are_exact() {
    assert_eq!(crate::export::iprange_v4_abi1_version(), 1);
    assert_eq!(std::mem::size_of::<crate::abi::Cardinality129>(), 24);
    assert_eq!(std::mem::align_of::<crate::abi::Cardinality129>(), 8);
    assert_eq!(std::mem::size_of::<Ip>(), 20);
}

#[cfg(target_os = "freebsd")]
#[test]
fn live_creation_is_rejected_before_c_artifacts() {
    let files = Files::new();
    let tag = b"asn";
    let mut report = std::ptr::null_mut();
    let mut error = std::ptr::null_mut();
    let status = unsafe {
        crate::lifecycle_ops::iprange_v4_abi1_create_live(
            files.abi_path(),
            4,
            1,
            ByteSlice {
                pointer: tag.as_ptr(),
                length: tag.len() as u64,
            },
            1,
            Cancellation::default(),
            &mut report,
            &mut error,
        )
    };

    assert_eq!(status, 1);
    assert!(report.is_null());
    assert!(!error.is_null());
    assert_eq!(unsafe { error_code(error) }, 44);
    unsafe { destroy_error(error) };
    assert!(!files.main.exists());
    assert!(!files.sidecar().exists());
}

#[test]
#[cfg(any(target_os = "linux", target_vendor = "apple"))]
fn reader_close_failure_keeps_the_c_handle_retryable() {
    let files = Files::new();
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::new(b"asn").unwrap(),
        1,
        &CancellationToken::new(),
    )
    .unwrap();

    let mut reader = std::ptr::null_mut();
    let mut error = std::ptr::null_mut();
    let status = unsafe {
        crate::lifecycle::iprange_v4_abi1_open_live_reader(
            files.abi_path(),
            Cancellation::default(),
            &mut reader,
            &mut error,
        )
    };
    assert_ok(status, error);

    let saved = files.sidecar().with_extension("readers.saved");
    fs::rename(files.sidecar(), &saved).unwrap();
    let status = unsafe { crate::lifecycle::iprange_v4_abi1_reader_close(reader, &mut error) };
    assert_ne!(status, 0);
    assert!(!error.is_null());
    unsafe { destroy_error(error) };
    error = std::ptr::null_mut();

    fs::rename(&saved, files.sidecar()).unwrap();
    let status = unsafe { crate::lifecycle::iprange_v4_abi1_reader_close(reader, &mut error) };
    assert_ok(status, error);
    let status = unsafe { crate::lifecycle::iprange_v4_abi1_reader_destroy(reader, &mut error) };
    assert_ok(status, error);
}

#[test]
fn residue_operation_registry_is_unambiguous() {
    let values = [
        crate::registry::RESIDUE_OPERATION_INSPECT_PUBLICATION,
        crate::registry::RESIDUE_OPERATION_REMOVE_PUBLICATION,
        crate::registry::RESIDUE_OPERATION_LIST_ABANDONED_SCRATCH,
        crate::registry::RESIDUE_OPERATION_REMOVE_ABANDONED_SCRATCH,
        crate::registry::RESIDUE_OPERATION_LIST_ABANDONED_PUBLICATION_TEMPS,
        crate::registry::RESIDUE_OPERATION_REMOVE_ABANDONED_PUBLICATION_TEMP,
        crate::registry::RESIDUE_OPERATION_LIST_ABANDONED_RESERVATION_ARTIFACTS,
        crate::registry::RESIDUE_OPERATION_REMOVE_ABANDONED_RESERVATION_ARTIFACT,
        crate::registry::RESIDUE_OPERATION_LIST_HOUSEKEEPING_ARTIFACTS,
        crate::registry::RESIDUE_OPERATION_REMOVE_HOUSEKEEPING_ARTIFACT,
        crate::registry::RESIDUE_OPERATION_SNAPSHOT_PREPARATION_FAILURE,
    ];
    let unique = values
        .into_iter()
        .collect::<std::collections::BTreeSet<_>>();
    assert_eq!(unique.len(), values.len());
    assert_eq!(unique, (1..=11).collect());
}

#[test]
fn committed_header_matches_the_rust_boundary() {
    assert_eq!(COMMITTED_HEADER, GENERATED_HEADER);
    let header = std::str::from_utf8(GENERATED_HEADER).unwrap();
    assert_eq!(
        header
            .match_indices("IPRANGE_V4_ABI1_API IPRANGE_V4_ABI1_CALL\n")
            .count(),
        136
    );
    assert_eq!(
        header
            .lines()
            .filter(|line| {
                line.strip_prefix("#define IPRANGE_V4_ABI1_")
                    .and_then(|value| value.split_once(' '))
                    .is_some_and(|(_, value)| value.parse::<u32>().is_ok())
            })
            .count(),
        292
    );
}

#[test]
fn failing_calls_clear_outputs_and_reject_wrong_handle_kinds() {
    let original = Box::into_raw(Box::new(ErrorHandle::from(
        BoundaryError::invalid_argument("test handle"),
    )));
    let mut present = u8::MAX;
    let mut value = u32::MAX;
    let mut failure = std::ptr::null_mut();
    let status = unsafe {
        crate::reader::iprange_v4_abi1_reader_lookup_direct(
            original.cast(),
            Ip::default(),
            &mut present,
            &mut value,
            &mut failure,
        )
    };
    assert_eq!(status, 1);
    assert_eq!((present, value), (0, 0));
    assert_eq!(unsafe { error_code(failure) }, 8);
    unsafe {
        destroy_error(failure);
        destroy_error(original);
    }
}

#[test]
fn overlapping_outputs_fail_before_handle_access() {
    let mut shared = u64::MAX;
    let present = (&mut shared as *mut u64).cast::<u8>();
    let value = (&mut shared as *mut u64).cast::<u32>();
    let mut failure = std::ptr::null_mut();
    let status = unsafe {
        crate::reader::iprange_v4_abi1_reader_lookup_direct(
            std::ptr::null(),
            Ip::default(),
            present,
            value,
            &mut failure,
        )
    };
    assert_eq!(status, 1);
    assert_eq!(shared, u64::MAX);
    assert_eq!(unsafe { error_code(failure) }, 1);
    unsafe { destroy_error(failure) };
}

#[test]
#[cfg(any(target_os = "linux", target_vendor = "apple"))]
fn callbacks_cannot_reenter_the_same_writer() {
    let files = Files::new();
    create_live(
        &files.main,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::new(b"asn").unwrap(),
        2,
        &CancellationToken::new(),
    )
    .unwrap();
    let budget = TransactionBudget {
        abi_version: 1,
        struct_size: std::mem::size_of::<TransactionBudget>() as u32,
        max_heap_bytes: 2 * 1024 * 1024,
        max_private_pages: 20_000,
        max_file_growth_pages: 20_000,
        max_open_files: 2,
        reserved: 0,
    };
    let mut writer = std::ptr::null_mut();
    let mut error = std::ptr::null_mut();
    assert_ok(
        unsafe {
            crate::lifecycle::iprange_v4_abi1_open_live_writer(
                files.abi_path(),
                &budget,
                Cancellation::default(),
                &mut writer,
                &mut error,
            )
        },
        error,
    );
    assert_ok(
        unsafe {
            crate::workflow::iprange_v4_abi1_writer_begin_direct_replacement(
                writer,
                Cancellation::default(),
                &mut error,
            )
        },
        error,
    );

    let mut source = ReentrantSource {
        writer,
        status: u32::MAX,
        changed: u8::MAX,
        code_status: u32::MAX,
        code: u32::MAX,
        destroy_status: u32::MAX,
    };
    assert_ok(
        unsafe {
            crate::workflow::iprange_v4_abi1_writer_add_direct_ranges(
                writer,
                Some(reentrant_source),
                (&mut source as *mut ReentrantSource).cast(),
                &mut error,
            )
        },
        error,
    );
    assert_eq!(source.status, 1);
    assert_eq!(source.changed, 0);
    assert_eq!(source.code_status, 0);
    assert_eq!(source.code, 10);
    assert_eq!(source.destroy_status, 0);

    let mut report = std::ptr::null_mut();
    assert_ok(
        unsafe { crate::writer::iprange_v4_abi1_writer_abort(writer, &mut report, &mut error) },
        error,
    );
    assert_ok(
        unsafe { crate::report::iprange_v4_abi1_report_destroy(report, &mut error) },
        error,
    );
    report = std::ptr::null_mut();
    assert_ok(
        unsafe { crate::writer::iprange_v4_abi1_writer_close(writer, &mut report, &mut error) },
        error,
    );
    assert_ok(
        unsafe { crate::report::iprange_v4_abi1_report_destroy(report, &mut error) },
        error,
    );
    assert_ok(
        unsafe { crate::lifecycle::iprange_v4_abi1_writer_destroy(writer, &mut error) },
        error,
    );
}
