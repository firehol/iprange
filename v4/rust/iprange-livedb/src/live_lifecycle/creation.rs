//! Canonical empty live-pair creation and failure cleanup.

use std::path::Path;

use crate::cancellation::CancellationToken;
use crate::contract::{AddressFamily, StructureKind, ValueKind, ValueTag};
use crate::database_file::EmptySpec;
use crate::error::{Error, Result};
use crate::live_cleanup::{self, Authority as CleanupAuthority};
use crate::live_namespace::Identity;
use crate::live_sidecar::Sidecar;
use crate::publication::{ArtifactKind, DirectoryRole, Housekeeping, HousekeepingArtifact};
use crate::random;
use crate::validation::LocalFileIdentity;

use crate::live_writer::LocalBasename;

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
    pub structure_kind: StructureKind,
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
    pub housekeeping: Housekeeping,
    pub visible_housekeeping: Box<[HousekeepingArtifact]>,
    pub cause: Option<Error>,
}

#[derive(Clone, Copy)]
struct Attempt {
    address_family: AddressFamily,
    value_kind: ValueKind,
    structure_kind: StructureKind,
    value_tag: ValueTag,
    database_id: [u8; 16],
    commit_nonce: [u8; 16],
    sidecar_id: [u8; 16],
    directory_identity: Option<LocalFileIdentity>,
    main_basename: LocalBasename,
    reader_capacity: u32,
}

