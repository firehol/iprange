//! Canonical empty live-pair creation.

use std::path::Path;

use crate::cancellation::CancellationToken;
use crate::contract::{AddressFamily, MetaV4, ValueKind, ValueTag, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::file_io;
use crate::live_sidecar::{self, Identity, Sidecar};
use crate::random;
use crate::validation::LocalFileIdentity;

use super::LocalBasename;

/// Factual terminal state of one creation attempt.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum CreationState {
    NotCreated,
    Created,
    OutcomeUnknown,
}

/// Identity and terminal state of one creation attempt.
#[derive(Debug)]
pub struct CreateResult {
    pub address_family: AddressFamily,
    pub value_kind: ValueKind,
    pub value_tag: ValueTag,
    pub database_id: [u8; 16],
    pub commit_nonce: [u8; 16],
    pub sidecar_id: [u8; 16],
    pub directory_identity: Option<LocalFileIdentity>,
    pub main_basename: LocalBasename,
    pub main_identity: Option<LocalFileIdentity>,
    pub sidecar_identity: Option<LocalFileIdentity>,
    pub reader_capacity: u32,
    pub state: CreationState,
    pub residue_possible: bool,
    pub cause: Option<Error>,
}

#[derive(Clone, Copy)]
struct Attempt {
    address_family: AddressFamily,
    value_kind: ValueKind,
    value_tag: ValueTag,
    database_id: [u8; 16],
    commit_nonce: [u8; 16],
    sidecar_id: [u8; 16],
    directory_identity: Option<LocalFileIdentity>,
    main_basename: LocalBasename,
    reader_capacity: u32,
}

/// Create an empty transaction-1 live database and reader table.
pub fn create_live(
    path: impl AsRef<Path>,
    address_family: AddressFamily,
    value_kind: ValueKind,
    value_tag: ValueTag,
    reader_capacity: u32,
    cancellation: &CancellationToken,
) -> Result<CreateResult> {
    let path = path.as_ref();
    cancellation.check()?;
    validate_destination(path, reader_capacity)?;
    let mut attempt = Attempt::new(path, address_family, value_kind, value_tag, reader_capacity)?;
    attempt.directory_identity = match live_sidecar::parent_identity(path) {
        Ok(identity) => Some(identity),
        Err(cause) => return Ok(attempt.not_created(cause)),
    };
    if let Err(cause) = cancellation.check() {
        return Ok(attempt.not_created(cause));
    }
    let sidecar = match reserve_sidecar(path, attempt) {
        Ok(sidecar) => sidecar,
        Err(cause) => {
            return Ok(if cause.residue_possible() {
                attempt.unknown(cause)
            } else {
                attempt.not_created(cause)
            })
        }
    };
    if let Err(cause) = cancellation.check() {
        return Ok(attempt.failed(path, &sidecar, None, cause));
    }
    if let Err(cause) = sidecar.initialize_creating() {
        return Ok(attempt.failed(path, &sidecar, None, cause));
    }
    if let Err(cause) = prepare_sidecar(path, &sidecar, cancellation) {
        return Ok(attempt.failed(path, &sidecar, None, cause));
    }

    let main = match live_sidecar::create_private(path) {
        Ok(main) => main,
        Err(cause) => {
            return Ok(if cause.residue_possible() {
                attempt.unknown(cause)
            } else {
                attempt.failed(path, &sidecar, None, cause)
            })
        }
    };
    let main_identity = match live_sidecar::identity(&main) {
        Ok(identity) => identity,
        Err(cause) => {
            return Ok(attempt.result(
                CreationState::OutcomeUnknown,
                None,
                Some(live_sidecar::public_identity(sidecar.local_identity())),
                true,
                Some(cause),
            ))
        }
    };
    let meta = empty_meta(
        address_family,
        value_kind,
        value_tag,
        attempt.database_id,
        attempt.commit_nonce,
    );
    if let Err(cause) = initialize_pair(path, &main, &sidecar, meta, cancellation) {
        return Ok(attempt.failed(path, &sidecar, Some(main_identity), cause));
    }
    Ok(attempt.created(main_identity, sidecar.local_identity()))
}

fn reserve_sidecar(path: &Path, attempt: Attempt) -> Result<Sidecar> {
    Sidecar::reserve(
        path,
        attempt.database_id,
        attempt.sidecar_id,
        attempt.reader_capacity,
    )
}

impl Attempt {
    fn new(
        path: &Path,
        address_family: AddressFamily,
        value_kind: ValueKind,
        value_tag: ValueTag,
        reader_capacity: u32,
    ) -> Result<Self> {
        Ok(Self {
            address_family,
            value_kind,
            value_tag,
            database_id: random::nonzero_128()?,
            commit_nonce: random::nonzero_128()?,
            sidecar_id: random::nonzero_128()?,
            directory_identity: None,
            main_basename: LocalBasename::from_path(path)?,
            reader_capacity,
        })
    }

