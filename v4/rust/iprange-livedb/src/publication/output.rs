//! Owned construction and one-pass preparation of an immutable output.

use std::fs::File;
use std::path::Path;

use crate::bootstrap::{self, OpenMode};
use crate::cancellation::CancellationToken;
use crate::contract::MetaV4;
use crate::immutable_output::Finished;
use crate::live_lock::{self, Mode};
use crate::live_sidecar::MAIN_LIFETIME_LOCK;
use crate::mapping::Mapping;
use crate::random;

use super::namespace::{
    local_identity as local, regular_identity, regular_identity_any_link, sync_file, Destination,
    Identity, Name, NamespaceError, BASENAME_ENCODING_KIND, CREATION_SECURITY_KIND,
};
use super::replacement::PreviousMain;
use super::reservation::Policy;
use super::{
    ArtifactKind, CreationSecurity, DirectoryRole, PrivateOutputAttempt, PublicationProblem,
};

#[path = "output_digest.rs"]
mod output_digest;
pub(super) use output_digest::{digest, digest_cancellable};
#[cfg(test)]
#[cfg(all(test, unix))]
use output_digest::{digest_with, DIGEST_BUFFER_SIZE};
#[path = "output_resume.rs"]
mod output_resume;
pub(super) use output_resume::ResumedOutput;

#[derive(Debug)]
pub(crate) enum Error {
    Namespace(NamespaceError),
    Sdk(crate::error::Error),
    Bootstrap,
    Gc(PublicationProblem),
    FinishedMetaChanged,
    FinishedLengthChanged,
}

#[derive(Debug)]
pub(crate) struct Failure<T> {
    pub(crate) owner: T,
    pub(crate) cause: Error,
}

#[derive(Debug)]
pub(crate) struct CreatedOutput {
    destination: Destination,
    attempt_id: [u8; 16],
    name: Name,
    file: File,
}

impl CreatedOutput {
    pub(crate) fn create(path: &Path) -> Result<Self, Error> {
        Self::create_with(path, false)
    }

    pub(crate) fn create_absent(path: &Path) -> Result<Self, Error> {
        Self::create_with(path, true)
    }

    fn create_with(path: &Path, require_absent: bool) -> Result<Self, Error> {
        let destination = Destination::bind(path).map_err(Error::Namespace)?;
        if require_absent {
            destination
                .require_fail_if_exists_available()
                .map_err(Error::Namespace)?;
        }
        let (attempt_id, name, file) = create_attempt(&destination)?;
        Ok(Self {
            destination,
            attempt_id,
            name,
            file,
        })
    }

    pub(crate) fn facts(&self) -> PrivateOutputAttempt {
        let identity =
            regular_identity_any_link(&self.file, self.destination.directory().identity())
                .ok()
                .map(local);
        facts(&self.destination, self.attempt_id, &self.name, identity)
    }

    pub(crate) fn file(&self) -> &File {
        &self.file
    }

    pub(crate) fn destination(&self) -> &Destination {
        &self.destination
    }

    pub(crate) fn name(&self) -> &Name {
        &self.name
    }

    #[cfg(windows)]
    pub(crate) fn attempt_id(&self) -> [u8; 16] {
        self.attempt_id
    }

    // The fixed-size owner is returned inline to preserve zero-allocation cleanup authority.
    #[allow(clippy::result_large_err)]
    pub(crate) fn secure(self) -> Result<SecuredOutput, Failure<Self>> {
        match secure_created(&self) {
            Ok(identity) => {
                let Self {
                    destination,
                    attempt_id,
                    name,
                    file,
                } = self;
                Ok(SecuredOutput {
                    attempt: OutputAttempt {
                        destination,
                        attempt_id,
                        name,
                        identity,
                    },
                    file,
                })
            }
            Err(cause) => Err(Failure { owner: self, cause }),
        }
    }
}

#[cfg(not(windows))]
fn create_attempt(destination: &Destination) -> Result<([u8; 16], Name, File), Error> {
    let attempt_id = random::nonzero_128().map_err(Error::Sdk)?;
    let name = destination
        .output_name(attempt_id)
        .map_err(Error::Namespace)?;
    let file = destination.create(&name).map_err(Error::Namespace)?;
    Ok((attempt_id, name, file))
}

