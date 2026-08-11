//! Runtime syscall probe driven by `check-mmap-runtime.sh`.

use std::path::PathBuf;

use crate::contract::MetaV4;
use crate::recovery::{
    inspect_recovery_candidates, recover_immutable, RecoveryBudget, RecoveryInspectionMode,
    RecoverySinkControl, RecoveryUnknownEnvelope, RuntimeProbeScratch as Scratch,
    RUNTIME_PROBE_HEADER_SIZE as HEADER_SIZE,
};
use crate::snapshot::{snapshot_to, SnapshotBudget, SnapshotPublicationPolicy, SnapshotSourceMode};
use crate::validation::{
    validate, ValidationBudget, ValidationFinding, ValidationMode, ValidationSinkControl,
};
use crate::{
    create_live, AddressFamily, CancellationToken, CreationState, ImmutableReader, Ipv4Key,
    LiveReader, LiveWriter, RangeDirection, TransactionBudget, ValueKind, ValueTag,
};

#[test]
#[ignore = "run through check-mmap-runtime.sh"]
fn persistent_storage_uses_mappings_only() {
    let directory = PathBuf::from(
        std::env::var_os("IPRANGE_V4_MMAP_PROBE_DIR")
            .expect("runtime mmap probe directory is required"),
    );
    std::fs::create_dir_all(&directory).unwrap();
    let live = directory.join("live.v4");
    let snapshot = directory.join("snapshot.v4");
    let recovered = directory.join("recovered.v4");
    let scratch_directory = directory.join("scratch");
    std::fs::create_dir_all(&scratch_directory).unwrap();
    let cancellation = CancellationToken::new();

    let created = create_live(
        &live,
        AddressFamily::Ipv4,
        ValueKind::Direct,
        crate::contract::StructureKind::None,
        ValueTag::FIRST_SEEN,
        4,
        &cancellation,
    )
    .unwrap();
    assert_eq!(created.state, CreationState::Created);

    let mut writer = LiveWriter::open(&live, transaction_budget(), &cancellation).unwrap();
    let mut transaction = writer.begin_direct_transaction(&cancellation).unwrap();
    transaction.assign_v4(Ipv4Key(10), Ipv4Key(30), 11).unwrap();
    transaction.assign_v4(Ipv4Key(20), Ipv4Key(25), 12).unwrap();
    transaction.commit().unwrap();
    let mut transaction = writer.begin_direct_transaction(&cancellation).unwrap();
    transaction.clear_v4(Ipv4Key(10), Ipv4Key(12)).unwrap();
    transaction.assign_v4(Ipv4Key(40), Ipv4Key(49), 13).unwrap();
    transaction.commit().unwrap();
    let _ = writer.reclaim(16, 10_000, &cancellation).unwrap();
    writer.close().unwrap();

    let mut reader = LiveReader::open(&live, &cancellation).unwrap();
    assert_eq!(reader.lookup_direct_v4(Ipv4Key(21)).unwrap(), Some(12));
    let ranges = {
        let mut cursor = reader.direct_cursor_v4(RangeDirection::Forward).unwrap();
        let mut ranges = 0u64;
        while cursor.next_range().unwrap().is_some() {
            ranges += 1;
        }
        ranges
    };
    assert!(ranges >= 2);
    reader.close().unwrap();

    let validation = validate(
        &live,
        ValidationMode::LiveCurrent,
        &ValidationBudget::heap_only(4 * 1024 * 1024, 2),
        &cancellation,
        &mut continue_validation,
    )
    .unwrap();
    assert!(validation.valid);

    snapshot_to(
        &live,
        SnapshotSourceMode::Live,
        &snapshot,
        SnapshotPublicationPolicy::FailIfExists,
        &SnapshotBudget::new(4 * 1024 * 1024, 10_000, 3),
        &cancellation,
    )
    .unwrap();

    let inspection = inspect_recovery_candidates(
        &snapshot,
        RecoveryInspectionMode::Immutable,
        &ValidationBudget::heap_only(4 * 1024 * 1024, 1),
        &cancellation,
    )
    .unwrap();
    let candidate = *inspection.candidate(0).unwrap();
    recover_immutable(
        &snapshot,
        candidate,
        &recovered,
        &RecoveryBudget::heap_only(4 * 1024 * 1024, 10_000, 2),
        &mut continue_recovery,
        &cancellation,
    )
    .unwrap();
    let recovered_reader = ImmutableReader::open(&recovered).unwrap();
    assert_eq!(
        recovered_reader.lookup_direct_v4(Ipv4Key(21)).unwrap(),
        Some(12)
    );

    let mut scratch = Scratch::start(&scratch_directory, scratch_meta(), 16 * 1024, 2, 4).unwrap();
    let slot = scratch.create().unwrap();
    scratch.resize(slot, HEADER_SIZE + 64).unwrap();
    scratch.write(slot, HEADER_SIZE, b"mapped scratch").unwrap();
    let mut bytes = [0u8; 14];
    scratch.read(slot, HEADER_SIZE, &mut bytes).unwrap();
    assert_eq!(&bytes, b"mapped scratch");
    assert!(scratch.cleanup().clean());
}

fn continue_validation(_: &ValidationFinding) -> crate::Result<ValidationSinkControl> {
    Ok(ValidationSinkControl::Continue)
}

fn continue_recovery(_: &RecoveryUnknownEnvelope) -> crate::Result<RecoverySinkControl> {
    Ok(RecoverySinkControl::Continue)
}

fn transaction_budget() -> TransactionBudget {
    TransactionBudget {
        max_heap_bytes: 4 * 1024 * 1024,
        max_private_pages: 10_000,
        max_file_growth_pages: 10_000,
        max_open_files: 2,
    }
}

fn scratch_meta() -> MetaV4 {
    MetaV4 {
        address_family: AddressFamily::Ipv4,
        value_kind: ValueKind::Direct,
        structure_kind_code: crate::contract::StructureKind::None as u8,
        value_tag: ValueTag::FIRST_SEEN,
        database_id: [0x31; 16],
        txn_id: 7,
        commit_nonce: [0x32; 16],
        page_count: 2,
        range_record_count: 0,
        active_feed_count: 0,
        feed_index_limit: 0,
        membership_entry_count: 0,
        membership_id_limit: 0,
        metadata_uncompressed_len: 0,
        metadata_compressed_len: 0,
        retired_extent_count: 0,
        range_root: 0,
        catalog_name_root: 0,
        catalog_index_root: 0,
        feed_used_root: 0,
        membership_id_root: 0,
        membership_hash_root: 0,
        membership_used_root: 0,
        metadata_root: 0,
        free_bitmap_root: 0,
        retirement_root: 0,
        allocator_reserve: [0; 4],
        structure_entry_count: 0,
        structure_id_limit: 0,
        structure_id_root: 0,
        structure_hash_root: 0,
        structure_used_root: 0,
    }
}
