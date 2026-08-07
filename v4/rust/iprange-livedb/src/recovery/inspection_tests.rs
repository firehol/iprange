use std::fs;
use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use super::*;
use crate::contract::MetaV4;
use crate::mapping::test_support as file_io;
use crate::{
    create_live, AddressFamily, CancellationToken, ErrorCode, Ipv4Key, LiveWriter,
    TransactionBudget, ValueKind, ValueTag,
};

struct TestFile(PathBuf);

impl TestFile {
    fn new(label: &str) -> Self {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        Self(std::env::temp_dir().join(format!(
            "iprange-v4-recovery-inspection-{label}-{}-{unique}",
            std::process::id()
        )))
    }
}

impl Drop for TestFile {
    fn drop(&mut self) {
        let _ = fs::remove_file(&self.0);
        if let Ok(sidecar) = crate::path::canonical_sidecar(&self.0) {
            let _ = fs::remove_file(sidecar);
        }
    }
}

#[test]
#[cfg(any(target_os = "linux", target_vendor = "apple", windows))]
fn live_inspection_reports_unprovable_current_order() {
    let source = create_source("unprovable");
    rewrite_meta(&source.0, 0, |meta| meta.commit_nonce = [0x55; 16]);

    let error = inspect_recovery_candidates(
        &source.0,
        RecoveryInspectionMode::Live,
        &ValidationBudget::heap_only(0, 2),
        &CancellationToken::new(),
    )
    .unwrap_err();

    assert_eq!(
        error.code(),
        ErrorCode::LiveRecoveryCurrentGenerationUnprovable
    );
}

#[test]
#[cfg(any(target_os = "linux", target_vendor = "apple", windows))]
fn live_inspection_reports_unreadable_proven_current() {
    let source = create_source("unreadable");
    let mut writer = LiveWriter::open(
        &source.0,
        transaction_budget(),
        &crate::CancellationToken::new(),
    )
    .unwrap();
    let cancellation = CancellationToken::new();
    let mut transaction = writer.begin_direct_transaction(&cancellation).unwrap();
    transaction.assign_v4(Ipv4Key(10), Ipv4Key(20), 7).unwrap();
    transaction.commit().unwrap();
    writer.close().unwrap();
    rewrite_meta(&source.0, 0, |meta| meta.range_record_count = 0);

    let error = inspect_recovery_candidates(
        &source.0,
        RecoveryInspectionMode::Live,
        &ValidationBudget::heap_only(0, 2),
        &CancellationToken::new(),
    )
    .unwrap_err();

    assert_eq!(
        error.code(),
        ErrorCode::LiveRecoveryCurrentGenerationUnreadable
    );
}

fn create_source(label: &str) -> TestFile {
    let source = TestFile::new(label);
    create_live(
        &source.0,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        ValueTag::RETENTION,
        2,
        &crate::CancellationToken::new(),
    )
    .unwrap();
    source
}

fn rewrite_meta(path: &Path, page_index: u64, change: impl FnOnce(&mut MetaV4)) {
    let file = live_sidecar::open_rw(path).unwrap();
    let mut page = [0; PAGE_SIZE];
    file_io::read_exact_at(&file, &mut page, page_index * PAGE_SIZE as u64).unwrap();
    let mut meta = MetaV4::decode_unchecked(&page).unwrap();
    change(&mut meta);
    meta.encode_into(&mut page);
    file_io::write_exact_at(&file, &page, page_index * PAGE_SIZE as u64).unwrap();
    file.sync_all().unwrap();
}

fn transaction_budget() -> TransactionBudget {
    TransactionBudget {
        max_heap_bytes: 1024 * 1024,
        max_private_pages: 100,
        max_file_growth_pages: 100,
        max_open_files: 2,
    }
}