#[cfg(windows)]
fn create_attempt(destination: &Destination) -> Result<([u8; 16], Name, File), Error> {
    loop {
        let attempt_id = random::nonzero_128().map_err(Error::Sdk)?;
        let name = destination
            .output_name(attempt_id)
            .map_err(Error::Namespace)?;
        if attempt_collision(destination, attempt_id, &name)? {
            continue;
        }
        match destination.create(&name) {
            Ok(file) => return Ok((attempt_id, name, file)),
            Err(NamespaceError::Exists) => continue,
            Err(error) => return Err(Error::Namespace(error)),
        }
    }
}

#[cfg(windows)]
fn attempt_collision(
    destination: &Destination,
    attempt_id: [u8; 16],
    output: &Name,
) -> Result<bool, Error> {
    let reservation = destination
        .reservation_name(attempt_id)
        .map_err(Error::Namespace)?;
    let envelope0 = super::gc_name::envelope(attempt_id, 0).map_err(Error::Namespace)?;
    let inert0 = super::gc_name::inert(attempt_id, 0).map_err(Error::Namespace)?;
    let envelope1 = super::gc_name::envelope(attempt_id, 1).map_err(Error::Namespace)?;
    let inert1 = super::gc_name::inert(attempt_id, 1).map_err(Error::Namespace)?;
    for name in [
        output,
        &reservation,
        &envelope0,
        &inert0,
        &envelope1,
        &inert1,
    ] {
        match destination.directory().require_absent(name) {
            Ok(()) => {}
            Err(NamespaceError::Exists) => return Ok(true),
            Err(error) => return Err(Error::Namespace(error)),
        }
    }
    Ok(false)
}

#[derive(Debug)]
pub(crate) struct SecuredOutput {
    attempt: OutputAttempt,
    file: File,
}

impl SecuredOutput {
    pub(crate) fn into_parts(self) -> (OutputAttempt, File) {
        (self.attempt, self.file)
    }
}

#[derive(Debug)]
pub(crate) struct OutputAttempt {
    destination: Destination,
    attempt_id: [u8; 16],
    name: Name,
    identity: Identity,
}

impl OutputAttempt {
    #[allow(clippy::result_large_err)]
    pub(crate) fn prepare_cancellable(
        self,
        finished: Finished,
        cancellation: &CancellationToken,
    ) -> Result<PreparedOutput, Failure<UnpreparedOutput>> {
        let owner = UnpreparedOutput {
            attempt: self,
            finished,
        };
        match prepare_cancellable(&owner, cancellation) {
            Ok((byte_length, sha512)) => {
                let UnpreparedOutput { attempt, finished } = owner;
                let Finished {
                    file,
                    mapping,
                    meta,
                } = finished;
                Ok(PreparedOutput {
                    attempt,
                    file,
                    mapping,
                    meta,
                    byte_length,
                    sha512,
                    policy: Policy::FailIfExists,
                    previous: None,
                })
            }
            Err(cause) => Err(Failure { owner, cause }),
        }
    }

    pub(crate) fn destination(&self) -> &Destination {
        &self.destination
    }

    pub(crate) fn attempt_id(&self) -> [u8; 16] {
        self.attempt_id
    }

    pub(crate) fn name(&self) -> &Name {
        &self.name
    }

    pub(crate) fn identity(&self) -> Identity {
        self.identity
    }

    pub(crate) fn facts(&self) -> PrivateOutputAttempt {
        facts(
            &self.destination,
            self.attempt_id,
            &self.name,
            Some(local(self.identity)),
        )
    }
}

pub(crate) fn resume_secured_output(
    destination_path: &Path,
    facts: &PrivateOutputAttempt,
) -> Result<(OutputAttempt, File), Error> {
    open_secured_output(destination_path, facts)?.ok_or(Error::Namespace(NamespaceError::Missing))
}

pub(crate) fn resume_secured_output_for_cleanup(
    destination_path: &Path,
    facts: &PrivateOutputAttempt,
) -> Result<Option<(OutputAttempt, File)>, Error> {
    let resumed = open_secured_output(destination_path, facts)?;
    if resumed.is_some() {
        return Ok(resumed);
    }

    let (destination, name, _) = bind_secured_output(destination_path, facts)?;
    #[cfg(unix)]
    destination.directory().sync().map_err(Error::Namespace)?;
    destination.directory().verify().map_err(Error::Namespace)?;
    destination
        .directory()
        .require_absent(&name)
        .map_err(Error::Namespace)?;
    Ok(None)
}