/// Create an empty transaction-1 live database and reader table.
pub(crate) fn create_live(
    path: impl AsRef<Path>,
    address_family: AddressFamily,
    value_kind: ValueKind,
    structure_kind: StructureKind,
    value_tag: ValueTag,
    reader_capacity: u32,
    cancellation: &CancellationToken,
) -> Result<CreateResult> {
    crate::live_lock::require_live_supported()?;
    let path = path.as_ref();
    cancellation.check()?;
    validate_destination(path, reader_capacity)?;
    validate_kinds(value_kind, structure_kind)?;
    let mut attempt = Attempt::new(
        path,
        address_family,
        value_kind,
        structure_kind,
        value_tag,
        reader_capacity,
    )?;
    attempt.directory_identity = match crate::live_namespace::parent_identity(path) {
        Ok(identity) => Some(identity),
        Err(cause) => return Ok(attempt.not_created(cause)),
    };
    if let Err(cause) = attempt.bind_cleanup_ids(path) {
        return Ok(attempt.not_created(cause));
    }
    if let Err(cause) = cancellation.check() {
        return Ok(attempt.not_created(cause));
    }
    let sidecar = match reserve_sidecar(path, attempt) {
        Ok(sidecar) => sidecar,
        Err(failure) => return Ok(attempt.reservation_failure(failure)),
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

    let created_main = match crate::live_namespace::create_private(
        path,
        CleanupAuthority {
            attempt_id: attempt.database_id,
            ordinal: 0,
            kind: ArtifactKind::OwnedMain,
            directory_role: DirectoryRole::MainFile,
        },
    ) {
        Ok(main) => main,
        Err(failure) => return Ok(attempt.private_failure(&sidecar, failure)),
    };
    let main = created_main.file;
    let main_identity = created_main.identity;
    let spec = EmptySpec::live(
        address_family,
        value_kind,
        structure_kind,
        value_tag,
        attempt.database_id,
        attempt.commit_nonce,
    );
    if let Err(cause) = initialize_pair(path, &main, &sidecar, spec, cancellation) {
        return Ok(attempt.failed(path, &sidecar, Some((&main, main_identity)), cause));
    }
    Ok(attempt.created(main_identity, sidecar.local_identity()))
}

fn reserve_sidecar(
    path: &Path,
    attempt: Attempt,
) -> core::result::Result<Sidecar, crate::live_namespace::PrivateCreationFailure> {
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
        structure_kind: StructureKind,
        value_tag: ValueTag,
        reader_capacity: u32,
    ) -> Result<Self> {
        Ok(Self {
            address_family,
            value_kind,
            structure_kind,
            value_tag,
            database_id: random::nonzero_128()?,
            commit_nonce: random::nonzero_128()?,
            sidecar_id: random::nonzero_128()?,
            directory_identity: None,
            main_basename: LocalBasename::from_path(path)?,
            reader_capacity,
        })
    }

    fn bind_cleanup_ids(&mut self, path: &Path) -> Result<()> {
        let database_id = live_cleanup::unique_attempt_id(path, 0)?;
        let sidecar_id =
            live_cleanup::unique_attempt_id(&crate::path::canonical_sidecar(path)?, 1)?;
        self.database_id = database_id;
        self.sidecar_id = sidecar_id;
        Ok(())
    }

    fn created(self, main: Identity, sidecar: Identity) -> CreateResult {
        self.result(
            CreationState::Created,
            Some(crate::live_namespace::public_identity(main)),
            Some(crate::live_namespace::public_identity(sidecar)),
            live_cleanup::TerminalFacts::clean(),
        )
    }

    fn not_created(self, cause: Error) -> CreateResult {
        self.result(
            CreationState::NotCreated,
            None,
            None,
            live_cleanup::TerminalFacts::cause(cause),
        )
    }

    fn reservation_failure(
        self,
        failure: crate::live_namespace::PrivateCreationFailure,
    ) -> CreateResult {
        let sidecar_identity = failure.identity.map(crate::live_namespace::public_identity);
        self.failure_result(None, sidecar_identity, failure.cause, failure.cleanup)
    }

    fn private_failure(
        self,
        sidecar: &Sidecar,
        failure: crate::live_namespace::PrivateCreationFailure,
    ) -> CreateResult {
        let main_identity = failure.identity.map(crate::live_namespace::public_identity);
        let sidecar_identity = Some(crate::live_namespace::public_identity(
            sidecar.local_identity(),
        ));
        let mut cleanup = failure.cleanup;
        if cleanup.is_clean() {
            cleanup.absorb(live_cleanup::remove(
                &sidecar.path,
                &sidecar.file,
                sidecar.local_identity(),
                CleanupAuthority {
                    attempt_id: self.sidecar_id,
                    ordinal: 1,
                    kind: ArtifactKind::OwnedCoordination,
                    directory_role: DirectoryRole::MainFile,
                },
            ));
        }
        self.failure_result(main_identity, sidecar_identity, failure.cause, cleanup)
    }

    fn failed(
        self,
        path: &Path,
        sidecar: &Sidecar,
        main: Option<(&std::fs::File, Identity)>,
        cause: Error,
    ) -> CreateResult {
        let public_main =
            main.map(|(_, identity)| crate::live_namespace::public_identity(identity));
        let public_sidecar = Some(crate::live_namespace::public_identity(
            sidecar.local_identity(),
        ));
        let cleanup = cleanup(path, sidecar, main, self.database_id, self.sidecar_id);
        self.failure_result(public_main, public_sidecar, cause, cleanup)
    }

    fn failure_result(
        self,
        main_identity: Option<LocalFileIdentity>,
        sidecar_identity: Option<LocalFileIdentity>,
        cause: Error,
        cleanup: live_cleanup::Outcome,
    ) -> CreateResult {
        let facts = live_cleanup::TerminalFacts::failed(cause, cleanup);
        if facts.residue_possible {
            self.result(
                CreationState::OutcomeUnknown,
                main_identity,
                sidecar_identity,
                facts,
            )
        } else {
            self.result(
                CreationState::NotCreated,
                main_identity,
                sidecar_identity,
                facts,
            )
        }
    }

    fn result(
        self,
        state: CreationState,
        main_identity: Option<LocalFileIdentity>,
        sidecar_identity: Option<LocalFileIdentity>,
        facts: live_cleanup::TerminalFacts,
    ) -> CreateResult {
        CreateResult {
            address_family: self.address_family,
            value_kind: self.value_kind,
            structure_kind: self.structure_kind,
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
            residue_possible: facts.residue_possible,
            housekeeping: facts.housekeeping,
            visible_housekeeping: facts.visible_housekeeping,
            cause: facts.cause,
        }
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

fn validate_kinds(value_kind: ValueKind, structure_kind: StructureKind) -> Result<()> {
    let valid = match value_kind {
        ValueKind::Direct | ValueKind::Membership => structure_kind == StructureKind::None,
        ValueKind::Structured => structure_kind != StructureKind::None,
    };
    if !valid {
        return Err(Error::WrongStructureKind(
            "value kind and structure kind do not form a valid database",
        ));
    }
    Ok(())
}

fn prepare_sidecar(path: &Path, sidecar: &Sidecar, cancellation: &CancellationToken) -> Result<()> {
    cancellation.check()?;
    crate::live_namespace::sync_parent(&sidecar.path)?;
    crate::fault::crash("create.after_sidecar_parent_sync");
    cancellation.check()?;
    require_absent(path)
}

fn initialize_pair(
    path: &Path,
    main: &std::fs::File,
    sidecar: &Sidecar,
    spec: EmptySpec,
    cancellation: &CancellationToken,
) -> Result<()> {
    cancellation.check()?;
    crate::database_file::write_empty(main, spec)?;
    crate::fault::crash("create.after_main_sync");
    cancellation.check()?;
    crate::live_namespace::sync_parent(path)?;
    crate::fault::crash("create.after_main_parent_sync");
    cancellation.check()?;
    sidecar.publish_ready()?;
    crate::fault::crash("create.after_ready_sync");
    crate::live_namespace::sync_parent(&sidecar.path)?;
    crate::fault::crash("create.after_ready_parent_sync");
    Ok(())
}

fn cleanup(
    path: &Path,
    sidecar: &Sidecar,
    main: Option<(&std::fs::File, Identity)>,
    database_id: [u8; 16],
    sidecar_id: [u8; 16],
) -> live_cleanup::Outcome {
    let mut outcome = live_cleanup::Outcome::clean();
    if let Some((file, identity)) = main {
        outcome.absorb(live_cleanup::remove(
            path,
            file,
            identity,
            CleanupAuthority {
                attempt_id: database_id,
                ordinal: 0,
                kind: ArtifactKind::OwnedMain,
                directory_role: DirectoryRole::MainFile,
            },
        ));
        if !outcome.is_clean() {
            return outcome;
        }
    }
    outcome.absorb(live_cleanup::remove(
        &sidecar.path,
        &sidecar.file,
        sidecar.local_identity(),
        CleanupAuthority {
            attempt_id: sidecar_id,
            ordinal: 1,
            kind: ArtifactKind::OwnedCoordination,
            directory_role: DirectoryRole::MainFile,
        },
    ));
    outcome
}

fn require_absent(path: &Path) -> Result<()> {
    match crate::live_namespace::path_identity(path)? {
        None => Ok(()),
        Some(_) => Err(Error::InvalidArgument("destination already exists")),
    }
}
