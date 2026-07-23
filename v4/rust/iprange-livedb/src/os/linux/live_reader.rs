//! Private Linux live-reader lifecycle over retained descriptors.

// Cleanup authority stays inline so an error after slot publication never
// needs a fallible heap allocation merely to return the retained descriptors.
#![allow(clippy::large_enum_variant, clippy::result_large_err)]

use core::marker::PhantomData;

use super::live_cleanup::{
    requires_cleanup, retry_any_cleanup, LinuxLiveCleanupError, LinuxLiveCleanupGuard,
    OwnedLiveClaim,
};
use super::*;
use crate::cardinality::Cardinality129;
use crate::key::IpKey;
use crate::range_page::RangeRecord;
use crate::range_reader::{RangeReadError, RangeTree};

#[derive(Debug)]
pub(crate) enum LinuxLiveReaderOpenCause {
    Pair(LinuxLivePairError),
    Slot(LinuxReaderSlotError),
    View(RangeReadError),
    Cancelled,
}

#[derive(Debug)]
pub(crate) enum LinuxLiveReaderOpenError {
    Failed {
        cause: LinuxLiveReaderOpenCause,
        cleanup_outcome: Option<LiveClaimCleanupOutcome>,
    },
    CleanupRequired {
        cause: LinuxLiveReaderOpenCause,
        cleanup: LinuxLiveCleanupError,
        guard: LinuxLiveCleanupGuard,
    },
}

#[derive(Debug)]
pub(crate) enum LinuxLiveReaderContentError {
    ForkedHandle,
    CloseRequired,
    Read(RangeReadError),
}