fn open_secured_output(
    destination_path: &Path,
    facts: &PrivateOutputAttempt,
) -> Result<Option<(OutputAttempt, File)>, Error> {
    let (destination, name, identity) = bind_secured_output(destination_path, facts)?;
    let Some(regular) = destination
        .directory()
        .open_regular(&name, true)
        .map_err(Error::Namespace)?
    else {
        return Ok(None);
    };
    if regular.identity != identity {
        return Err(Error::Namespace(NamespaceError::IdentityChanged));
    }
    destination
        .directory()
        .verify_name(&name, identity)
        .map_err(Error::Namespace)?;
    destination
        .verify_created(&regular.file)
        .map_err(Error::Namespace)?;
    Ok(Some((
        OutputAttempt {
            destination,
            attempt_id: facts.publication_attempt_id,
            name,
            identity,
        },
        regular.file,
    )))
}

fn bind_secured_output(
    destination_path: &Path,
    facts: &PrivateOutputAttempt,
) -> Result<(Destination, Name, Identity), Error> {
    if facts.basename_encoding != BASENAME_ENCODING_KIND
        || facts.creation_security.kind != CREATION_SECURITY_KIND
    {
        return Err(Error::Sdk(crate::Error::InvalidArgument(
            "worker output facts use an unsupported encoding",
        )));
    }
    let identity = facts
        .identity
        .and_then(|identity| Identity::decode(identity.bytes))
        .ok_or(Error::Sdk(crate::Error::InvalidArgument(
            "worker output identity is invalid",
        )))?;
    let destination = Destination::bind(destination_path).map_err(Error::Namespace)?;
    let name = destination
        .output_name(facts.publication_attempt_id)
        .map_err(Error::Namespace)?;
    if local(destination.directory().identity()) != facts.directory_identity
        || name.bytes() != facts.basename.as_ref()
        || destination.security_commitment() != facts.creation_security.commitment
    {
        return Err(Error::Namespace(NamespaceError::IdentityChanged));
    }
    Ok((destination, name, identity))
}

#[derive(Debug)]
pub(crate) struct UnpreparedOutput {
    pub(crate) attempt: OutputAttempt,
    pub(crate) finished: Finished,
}

#[derive(Debug)]
pub(crate) struct PreparedOutput {
    pub(crate) attempt: OutputAttempt,
    pub(crate) file: File,
    pub(crate) mapping: Mapping,
    pub(crate) meta: MetaV4,
    pub(crate) byte_length: u64,
    pub(crate) sha512: [u8; 64],
    pub(crate) policy: Policy,
    pub(crate) previous: Option<PreviousMain>,
}

impl PreparedOutput {
    pub(crate) fn verify_private(&self) -> Result<(), Error> {
        self.verify(Location::Private)
    }

    pub(crate) fn verify_main(&self) -> Result<(), Error> {
        self.verify(Location::Main)
    }

    pub(crate) fn verify_destination_before_main(&self) -> Result<(), Error> {
        match &self.previous {
            Some(previous) => previous
                .verify_canonical_namespace(self.attempt.destination())
                .map_err(Error::Namespace),
            None => self
                .attempt
                .destination()
                .directory()
                .require_absent(self.attempt.destination().main())
                .map_err(Error::Namespace),
        }
    }

    fn verify(&self, location: Location) -> Result<(), Error> {
        let _probe = crate::worker::enter_output(&self.mapping).map_err(Error::Sdk)?;
        let length = inspect_exact(
            &self.attempt,
            &self.file,
            &self.mapping,
            self.meta,
            location,
        )?;
        if length != self.byte_length {
            return Err(Error::FinishedLengthChanged);
        }
        match (&self.previous, location) {
            (None, Location::Main) => self
                .attempt
                .destination()
                .directory()
                .require_absent(self.attempt.name())?,
            (Some(previous), Location::Main) => previous
                .verify_private_or_retired(self.attempt.destination(), self.attempt.name())?,
            (_, Location::Private) => {}
        }
        Ok(())
    }
}

fn secure_created(created: &CreatedOutput) -> Result<Identity, Error> {
    let directory = created.destination.directory();
    let identity = regular_identity(&created.file, directory.identity())?;
    directory.verify_name(&created.name, identity)?;
    created.destination.secure_created(&created.file)?;
    let secured = regular_identity(&created.file, directory.identity())?;
    if secured != identity {
        return Err(NamespaceError::IdentityChanged.into());
    }
    directory.verify_name(&created.name, identity)?;
    created.destination.verify_created(&created.file)?;
    Ok(identity)
}

