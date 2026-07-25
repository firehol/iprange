//! Canonical empty live-pair creation.

use std::fs;
use std::path::Path;

use crate::contract::{AddressFamily, MetaV4, ValueKind, ValueTag, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::file_io;
use crate::live_sidecar::{self, Identity, Sidecar};
use crate::random;

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
    pub database_id: [u8; 16],
    pub commit_nonce: [u8; 16],
    pub reader_capacity: u32,
    pub state: CreationState,
    pub residue_possible: bool,
    pub cause: Option<Error>,
}

#[derive(Clone, Copy)]
struct Attempt {
    database_id: [u8; 16],
    commit_nonce: [u8; 16],
    reader_capacity: u32,
}

/// Create an empty transaction-1 live database and reader table.
pub fn create_live(
    path: impl AsRef<Path>,
    address_family: AddressFamily,
    value_kind: ValueKind,
    value_tag: ValueTag,
    reader_capacity: u32,
) -> Result<CreateResult> {
    let path = path.as_ref();
    validate_destination(path, reader_capacity)?;
    let attempt = Attempt::new(reader_capacity)?;
    let sidecar = match reserve_sidecar(path, attempt) {
        Ok(sidecar) => sidecar,
        Err(result) => return Ok(result),
    };
    if let Err(cause) = sidecar.initialize_creating() {
        return Ok(attempt.failed(path, &sidecar, None, cause));
    }
    if let Err(cause) = prepare_sidecar(path, &sidecar) {
        return Ok(attempt.failed(path, &sidecar, None, cause));
    }

    let (main, main_identity) = match create_main(path, &sidecar, attempt) {
        Ok(main) => main,
        Err(result) => return Ok(result),
    };
    let meta = empty_meta(
        address_family,
        value_kind,
        value_tag,
        attempt.database_id,
        attempt.commit_nonce,
    );
    if let Err(cause) = initialize_pair(path, &main, &sidecar, meta) {
        return Ok(attempt.failed(path, &sidecar, Some(main_identity), cause));
    }
    Ok(attempt.created())
}

fn reserve_sidecar(path: &Path, attempt: Attempt) -> std::result::Result<Sidecar, CreateResult> {
    let sidecar_id = random::nonzero_128().map_err(|cause| attempt.not_created(cause))?;
    Sidecar::reserve(
        path,
        attempt.database_id,
        sidecar_id,
        attempt.reader_capacity,
    )
    .map_err(|cause| {
        if cause.residue_possible() {
            attempt.unknown(cause)
        } else {
            attempt.not_created(cause)
        }
    })
}

fn create_main(
    path: &Path,
    sidecar: &Sidecar,
    attempt: Attempt,
) -> std::result::Result<(std::fs::File, Identity), CreateResult> {
    let file = live_sidecar::create_private(path).map_err(|cause| {
        if cause.residue_possible() {
            attempt.unknown(cause)
        } else {
            attempt.failed(path, sidecar, None, cause)
        }
    })?;
    let identity = live_sidecar::identity(&file).map_err(|cause| attempt.unknown(cause))?;
    Ok((file, identity))
}

impl Attempt {
    fn new(reader_capacity: u32) -> Result<Self> {
        Ok(Self {
            database_id: random::nonzero_128()?,
            commit_nonce: random::nonzero_128()?,
            reader_capacity,
        })
    }

    fn created(self) -> CreateResult {
        self.result(CreationState::Created, false, None)
    }

    fn not_created(self, cause: Error) -> CreateResult {
        self.result(CreationState::NotCreated, false, Some(cause))
    }

    fn unknown(self, cause: Error) -> CreateResult {
        self.result(CreationState::OutcomeUnknown, true, Some(cause))
    }

    fn failed(
        self,
        path: &Path,
        sidecar: &Sidecar,
        main_identity: Option<Identity>,
        cause: Error,
    ) -> CreateResult {
        match cleanup(path, sidecar, main_identity) {
            Ok(()) => self.not_created(cause),
            Err(cleanup) => self.unknown(Error::CleanupIncomplete {
                cause: Box::new(cause),
                cleanup: Box::new(cleanup),
            }),
        }
    }

    fn result(
        self,
        state: CreationState,
        residue_possible: bool,
        cause: Option<Error>,
    ) -> CreateResult {
        CreateResult {
            database_id: self.database_id,
            commit_nonce: self.commit_nonce,
            reader_capacity: self.reader_capacity,
            state,
            residue_possible,
            cause,
        }
    }
}

fn empty_meta(
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

fn prepare_sidecar(path: &Path, sidecar: &Sidecar) -> Result<()> {
    live_sidecar::sync_parent(&sidecar.path)?;
    crate::fault::crash("create.after_sidecar_parent_sync");
    require_absent(path)
}

fn initialize_pair(
    path: &Path,
    main: &std::fs::File,
    sidecar: &Sidecar,
    meta: MetaV4,
) -> Result<()> {
    main.set_len((2 * PAGE_SIZE) as u64)?;
    let mut page = [0; PAGE_SIZE];
    meta.encode_into(&mut page);
    file_io::write_exact_at(main, &page, 0)?;
    file_io::write_exact_at(main, &page, PAGE_SIZE as u64)?;
    main.sync_all()?;
    crate::fault::crash("create.after_main_sync");
    live_sidecar::sync_parent(path)?;
    crate::fault::crash("create.after_main_parent_sync");
    sidecar.publish_ready()?;
    crate::fault::crash("create.after_ready_sync");
    live_sidecar::sync_parent(&sidecar.path)?;
    crate::fault::crash("create.after_ready_parent_sync");
    Ok(())
}

fn cleanup(path: &Path, sidecar: &Sidecar, main_identity: Option<Identity>) -> Result<()> {
    if let Some(identity) = main_identity {
        live_sidecar::verify_path(path, identity)?;
        fs::remove_file(path)?;
        live_sidecar::sync_parent(path)?;
    }
    sidecar.verify_path()?;
    fs::remove_file(&sidecar.path)?;
    live_sidecar::sync_parent(&sidecar.path)
}

fn require_absent(path: &Path) -> Result<()> {
    match fs::symlink_metadata(path) {
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(()),
        Err(error) => Err(error.into()),
        Ok(_) => Err(Error::InvalidArgument("destination already exists")),
    }
}
