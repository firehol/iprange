//! Owned construction and one-pass preparation of an immutable output.

use std::fs::File;
use std::path::Path;

use sha2::{Digest, Sha512};

use crate::bootstrap::{self, BootstrapError, OpenMode};
use crate::cancellation::CancellationToken;
use crate::contract::{MetaV4, PAGE_SIZE};
use crate::immutable_output::Finished;
use crate::live_lock::{self, Mode};
use crate::live_sidecar::MAIN_LIFETIME_LOCK;
use crate::{file_io, random};

use super::namespace::{regular_identity, Destination, Identity, Name, NamespaceError};

const DIGEST_BUFFER_SIZE: usize = 64 * 1024;

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
        let destination = Destination::bind(path).map_err(Error::Namespace)?;
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
}

impl PreparedOutput {
    pub(super) fn resume(
        destination: Destination,
        attempt_id: [u8; 16],
        inspected: super::file_inspection::Inspected,
    ) -> Result<Self, Error> {
        let name = destination
            .output_name(attempt_id)
            .map_err(Error::Namespace)?;
        Ok(Self {
            attempt: OutputAttempt {
                destination,
                attempt_id,
                name,
                identity: inspected.identity,
            },
            file: inspected.file,
            meta: inspected.meta,
            byte_length: inspected.byte_length,
            sha512: inspected.sha512,
        })
    }

    pub(crate) fn verify_private(&self) -> Result<(), Error> {
        self.verify(Location::Private)
    }

    pub(crate) fn verify_main(&self) -> Result<(), Error> {
        self.verify(Location::Main)
    }

    fn verify(&self, location: Location) -> Result<(), Error> {
        let length = inspect_exact(&self.attempt, &self.file, self.meta, location)?;
        if length != self.byte_length {
            return Err(Error::FinishedLengthChanged);
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
            directory.require_absent(&attempt.name)?;
        }
    }
    attempt.destination.verify_created(file)?;
    Ok(())
}

#[derive(Clone, Copy)]
enum Location {
    Private,
    Main,
}

pub(super) fn digest(file: &File, byte_length: u64) -> Result<[u8; 64], Error> {
    digest_with(byte_length, |offset, output| {
        file_io::read_exact_at(file, output, offset).map_err(Error::Sdk)
    })
}

pub(super) fn digest_cancellable(
    file: &File,
    byte_length: u64,
    cancellation: &CancellationToken,
) -> Result<[u8; 64], Error> {
    let result = digest_with(byte_length, |offset, output| {
        cancellation.check().map_err(Error::Sdk)?;
        file_io::read_exact_at(file, output, offset).map_err(Error::Sdk)
    });
    cancellation.check().map_err(Error::Sdk)?;
    result
}

fn digest_with(
    byte_length: u64,
    mut read: impl FnMut(u64, &mut [u8]) -> Result<(), Error>,
) -> Result<[u8; 64], Error> {
    let mut hasher = Sha512::new();
    let mut buffer = [0; DIGEST_BUFFER_SIZE];
    let mut offset = 0;
    while offset < byte_length {
        let remaining = byte_length - offset;
        let length = usize::try_from(remaining.min(DIGEST_BUFFER_SIZE as u64))
            .expect("digest chunk fits usize");
        read(offset, &mut buffer[..length])?;
        hasher.update(&buffer[..length]);
        offset = offset
            .checked_add(length as u64)
            .ok_or(Error::FinishedLengthChanged)?;
    }
    Ok(hasher.finalize().into())
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