fn prepare_cancellable(
    owner: &UnpreparedOutput,
    cancellation: &CancellationToken,
) -> Result<(u64, [u8; 64]), Error> {
    cancellation.check().map_err(Error::Sdk)?;
    let _probe = crate::worker::enter_output(&owner.finished.mapping).map_err(Error::Sdk)?;
    verify_custody(&owner.attempt, &owner.finished.file, Location::Private)?;
    live_lock::lock_file_cancellable(
        &owner.finished.file,
        MAIN_LIFETIME_LOCK,
        Mode::Exclusive,
        cancellation,
    )
    .map_err(Error::Sdk)?;
    let byte_length = inspect_finished(owner)?;
    let sha512 = digest_cancellable(&owner.finished.mapping, byte_length, cancellation)?;
    finish_cancellable(owner, byte_length, cancellation)?;
    Ok((byte_length, sha512))
}

fn finish_cancellable(
    owner: &UnpreparedOutput,
    byte_length: u64,
    cancellation: &CancellationToken,
) -> Result<(), Error> {
    sync_file(&owner.finished.file).map_err(crate::error::Error::from)?;
    cancellation.check().map_err(Error::Sdk)?;
    let final_length = inspect_finished(owner)?;
    if final_length != byte_length {
        return Err(Error::FinishedLengthChanged);
    }
    Ok(())
}

fn inspect_finished(owner: &UnpreparedOutput) -> Result<u64, Error> {
    inspect_exact(
        &owner.attempt,
        &owner.finished.file,
        &owner.finished.mapping,
        owner.finished.meta,
        Location::Private,
    )
}

fn inspect_exact(
    attempt: &OutputAttempt,
    file: &File,
    mapping: &Mapping,
    expected: MetaV4,
    location: Location,
) -> Result<u64, Error> {
    verify_custody(attempt, file, location)?;
    let byte_length = file.metadata().map_err(crate::error::Error::from)?.len();
    let page0 = mapping.page(0, 2).map_err(Error::Sdk)?;
    let page1 = mapping.page(1, 2).map_err(Error::Sdk)?;
    let opened = bootstrap::open_meta_pages(page0, page1, byte_length, OpenMode::ImmutableReader)
        .map_err(|_| Error::Bootstrap)?;
    if opened.meta != expected {
        return Err(Error::FinishedMetaChanged);
    }
    Ok(byte_length)
}

fn verify_custody(attempt: &OutputAttempt, file: &File, location: Location) -> Result<(), Error> {
    let directory = attempt.destination.directory();
    let identity = regular_identity(file, directory.identity())?;
    if identity != attempt.identity {
        return Err(NamespaceError::IdentityChanged.into());
    }
    match location {
        Location::Private => {
            super::gc_barrier::require_source_available(
                directory,
                attempt.attempt_id,
                0,
                ArtifactKind::PrivateOutput,
                DirectoryRole::Destination,
                &attempt.name,
                identity,
            )
            .map_err(Error::Gc)?;
            directory.verify_name(&attempt.name, identity)?;
        }
        Location::Main => {
            directory.verify_name(attempt.destination.main(), identity)?;
        }
    }
    attempt.destination.verify_created(file)?;
    Ok(())
}

fn facts(
    destination: &Destination,
    attempt_id: [u8; 16],
    name: &Name,
    identity: Option<crate::validation::LocalFileIdentity>,
) -> PrivateOutputAttempt {
    PrivateOutputAttempt {
        publication_attempt_id: attempt_id,
        directory_identity: local(destination.directory().identity()),
        basename_encoding: BASENAME_ENCODING_KIND,
        basename: name.bytes().into(),
        identity,
        creation_security: CreationSecurity {
            kind: CREATION_SECURITY_KIND,
            commitment: destination.security_commitment(),
        },
    }
}

#[derive(Clone, Copy)]
enum Location {
    Private,
    Main,
}

impl From<NamespaceError> for Error {
    fn from(value: NamespaceError) -> Self {
        Self::Namespace(value)
    }
}

impl From<crate::error::Error> for Error {
    fn from(value: crate::error::Error) -> Self {
        Self::Sdk(value)
    }
}

#[cfg(all(test, unix))]
#[path = "output_tests.rs"]
mod tests;