    fn created(self, main: Identity, sidecar: Identity) -> CreateResult {
        self.result(
            CreationState::Created,
            Some(live_sidecar::public_identity(main)),
            Some(live_sidecar::public_identity(sidecar)),
            false,
            None,
        )
    }

    fn not_created(self, cause: Error) -> CreateResult {
        self.result(CreationState::NotCreated, None, None, false, Some(cause))
    }

    fn unknown(self, cause: Error) -> CreateResult {
        self.result(CreationState::OutcomeUnknown, None, None, true, Some(cause))
    }

    fn failed(
        self,
        path: &Path,
        sidecar: &Sidecar,
        main_identity: Option<Identity>,
        cause: Error,
    ) -> CreateResult {
        let public_main = main_identity.map(live_sidecar::public_identity);
        let public_sidecar = Some(live_sidecar::public_identity(sidecar.local_identity()));
        match cleanup(path, sidecar, main_identity) {
            Ok(()) => self.result(
                CreationState::NotCreated,
                public_main,
                public_sidecar,
                false,
                Some(cause),
            ),
            Err(cleanup) => self.result(
                CreationState::OutcomeUnknown,
                public_main,
                public_sidecar,
                true,
                Some(Error::CleanupIncomplete {
                    cause: Box::new(cause),
                    cleanup: Box::new(cleanup),
                }),
            ),
        }
    }

    fn result(
        self,
        state: CreationState,
        main_identity: Option<LocalFileIdentity>,
        sidecar_identity: Option<LocalFileIdentity>,
        residue_possible: bool,
        cause: Option<Error>,
    ) -> CreateResult {
        CreateResult {
            address_family: self.address_family,
            value_kind: self.value_kind,
            value_tag: self.value_tag,
            database_id: self.database_id,
            commit_nonce: self.commit_nonce,
            sidecar_id: self.sidecar_id,
            directory_identity: self.directory_identity,
            main_basename: self.main_basename,
            main_identity,
            sidecar_identity,
            reader_capacity: self.reader_capacity,
            state,
            residue_possible,
            cause,
        }
    }
}

pub(crate) fn empty_meta(
    address_family: AddressFamily,
    value_kind: ValueKind,
    value_tag: ValueTag,
    database_id: [u8; 16],
    commit_nonce: [u8; 16],
) -> MetaV4 {
    MetaV4 {
        address_family,
        value_kind,
        value_tag,
        database_id,
        txn_id: 1,
        commit_nonce,
        page_count: 2,
        range_record_count: 0,
        active_feed_count: 0,
        feed_index_limit: 0,
        membership_entry_count: 0,
        membership_id_limit: u64::from(value_kind == ValueKind::Membership),
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
    }
}

fn validate_destination(path: &Path, reader_capacity: u32) -> Result<()> {
    if reader_capacity == 0 {
        return Err(Error::InvalidArgument(
            "reader capacity must be greater than zero",
        ));
    }
    require_absent(path)?;
    require_absent(&crate::path::canonical_sidecar(path)?)
}

fn prepare_sidecar(path: &Path, sidecar: &Sidecar, cancellation: &CancellationToken) -> Result<()> {
    cancellation.check()?;
    live_sidecar::sync_parent(&sidecar.path)?;
    crate::fault::crash("create.after_sidecar_parent_sync");
    cancellation.check()?;
    require_absent(path)
}

fn initialize_pair(
    path: &Path,
    main: &std::fs::File,
    sidecar: &Sidecar,
    meta: MetaV4,
    cancellation: &CancellationToken,
) -> Result<()> {
    cancellation.check()?;
    write_empty_main(main, meta)?;
    crate::fault::crash("create.after_main_sync");
    cancellation.check()?;
    live_sidecar::sync_parent(path)?;
    crate::fault::crash("create.after_main_parent_sync");
    cancellation.check()?;
    sidecar.publish_ready()?;
    crate::fault::crash("create.after_ready_sync");
    live_sidecar::sync_parent(&sidecar.path)?;
    crate::fault::crash("create.after_ready_parent_sync");
    Ok(())
}

pub(crate) fn write_empty_main(main: &std::fs::File, meta: MetaV4) -> Result<()> {
    main.set_len((2 * PAGE_SIZE) as u64)?;
    let mut page = [0; PAGE_SIZE];
    meta.encode_into(&mut page);
    file_io::write_exact_at(main, &page, 0)?;
    file_io::write_exact_at(main, &page, PAGE_SIZE as u64)?;
    main.sync_all()?;
    Ok(())
}

fn cleanup(path: &Path, sidecar: &Sidecar, main_identity: Option<Identity>) -> Result<()> {
    if let Some(identity) = main_identity {
        live_sidecar::remove_exact(path, identity)?;
    }
    live_sidecar::remove_exact(&sidecar.path, sidecar.local_identity())
}

fn require_absent(path: &Path) -> Result<()> {
    match live_sidecar::path_identity(path)? {
        None => Ok(()),
        Some(_) => Err(Error::InvalidArgument("destination already exists")),
    }
}
