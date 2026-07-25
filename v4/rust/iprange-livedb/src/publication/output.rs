//! Owned construction and one-pass preparation of an immutable output.

use std::fs::File;
use std::path::Path;

use crate::bootstrap::{self, BootstrapError, OpenMode};
use crate::cancellation::CancellationToken;
use crate::contract::{MetaV4, PAGE_SIZE};
use crate::immutable_output::Finished;
use crate::live_lock::{self, Mode};
use crate::live_sidecar::MAIN_LIFETIME_LOCK;
use crate::{file_io, random};

use super::namespace::{
    regular_identity, regular_identity_any_link, Destination, Identity, Name, NamespaceError,
};
use super::replacement::PreviousMain;
use super::{CreationSecurity, PrivateOutputAttempt};

const POSIX_KIND: u16 = 1;

#[path = "output_digest.rs"]
mod output_digest;
pub(super) use output_digest::{digest, digest_cancellable};
#[cfg(test)]
use output_digest::{digest_with, DIGEST_BUFFER_SIZE};
#[path = "output_resume.rs"]
mod output_resume;
pub(super) use output_resume::ResumedOutput;

#[derive(Debug)]
pub(crate) enum Error {
    Namespace(NamespaceError),
    Sdk(crate::error::Error),
    Bootstrap(BootstrapError),
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
        let attempt_id = random::nonzero_128().map_err(Error::Sdk)?;
        let name = destination
            .output_name(attempt_id)
            .map_err(Error::Namespace)?;
        let file = destination
            .directory()
            .create(&name)
            .map_err(Error::Namespace)?;
        Ok(Self {
            destination,
            attempt_id,
            name,
            file,
        })
    }

    pub(crate) fn facts(&self) -> PrivateOutputAttempt {
        let identity =
            regular_identity_any_link(&self.file, self.destination.directory().identity().device)
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
    // Boxing this failure would allocate on the publication path and obscure ownership.
    #[allow(clippy::result_large_err)]
    pub(crate) fn prepare(
        self,
        finished: Finished,
    ) -> Result<PreparedOutput, Failure<UnpreparedOutput>> {
        let owner = UnpreparedOutput {
            attempt: self,
            finished,
        };
        match prepare(&owner) {
            Ok((byte_length, sha512)) => {
                let UnpreparedOutput { attempt, finished } = owner;
                Ok(PreparedOutput {
                    attempt,
                    file: finished.file,
                    meta: finished.meta,
                    byte_length,
                    sha512,
                    previous: None,
                })
            }
            Err(cause) => Err(Failure { owner, cause }),
        }
    }

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
                Ok(PreparedOutput {
                    attempt,
                    file: finished.file,
                    meta: finished.meta,
                    byte_length,
                    sha512,
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

#[derive(Debug)]
pub(crate) struct UnpreparedOutput {
    pub(crate) attempt: OutputAttempt,
    pub(crate) finished: Finished,
}

#[derive(Debug)]
pub(crate) struct PreparedOutput {
    pub(crate) attempt: OutputAttempt,
    pub(crate) file: File,
    pub(crate) meta: MetaV4,
    pub(crate) byte_length: u64,
    pub(crate) sha512: [u8; 64],
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
        let length = inspect_exact(&self.attempt, &self.file, self.meta, location)?;
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
    let identity = regular_identity(&created.file, directory.identity().device)?;
    directory.verify_name(&created.name, identity)?;
    created.destination.secure_created(&created.file)?;
    let secured = regular_identity(&created.file, directory.identity().device)?;
    if secured != identity {
        return Err(NamespaceError::IdentityChanged.into());
    }
    directory.verify_name(&created.name, identity)?;
    created.destination.verify_created(&created.file)?;
    Ok(identity)
}

fn prepare(owner: &UnpreparedOutput) -> Result<(u64, [u8; 64]), Error> {
    verify_custody(&owner.attempt, &owner.finished.file, Location::Private)?;
    live_lock::lock(&owner.finished.file, MAIN_LIFETIME_LOCK, Mode::Exclusive)
        .map_err(Error::Sdk)?;
    let byte_length = inspect_finished(owner)?;
    let sha512 = digest(&owner.finished.file, byte_length)?;
    owner
        .finished
        .file
        .sync_all()
        .map_err(crate::error::Error::from)?;
    let final_length = inspect_finished(owner)?;
    if final_length != byte_length {
        return Err(Error::FinishedLengthChanged);
    }
    Ok((byte_length, sha512))
}

fn prepare_cancellable(
    owner: &UnpreparedOutput,
    cancellation: &CancellationToken,
) -> Result<(u64, [u8; 64]), Error> {
    cancellation.check().map_err(Error::Sdk)?;
    verify_custody(&owner.attempt, &owner.finished.file, Location::Private)?;
    live_lock::lock_cancellable(
        &owner.finished.file,
        MAIN_LIFETIME_LOCK,
        Mode::Exclusive,
        cancellation,
    )
    .map_err(Error::Sdk)?;
    let byte_length = inspect_finished(owner)?;
    let sha512 = digest_cancellable(&owner.finished.file, byte_length, cancellation)?;
    finish_cancellable(owner, byte_length, cancellation)?;
    Ok((byte_length, sha512))
}

fn finish_cancellable(
    owner: &UnpreparedOutput,
    byte_length: u64,
    cancellation: &CancellationToken,
) -> Result<(), Error> {
    owner
        .finished
        .file
        .sync_all()
        .map_err(crate::error::Error::from)?;
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
        owner.finished.meta,
        Location::Private,
    )
}

fn inspect_exact(
    attempt: &OutputAttempt,
    file: &File,
    expected: MetaV4,
    location: Location,
) -> Result<u64, Error> {
    verify_custody(attempt, file, location)?;
    let byte_length = file.metadata().map_err(crate::error::Error::from)?.len();
    let mut pages = [0; 2 * PAGE_SIZE];
    file_io::read_exact_at(file, &mut pages, 0).map_err(Error::Sdk)?;
    let page0 = (&pages[..PAGE_SIZE]).try_into().expect("fixed meta page");
    let page1 = (&pages[PAGE_SIZE..]).try_into().expect("fixed meta page");
    let opened = bootstrap::open_meta_pages(page0, page1, byte_length, OpenMode::ImmutableReader)
        .map_err(Error::Bootstrap)?;
    if opened.meta != expected {
        return Err(Error::FinishedMetaChanged);
    }
    Ok(byte_length)
}

fn verify_custody(attempt: &OutputAttempt, file: &File, location: Location) -> Result<(), Error> {
    let directory = attempt.destination.directory();
    let identity = regular_identity(file, directory.identity().device)?;
    if identity != attempt.identity {
        return Err(NamespaceError::IdentityChanged.into());
    }
    match location {
        Location::Private => directory.verify_name(&attempt.name, identity)?,
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
        basename_encoding: POSIX_KIND,
        basename: name.bytes().into(),
        identity,
        creation_security: CreationSecurity {
            kind: POSIX_KIND,
            commitment: destination.security_commitment(),
        },
    }
}

fn local(identity: Identity) -> crate::validation::LocalFileIdentity {
    crate::validation::LocalFileIdentity {
        kind: POSIX_KIND,
        bytes: identity.encode(),
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

#[cfg(test)]
#[path = "output_tests.rs"]
mod tests;