#[derive(Debug)]
pub(crate) enum LinuxLiveReaderCloseError {
    ForkedHandle,
    Cleanup(LinuxReaderSlotError),
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum ReaderState {
    Open,
    CleanupOnly,
    Closed,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum OpenStage {
    DeadWriterFound,
    ClaimPublished,
    PinPublished,
    BeforeUnlock,
    ViewSetup,
}

/// Established private live reader. Content is always copied from its retained
/// descriptor into range-reader-owned page buffers.
#[derive(Debug)]
pub(crate) struct LinuxLiveReader<K: IpKey> {
    files: Option<RetainedLiveFiles>,
    owned: Option<OwnedReaderSlot>,
    bootstrap: Bootstrap,
    creator_pid: u32,
    state: ReaderState,
    _key: PhantomData<K>,
}

impl<K: IpKey> LinuxLiveReader<K> {
    pub(crate) fn open(path: &Path) -> Result<Self, LinuxLiveReaderOpenError> {
        Self::open_with_cancel(path, || false)
    }

    pub(crate) fn open_with_cancel(
        path: &Path,
        cancelled: impl FnMut() -> bool,
    ) -> Result<Self, LinuxLiveReaderOpenError> {
        Self::open_with_hook(path, cancelled, |_, _, _| Ok(()))
    }

    fn open_with_hook(
        path: &Path,
        mut cancelled: impl FnMut() -> bool,
        mut hook: impl FnMut(
            OpenStage,
            &mut RetainedLiveFiles,
            Option<&OwnedReaderSlot>,
        ) -> Result<(), LinuxLiveReaderOpenCause>,
    ) -> Result<Self, LinuxLiveReaderOpenError> {
        let mut files =
            RetainedLiveFiles::open_locked_with_cancel(path, &mut cancelled).map_err(|cause| {
                LinuxLiveReaderOpenError::Failed {
                    cause: pair_open_cause(cause),
                    cleanup_outcome: None,
                }
            })?;

        loop {
            match files.scan_and_reap_with_cancel(observe_posix_process, &mut cancelled) {
                Ok(_) => break,
                Err(cause @ LinuxLivePairError::Scan(LinuxSidecarScanError::DeadWriter { .. })) => {
                    if let Err(hook_cause) = hook(OpenStage::DeadWriterFound, &mut files, None) {
                        return failed_with_possible_cleanup(files, None, hook_cause, None);
                    }
                    if cancelled() {
                        return failed_with_possible_cleanup(
                            files,
                            None,
                            LinuxLiveReaderOpenCause::Cancelled,
                            None,
                        );
                    }
                    match files.retry_dead_writer_cleanup() {
                        Ok(()) => continue,
                        Err(cleanup) => {
                            return failed_with_possible_cleanup(
                                files,
                                None,
                                LinuxLiveReaderOpenCause::Pair(cause),
                                Some(LinuxLiveCleanupError::Pair(cleanup)),
                            );
                        }
                    }
                }
                Err(cause) => {
                    return failed_with_possible_cleanup(files, None, pair_open_cause(cause), None);
                }
            }
        }

        if cancelled() {
            return Err(LinuxLiveReaderOpenError::Failed {
                cause: LinuxLiveReaderOpenCause::Cancelled,
                cleanup_outcome: None,
            });
        }

        let scanned = files
            .scanned_bootstrap()
            .expect("successful scan retains its selected bootstrap");
        if let Err(cause) = RangeTree::<K, _>::from_source(&files.main, scanned) {
            return Err(LinuxLiveReaderOpenError::Failed {
                cause: LinuxLiveReaderOpenCause::View(cause),
                cleanup_outcome: None,
            });
        }

        let mut owned = match files.claim_reader_slot() {
            Ok(owned) => owned,
            Err(cause) => {
                return failed_with_possible_cleanup(
                    files,
                    None,
                    LinuxLiveReaderOpenCause::Slot(cause),
                    None,
                );
            }
        };
        if let Err(cause) = hook(OpenStage::ClaimPublished, &mut files, Some(&owned)) {
            return failed_with_possible_cleanup(files, Some(owned), cause, None);
        }
        if cancelled() {
            return failed_with_possible_cleanup(
                files,
                Some(owned),
                LinuxLiveReaderOpenCause::Cancelled,
                None,
            );
        }

        let bootstrap = match files.pin_reader_slot(&mut owned) {
            Ok(bootstrap) => bootstrap,
            Err(cause) => {
                return failed_with_possible_cleanup(
                    files,
                    Some(owned),
                    LinuxLiveReaderOpenCause::Slot(cause),
                    None,
                );
            }
        };
        if let Err(cause) = hook(OpenStage::PinPublished, &mut files, Some(&owned)) {
            return failed_with_possible_cleanup(files, Some(owned), cause, None);
        }
        if cancelled() {
            return failed_with_possible_cleanup(
                files,
                Some(owned),
                LinuxLiveReaderOpenCause::Cancelled,
                None,
            );
        }

        if let Err(cause) = hook(OpenStage::BeforeUnlock, &mut files, Some(&owned)) {
            return failed_with_possible_cleanup(files, Some(owned), cause, None);
        }
        if let Err(cause) = files.release_reader_registration_lock(&owned, bootstrap) {
            return failed_with_possible_cleanup(
                files,
                Some(owned),
                LinuxLiveReaderOpenCause::Slot(cause),
                None,
            );
        }
        if cancelled() {
            return failed_with_possible_cleanup(
                files,
                Some(owned),
                LinuxLiveReaderOpenCause::Cancelled,
                None,
            );
        }

        if let Err(cause) = hook(OpenStage::ViewSetup, &mut files, Some(&owned)) {
            return failed_with_possible_cleanup(files, Some(owned), cause, None);
        }
        Ok(Self {
            files: Some(files),
            owned: Some(owned),
            bootstrap,
            creator_pid: std::process::id(),
            state: ReaderState::Open,
            _key: PhantomData,
        })
    }

    pub(crate) fn lookup(
        &self,
        target: K,
    ) -> Result<Option<RangeRecord<K>>, LinuxLiveReaderContentError> {
        let files = self.open_files()?;
        let tree = RangeTree::<K, _>::from_source(&files.main, self.bootstrap)
            .map_err(LinuxLiveReaderContentError::Read)?;
        tree.lookup(target)
            .map_err(LinuxLiveReaderContentError::Read)
    }

    pub(crate) fn count_addresses(&self) -> Result<Cardinality129, LinuxLiveReaderContentError> {
        let files = self.open_files()?;
        let tree = RangeTree::<K, _>::from_source(&files.main, self.bootstrap)
            .map_err(LinuxLiveReaderContentError::Read)?;
        tree.count_addresses()
            .map_err(LinuxLiveReaderContentError::Read)
    }

    pub(crate) fn close(
        &mut self,
    ) -> Result<Option<LiveClaimCleanupOutcome>, LinuxLiveReaderCloseError> {
        self.check_creator_for_close()?;
        if self.state == ReaderState::Closed {
            return Ok(None);
        }
        self.state = ReaderState::CleanupOnly;
        let outcome = self
            .files
            .as_mut()
            .expect("non-closed reader retains files")
            .retry_reader_slot_cleanup(self.owned.as_ref())
            .map_err(LinuxLiveReaderCloseError::Cleanup)?;
        self.owned = None;
        self.files = None;
        self.state = ReaderState::Closed;
        Ok(Some(outcome))
    }

    fn open_files(&self) -> Result<&RetainedLiveFiles, LinuxLiveReaderContentError> {
        if std::process::id() != self.creator_pid {
            return Err(LinuxLiveReaderContentError::ForkedHandle);
        }
        if self.state != ReaderState::Open {
            return Err(LinuxLiveReaderContentError::CloseRequired);
        }
        Ok(self.files.as_ref().expect("open reader retains files"))
    }

    fn check_creator_for_close(&self) -> Result<(), LinuxLiveReaderCloseError> {
        if std::process::id() != self.creator_pid {
            return Err(LinuxLiveReaderCloseError::ForkedHandle);
        }
        Ok(())
    }
}

fn failed_with_possible_cleanup<K: IpKey>(
    mut files: RetainedLiveFiles,
    owned: Option<OwnedReaderSlot>,
    cause: LinuxLiveReaderOpenCause,
    known_cleanup_error: Option<LinuxLiveCleanupError>,
) -> Result<LinuxLiveReader<K>, LinuxLiveReaderOpenError> {
    let owned = owned.map(OwnedLiveClaim::Reader);
    if !requires_cleanup(&files, owned.as_ref()) {
        let cleanup_outcome = known_cleanup_error
            .as_ref()
            .map(|_| files.live_cleanup_paths());
        return Err(LinuxLiveReaderOpenError::Failed {
            cause,
            cleanup_outcome,
        });
    }

    let cleanup = match known_cleanup_error {
        Some(cleanup) => Err(cleanup),
        None => retry_any_cleanup(&mut files, owned.as_ref()),
    };
    match cleanup {
        Ok(cleanup_outcome) => Err(LinuxLiveReaderOpenError::Failed {
            cause,
            cleanup_outcome: Some(cleanup_outcome),
        }),
        Err(cleanup) => Err(LinuxLiveReaderOpenError::CleanupRequired {
            cause,
            cleanup,
            guard: LinuxLiveCleanupGuard::new(files, owned),
        }),
    }
}

fn pair_open_cause(error: LinuxLivePairError) -> LinuxLiveReaderOpenCause {
    if matches!(
        &error,
        LinuxLivePairError::Os(LinuxOsError::Cancelled)
            | LinuxLivePairError::Scan(LinuxSidecarScanError::Cancelled)
            | LinuxLivePairError::Scan(LinuxSidecarScanError::Os(LinuxOsError::Cancelled))
    ) {
        LinuxLiveReaderOpenCause::Cancelled
    } else {
        LinuxLiveReaderOpenCause::Pair(error)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::bootstrap::tests::empty_direct_meta;
    use crate::key::{Ipv4Key, Ipv6Key};
    use crate::page::{write_crc32c, PageHeader, PageType, PAGE_HEADER_SIZE};
    use crate::sidecar::{encode_active_slot, SidecarOrigin};
    use std::cell::Cell;
    use std::io::Write;
    use std::os::unix::ffi::OsStrExt;
    use std::sync::atomic::{AtomicU64, Ordering};

    static NEXT_DIRECTORY: AtomicU64 = AtomicU64::new(1);

    #[derive(Debug)]
    struct TestDatabase {
        directory: PathBuf,
        main: PathBuf,
        sidecar: PathBuf,
        header: SidecarHeader,
    }

    impl Drop for TestDatabase {
        fn drop(&mut self) {
            let _ = std::fs::remove_dir_all(&self.directory);
        }
    }

    impl TestDatabase {
        fn new(physical_pages: usize, corrupt_leaf_crc: bool) -> Self {
            assert!(physical_pages >= 3);
            let ordinal = NEXT_DIRECTORY.fetch_add(1, Ordering::Relaxed);
            let directory = std::env::temp_dir().join(format!(
                "iprange-v4-live-reader-{}-{ordinal}",
                std::process::id()
            ));
            std::fs::create_dir(&directory).unwrap();
            let main = directory.join("main.iprdb");
            let sidecar = directory.join("main.iprdb.readers");

            let mut meta = empty_direct_meta(1);
            meta.page_count = 3;
            meta.range_root = 2;
            meta.range_record_count = 2;
            let mut bytes = vec![0u8; physical_pages * PAGE_SIZE];
            meta.encode_into((&mut bytes[..PAGE_SIZE]).try_into().unwrap());
            meta.encode_into((&mut bytes[PAGE_SIZE..2 * PAGE_SIZE]).try_into().unwrap());
            put_leaf(
                (&mut bytes[2 * PAGE_SIZE..3 * PAGE_SIZE])
                    .try_into()
                    .unwrap(),
            );
            if corrupt_leaf_crc {
                bytes[2 * PAGE_SIZE + 28] ^= 1;
            }
            std::fs::write(&main, bytes).unwrap();

            let (parent, main_component) = RetainedDirectory::open_parent(&main).unwrap();
            let sidecar_component = parent.sidecar_component(&main_component).unwrap();
            let created = File::create(&sidecar).unwrap();
            created
                .set_len(2 * PAGE_SIZE as u64 + 2 * u64::from(SLOT_SIZE))
                .unwrap();
            drop(created);
            let retained_main = parent.open_regular(&main_component, true).unwrap();
            let retained_sidecar = parent.open_regular(&sidecar_component, true).unwrap();
            let header = SidecarHeader {
                identity_kind: LocalIdentityKind::Posix,
                capacity: 1,
                state: SidecarState::Ready,
                database_id: meta.database_id,
                main_identity: retained_main.identity().encode(),
                sidecar_identity: retained_sidecar.identity().encode(),
                sidecar_id: [7; 16],
                origin: SidecarOrigin::CreateLive,
                attempted_txn_id: meta.txn_id,
                attempted_commit_nonce: meta.commit_nonce,
                attempted_main_bytes: meta.page_count * PAGE_SIZE as u64,
                attempted_main_sha512: [8; 64],
                process_domain_kind: ProcessDomainKind::LinuxPidNamespace,
                process_domain_token: linux_process_domain_token().unwrap(),
                basename_encoding: BasenameEncoding::PosixBytes as u16,
                basename_len: main_component.as_bytes().len() as u32,
                basename_commitment: basename_commitment(
                    BasenameEncoding::PosixBytes,
                    main_component.as_bytes(),
                )
                .unwrap(),
                creation_security_kind: 1,
                creation_security_commitment: [9; 32],
                header_seq: 1,
            };
            let mut block = [0u8; PAGE_SIZE];
            header.encode_into(&mut block);
            retained_sidecar.write_all_at(&block, 0).unwrap();
            retained_sidecar
                .write_all_at(&block, PAGE_SIZE as u64)
                .unwrap();
            drop(retained_sidecar);
            drop(retained_main);
            drop(parent);

            Self {
                directory,
                main,
                sidecar,
                header,
            }
        }

        fn slot(&self, index: u32) -> [u8; SLOT_SIZE as usize] {
            let bytes = std::fs::read(&self.sidecar).unwrap();
            let offset = usize::try_from(sidecar_slot_offset(self.header, index).unwrap()).unwrap();
            bytes[offset..offset + SLOT_SIZE as usize]
                .try_into()
                .unwrap()
        }

        fn put_writer(&self, active: ActiveSlot) {
            let (parent, main_component) = RetainedDirectory::open_parent(&self.main).unwrap();
            let sidecar_component = parent.sidecar_component(&main_component).unwrap();
            let retained = parent.open_regular(&sidecar_component, true).unwrap();
            retained
                .write_all_at(
                    &encode_active_slot(active),
                    sidecar_slot_offset(self.header, 0).unwrap(),
                )
                .unwrap();
        }
    }

    fn put_leaf(page: &mut [u8; PAGE_SIZE]) {
        let records = [(10u32, 20u32, 7u32), (30u32, 40u32, 8u32)];
        PageHeader {
            page_type: PageType::RangeLeaf,
            born_txn: 1,
            item_count: records.len() as u16,
            level: 0,
            lower: (usize::from(PAGE_HEADER_SIZE) + records.len() * 12) as u16,
            upper: PAGE_SIZE as u16,
            aux: 4,
            page_crc32c: 0,
        }
        .encode_into(page);
        for (index, (from, to, value)) in records.into_iter().enumerate() {
            let offset = usize::from(PAGE_HEADER_SIZE) + index * 12;
            page[offset..offset + 4].copy_from_slice(&from.to_le_bytes());
            page[offset + 4..offset + 8].copy_from_slice(&to.to_le_bytes());
            page[offset + 8..offset + 12].copy_from_slice(&value.to_le_bytes());
        }
        write_crc32c(page);
    }

    fn slot_at(path: &Path, header: SidecarHeader, index: u32) -> [u8; SLOT_SIZE as usize] {
        let bytes = std::fs::read(path).unwrap();
        let offset = usize::try_from(sidecar_slot_offset(header, index).unwrap()).unwrap();
        bytes[offset..offset + SLOT_SIZE as usize]
            .try_into()
            .unwrap()
    }

    fn injected_failure() -> LinuxLiveReaderOpenCause {
        LinuxLiveReaderOpenCause::Slot(LinuxReaderSlotError::GenerationChanged)
    }

    #[test]
    fn failures_at_every_post_claim_open_boundary_clear_exact_slot() {
        for failed_stage in [
            OpenStage::ClaimPublished,
            OpenStage::PinPublished,
            OpenStage::BeforeUnlock,
            OpenStage::ViewSetup,
        ] {
            let database = TestDatabase::new(3, false);
            let result = LinuxLiveReader::<Ipv4Key>::open_with_hook(
                &database.main,
                || false,
                |stage, _, _| {
                    if stage == failed_stage {
                        Err(injected_failure())
                    } else {
                        Ok(())
                    }
                },
            );
            assert!(matches!(
                result,
                Err(LinuxLiveReaderOpenError::Failed {
                    cause: LinuxLiveReaderOpenCause::Slot(LinuxReaderSlotError::GenerationChanged),
                    cleanup_outcome: Some(_),
                })
            ));
            assert_eq!(database.slot(1), [0; SLOT_SIZE as usize]);
        }
    }

    #[test]
    fn wrong_key_family_fails_before_claim_publication() {
        let database = TestDatabase::new(3, false);
        let reached_hook = Cell::new(false);
        let result = LinuxLiveReader::<Ipv6Key>::open_with_hook(
            &database.main,
            || false,
            |_, _, _| {
                reached_hook.set(true);
                Ok(())
            },
        );
        assert!(matches!(
            result,
            Err(LinuxLiveReaderOpenError::Failed {
                cause: LinuxLiveReaderOpenCause::View(RangeReadError::WrongKeyFamily),
                cleanup_outcome: None,
            })
        ));
        assert!(!reached_hook.get());
        assert_eq!(database.slot(1), [0; SLOT_SIZE as usize]);
    }

    #[test]
    fn automatic_cleanup_reports_both_replaced_paths() {
        let database = TestDatabase::new(3, false);
        let old_main = database.directory.join("failed-open-main");
        let old_sidecar = database.directory.join("failed-open-sidecar");
        let result = LinuxLiveReader::<Ipv4Key>::open_with_hook(
            &database.main,
            || false,
            |stage, _, _| {
                if stage == OpenStage::ClaimPublished {
                    std::fs::rename(&database.main, &old_main).unwrap();
                    std::fs::rename(&database.sidecar, &old_sidecar).unwrap();
                    std::fs::write(&database.main, b"replacement").unwrap();
                    std::fs::write(&database.sidecar, b"replacement").unwrap();
                    return Err(injected_failure());
                }
                Ok(())
            },
        );
        let outcome = match result.unwrap_err() {
            LinuxLiveReaderOpenError::Failed {
                cause: LinuxLiveReaderOpenCause::Slot(LinuxReaderSlotError::GenerationChanged),
                cleanup_outcome: Some(outcome),
            } => outcome,
            other => panic!("expected cleaned open failure, got {other:?}"),
        };
        assert!(matches!(
            outcome.main_path,
            Err(LinuxOsError::PathIdentityMismatch)
        ));
        assert!(matches!(
            outcome.sidecar_path,
            Err(LinuxOsError::PathIdentityMismatch)
        ));
        let bytes = std::fs::read(old_sidecar).unwrap();
        let offset = usize::try_from(sidecar_slot_offset(database.header, 1).unwrap()).unwrap();
        assert_eq!(
            &bytes[offset..offset + SLOT_SIZE as usize],
            &[0; SLOT_SIZE as usize]
        );
    }

    #[test]
    fn cancellation_is_typed_before_and_after_claim() {
        let database = TestDatabase::new(3, false);
        assert!(matches!(
            LinuxLiveReader::<Ipv4Key>::open_with_cancel(&database.main, || true),
            Err(LinuxLiveReaderOpenError::Failed {
                cause: LinuxLiveReaderOpenCause::Cancelled,
                cleanup_outcome: None,
            })
        ));

        let cancel = Cell::new(false);
        let result = LinuxLiveReader::<Ipv4Key>::open_with_hook(
            &database.main,
            || cancel.get(),
            |stage, _, _| {
                if stage == OpenStage::ClaimPublished {
                    cancel.set(true);
                }
                Ok(())
            },
        );
        assert!(matches!(
            result,
            Err(LinuxLiveReaderOpenError::Failed {
                cause: LinuxLiveReaderOpenCause::Cancelled,
                cleanup_outcome: Some(_),
            })
        ));
        assert_eq!(database.slot(1), [0; SLOT_SIZE as usize]);
    }

    #[test]
    fn cleanup_guard_retains_conflict_and_retries_non_consumingly() {
        let database = TestDatabase::new(3, false);
        let result = LinuxLiveReader::<Ipv4Key>::open_with_hook(
            &database.main,
            || false,
            |stage, files, owned| {
                if stage != OpenStage::ClaimPublished {
                    return Ok(());
                }
                let owned = owned.unwrap();
                let foreign = ActiveSlot {
                    nonce: [0x55; 16],
                    ..owned.active
                };
                files
                    .sidecar
                    .write_all_at(
                        &encode_active_slot(foreign),
                        sidecar_slot_offset(owned.header, owned.index).unwrap(),
                    )
                    .unwrap();
                Err(injected_failure())
            },
        );
        let mut guard = match result.unwrap_err() {
            LinuxLiveReaderOpenError::CleanupRequired { guard, .. } => guard,
            other => panic!("expected cleanup guard, got {other:?}"),
        };
        assert!(matches!(
            guard.retry_cleanup(),
            Err(LinuxLiveCleanupError::Reader(_))
        ));
        let owned = guard.owned_reader().unwrap();
        let files = guard.files().unwrap();
        files
            .sidecar
            .write_all_at(
                &encode_active_slot(owned.active),
                sidecar_slot_offset(owned.header, owned.index).unwrap(),
            )
            .unwrap();
        assert!(guard.close().unwrap().is_some());
        assert!(guard.retry_cleanup().unwrap().is_none());
        assert_eq!(database.slot(1), [0; SLOT_SIZE as usize]);
    }

    #[test]
    fn established_close_is_close_only_retriable_and_idempotent() {
        let database = TestDatabase::new(3, false);
        let mut reader = LinuxLiveReader::<Ipv4Key>::open(&database.main).unwrap();
        let owned = reader.owned.as_ref().unwrap();
        let foreign = ActiveSlot {
            nonce: [0x66; 16],
            ..owned.active
        };
        reader
            .files
            .as_ref()
            .unwrap()
            .sidecar
            .write_all_at(
                &encode_active_slot(foreign),
                sidecar_slot_offset(owned.header, owned.index).unwrap(),
            )
            .unwrap();
        assert!(matches!(
            reader.close(),
            Err(LinuxLiveReaderCloseError::Cleanup(_))
        ));
        assert!(matches!(
            reader.lookup(Ipv4Key(15)),
            Err(LinuxLiveReaderContentError::CloseRequired)
        ));

        let owned = reader.owned.as_ref().unwrap();
        reader
            .files
            .as_ref()
            .unwrap()
            .sidecar
            .write_all_at(
                &encode_active_slot(owned.active),
                sidecar_slot_offset(owned.header, owned.index).unwrap(),
            )
            .unwrap();
        assert!(reader.close().unwrap().is_some());
        assert!(reader.close().unwrap().is_none());
        assert_eq!(database.slot(1), [0; SLOT_SIZE as usize]);
    }

    #[test]
    fn first_close_accepts_exact_already_zero_slot() {
        let database = TestDatabase::new(3, false);
        let mut reader = LinuxLiveReader::<Ipv4Key>::open(&database.main).unwrap();
        let owned = reader.owned.as_ref().unwrap();
        reader
            .files
            .as_ref()
            .unwrap()
            .sidecar
            .write_all_at(
                &[0; SLOT_SIZE as usize],
                sidecar_slot_offset(owned.header, owned.index).unwrap(),
            )
            .unwrap();
        assert!(reader.close().unwrap().is_some());
        assert!(reader.close().unwrap().is_none());
    }

    #[test]
    fn close_reports_each_replaced_path_after_exact_zero() {
        let database = TestDatabase::new(3, false);
        let mut reader = LinuxLiveReader::<Ipv4Key>::open(&database.main).unwrap();
        let old_main = database.directory.join("old-main");
        let old_sidecar = database.directory.join("old-sidecar");
        std::fs::rename(&database.main, old_main).unwrap();
        std::fs::rename(&database.sidecar, old_sidecar).unwrap();
        std::fs::write(&database.main, b"replacement").unwrap();
        std::fs::write(&database.sidecar, b"replacement").unwrap();

        let outcome = reader.close().unwrap().unwrap();
        assert!(matches!(
            outcome.main_path,
            Err(LinuxOsError::PathIdentityMismatch)
        ));
        assert!(matches!(
            outcome.sidecar_path,
            Err(LinuxOsError::PathIdentityMismatch)
        ));
        assert!(reader.close().unwrap().is_none());
    }

    #[test]
    fn open_reaps_dead_writer_tail_then_registers_reader() {
        let database = TestDatabase::new(4, false);
        database.put_writer(ActiveSlot {
            txn_id: 1,
            process_id: i32::MAX as u64,
            process_start: 1,
            task_id: 0,
            nonce: [0x77; 16],
        });
        let mut reader = LinuxLiveReader::<Ipv4Key>::open(&database.main).unwrap();
        assert_eq!(
            std::fs::metadata(&database.main).unwrap().len(),
            3 * PAGE_SIZE as u64
        );
        assert_eq!(database.slot(0), [0; SLOT_SIZE as usize]);
        assert_eq!(reader.lookup(Ipv4Key(35)).unwrap().unwrap().value, 8);
        reader.close().unwrap();
    }

    #[test]
    fn dead_writer_clear_interruptions_retry_through_reader_open_cleanup() {
        for completed_writes in 0..=3 {
            let database = TestDatabase::new(4, false);
            database.put_writer(ActiveSlot {
                txn_id: 1,
                process_id: i32::MAX as u64,
                process_start: 1,
                task_id: 0,
                nonce: [0x7c; 16],
            });
            let result = LinuxLiveReader::<Ipv4Key>::open_with_hook(
                &database.main,
                || false,
                |stage, files, _| {
                    if stage == OpenStage::DeadWriterFound {
                        files.interrupt_dead_writer_clear_for_test(completed_writes);
                        return Err(injected_failure());
                    }
                    Ok(())
                },
            );
            assert!(matches!(
                result,
                Err(LinuxLiveReaderOpenError::Failed {
                    cause: LinuxLiveReaderOpenCause::Slot(LinuxReaderSlotError::GenerationChanged),
                    cleanup_outcome: Some(_),
                })
            ));
            assert_eq!(database.slot(0), [0; SLOT_SIZE as usize]);
            assert_eq!(
                std::fs::metadata(&database.main).unwrap().len(),
                3 * PAGE_SIZE as u64
            );
        }
    }

    #[test]
    fn cancellation_after_dead_writer_discovery_routes_through_cleanup() {
        let database = TestDatabase::new(4, false);
        database.put_writer(ActiveSlot {
            txn_id: 1,
            process_id: i32::MAX as u64,
            process_start: 1,
            task_id: 0,
            nonce: [0x79; 16],
        });
        let cancelled = Cell::new(false);
        let result = LinuxLiveReader::<Ipv4Key>::open_with_hook(
            &database.main,
            || cancelled.get(),
            |stage, _, _| {
                if stage == OpenStage::DeadWriterFound {
                    cancelled.set(true);
                }
                Ok(())
            },
        );
        assert!(matches!(
            result,
            Err(LinuxLiveReaderOpenError::Failed {
                cause: LinuxLiveReaderOpenCause::Cancelled,
                cleanup_outcome: Some(_),
            })
        ));
        assert_eq!(database.slot(0), [0; SLOT_SIZE as usize]);
        assert_eq!(
            std::fs::metadata(&database.main).unwrap().len(),
            3 * PAGE_SIZE as u64
        );
    }

    #[test]
    fn cancelled_dead_writer_cleanup_failure_retains_retryable_guard() {
        let database = TestDatabase::new(4, false);
        database.put_writer(ActiveSlot {
            txn_id: 1,
            process_id: i32::MAX as u64,
            process_start: 1,
            task_id: 0,
            nonce: [0x7a; 16],
        });
        let cancelled = Cell::new(false);
        let result = LinuxLiveReader::<Ipv4Key>::open_with_hook(
            &database.main,
            || cancelled.get(),
            |stage, files, _| {
                if stage == OpenStage::DeadWriterFound {
                    files.main.file.set_len(2 * PAGE_SIZE as u64).unwrap();
                    cancelled.set(true);
                }
                Ok(())
            },
        );
        let mut guard = match result.unwrap_err() {
            LinuxLiveReaderOpenError::CleanupRequired {
                cause: LinuxLiveReaderOpenCause::Cancelled,
                guard,
                ..
            } => guard,
            other => panic!("expected cancelled cleanup guard, got {other:?}"),
        };
        assert_ne!(database.slot(0), [0; SLOT_SIZE as usize]);
        guard
            .files()
            .unwrap()
            .main
            .file
            .set_len(4 * PAGE_SIZE as u64)
            .unwrap();
        assert!(guard.close().unwrap().is_some());
        assert_eq!(database.slot(0), [0; SLOT_SIZE as usize]);
    }

    #[test]
    fn cancelled_dead_writer_path_replacement_retains_old_pair_guard() {
        let database = TestDatabase::new(3, false);
        database.put_writer(ActiveSlot {
            txn_id: 1,
            process_id: i32::MAX as u64,
            process_start: 1,
            task_id: 0,
            nonce: [0x7b; 16],
        });
        let old_main = database.directory.join("cancelled-old-main");
        let old_sidecar = database.directory.join("cancelled-old-sidecar");
        let cancelled = Cell::new(false);
        let result = LinuxLiveReader::<Ipv4Key>::open_with_hook(
            &database.main,
            || cancelled.get(),
            |stage, _, _| {
                if stage == OpenStage::DeadWriterFound {
                    std::fs::rename(&database.main, &old_main).unwrap();
                    std::fs::rename(&database.sidecar, &old_sidecar).unwrap();
                    std::fs::write(&database.main, b"replacement").unwrap();
                    std::fs::write(&database.sidecar, b"replacement").unwrap();
                    cancelled.set(true);
                }
                Ok(())
            },
        );
        match result.unwrap_err() {
            LinuxLiveReaderOpenError::CleanupRequired {
                cause: LinuxLiveReaderOpenCause::Cancelled,
                cleanup:
                    LinuxLiveCleanupError::Pair(LinuxLivePairError::Os(
                        LinuxOsError::PathIdentityMismatch,
                    )),
                ..
            } => {}
            other => panic!("expected cancelled path-replacement guard, got {other:?}"),
        }
        assert_ne!(
            slot_at(&old_sidecar, database.header, 0),
            [0; SLOT_SIZE as usize]
        );
    }

    #[test]
    fn dead_writer_tail_failure_returns_guard_that_retries_exact_obligation() {
        let database = TestDatabase::new(4, false);
        database.put_writer(ActiveSlot {
            txn_id: 1,
            process_id: i32::MAX as u64,
            process_start: 1,
            task_id: 0,
            nonce: [0x78; 16],
        });
        let result = LinuxLiveReader::<Ipv4Key>::open_with_hook(
            &database.main,
            || false,
            |stage, files, _| {
                if stage == OpenStage::DeadWriterFound {
                    files.main.file.set_len(2 * PAGE_SIZE as u64).unwrap();
                }
                Ok(())
            },
        );
        let mut guard = match result.unwrap_err() {
            LinuxLiveReaderOpenError::CleanupRequired { guard, .. } => guard,
            other => panic!("expected dead-writer cleanup guard, got {other:?}"),
        };
        assert!(matches!(
            guard.retry_cleanup(),
            Err(LinuxLiveCleanupError::Pair(
                LinuxLivePairError::TailLengthConflict {
                    target,
                    observed_end,
                    actual,
                }
            )) if target == 3 * PAGE_SIZE as u64
                && observed_end == 4 * PAGE_SIZE as u64
                && actual == 2 * PAGE_SIZE as u64
        ));
        guard
            .files()
            .unwrap()
            .main
            .file
            .set_len(4 * PAGE_SIZE as u64)
            .unwrap();
        assert!(guard.close().unwrap().is_some());
        assert!(guard.close().unwrap().is_none());
        assert_eq!(
            std::fs::metadata(&database.main).unwrap().len(),
            3 * PAGE_SIZE as u64
        );
        assert_eq!(database.slot(0), [0; SLOT_SIZE as usize]);
    }

    #[test]
    fn retained_positional_content_survives_path_replacement_without_validation() {
        let database = TestDatabase::new(3, true);
        let mut reader = LinuxLiveReader::<Ipv4Key>::open(&database.main).unwrap();
        let old_main = database.directory.join("selected-main");
        std::fs::rename(&database.main, old_main).unwrap();
        let mut replacement = File::create(&database.main).unwrap();
        replacement.write_all(b"not the selected database").unwrap();
        drop(replacement);

        assert_eq!(reader.lookup(Ipv4Key(15)).unwrap().unwrap().value, 7);
        assert_eq!(
            reader.count_addresses().unwrap(),
            Cardinality129::from_u64(22)
        );
        let outcome = reader.close().unwrap().unwrap();
        assert!(outcome.main_path.is_err());
    }

    #[test]
    fn handle_and_guard_methods_check_creator_before_content_or_cleanup() {
        let database = TestDatabase::new(3, false);
        let mut reader = LinuxLiveReader::<Ipv4Key>::open(&database.main).unwrap();
        reader.creator_pid = reader.creator_pid.wrapping_add(1);
        assert!(matches!(
            reader.lookup(Ipv4Key(15)),
            Err(LinuxLiveReaderContentError::ForkedHandle)
        ));
        assert!(matches!(
            reader.close(),
            Err(LinuxLiveReaderCloseError::ForkedHandle)
        ));
        reader.creator_pid = std::process::id();
        reader.close().unwrap();

        let result = LinuxLiveReader::<Ipv4Key>::open_with_hook(
            &database.main,
            || false,
            |stage, files, owned| {
                if stage == OpenStage::ClaimPublished {
                    let owned = owned.unwrap();
                    let foreign = ActiveSlot {
                        nonce: [0x88; 16],
                        ..owned.active
                    };
                    files
                        .sidecar
                        .write_all_at(
                            &encode_active_slot(foreign),
                            sidecar_slot_offset(owned.header, owned.index).unwrap(),
                        )
                        .unwrap();
                    return Err(injected_failure());
                }
                Ok(())
            },
        );
        let mut guard = match result.unwrap_err() {
            LinuxLiveReaderOpenError::CleanupRequired { guard, .. } => guard,
            other => panic!("expected cleanup guard, got {other:?}"),
        };
        guard.make_forked_for_test();
        assert!(matches!(
            guard.retry_cleanup(),
            Err(LinuxLiveCleanupError::ForkedHandle)
        ));
    }

    #[test]
    fn dropping_established_reader_does_not_clear_its_slot() {
        let database = TestDatabase::new(3, false);
        let reader = LinuxLiveReader::<Ipv4Key>::open(&database.main).unwrap();
        let active = database.slot(1);
        assert_ne!(active, [0; SLOT_SIZE as usize]);
        drop(reader);
        assert_eq!(database.slot(1), active);
    }
}
