//! Stable ownership of the destination replaced by one publication.

use std::fs::File;

use crate::cancellation::CancellationToken;
use crate::live_lock::{self, Mode};
use crate::live_sidecar::MAIN_LIFETIME_LOCK;

use super::namespace::{
    regular_identity, regular_identity_any_link, regular_link_count, Destination, Identity, Name,
    NamespaceError,
};
use super::output::{self, PreparedOutput};
use super::reservation::Policy;

#[derive(Debug)]
pub(crate) struct PreviousMain {
    pub(crate) file: File,
    pub(crate) identity: Identity,
    pub(crate) byte_length: u64,
    pub(crate) sha512: [u8; 64],
}

#[derive(Debug)]
pub(crate) enum Error {
    Namespace(NamespaceError),
    Sdk(crate::error::Error),
    Output(output::Error),
    SameIdentity,
    ContentChanged,
}

#[derive(Debug)]
pub(crate) struct Failure {
    pub(crate) output: PreparedOutput,
    pub(crate) cause: Error,
}

pub(crate) fn bind(
    output: PreparedOutput,
    cancellation: &CancellationToken,
) -> Result<PreparedOutput, Box<Failure>> {
    bind_with(output, Policy::ReplaceExisting, cancellation)
}

pub(crate) fn bind_no_rollback(
    output: PreparedOutput,
    cancellation: &CancellationToken,
) -> Result<PreparedOutput, Box<Failure>> {
    bind_with(output, Policy::ReplaceExistingNoRollback, cancellation)
}

fn bind_with(
    mut output: PreparedOutput,
    policy: Policy,
    cancellation: &CancellationToken,
) -> Result<PreparedOutput, Box<Failure>> {
    debug_assert!(policy.is_replacement());
    match open(&output, cancellation) {
        Ok(previous) => {
            output.policy = policy;
            output.previous = Some(previous);
            Ok(output)
        }
        Err(cause) => Err(Box::new(Failure { output, cause })),
    }
}

fn open(output: &PreparedOutput, cancellation: &CancellationToken) -> Result<PreviousMain, Error> {
    cancellation.check().map_err(Error::Sdk)?;
    let destination = output.attempt.destination();
    destination
        .directory()
        .require_absent(destination.coordination())?;
    let regular = destination
        .directory()
        .open_regular(destination.main(), true)?
        .ok_or(NamespaceError::Missing)?;
    if regular.identity == output.attempt.identity() {
        return Err(Error::SameIdentity);
    }
    live_lock::lock_cancellable(
        &regular.file,
        MAIN_LIFETIME_LOCK,
        Mode::Exclusive,
        cancellation,
    )
    .map_err(Error::Sdk)?;
    verify_canonical(destination, &regular.file, regular.identity, None)?;
    super::namespace::sync_file(&regular.file).map_err(crate::error::Error::from)?;
    cancellation.check().map_err(Error::Sdk)?;
    let byte_length = regular
        .file
        .metadata()
        .map_err(crate::error::Error::from)?
        .len();
    let sha512 = output::digest_cancellable(&regular.file, byte_length, cancellation)
        .map_err(Error::Output)?;
    verify_canonical(
        destination,
        &regular.file,
        regular.identity,
        Some(byte_length),
    )?;
    Ok(PreviousMain {
        file: regular.file,
        identity: regular.identity,
        byte_length,
        sha512,
    })
}

impl PreviousMain {
    pub(crate) fn verify_canonical_namespace(
        &self,
        destination: &Destination,
    ) -> Result<(), NamespaceError> {
        verify_canonical(
            destination,
            &self.file,
            self.identity,
            Some(self.byte_length),
        )
    }

    pub(crate) fn verify_private_or_retired(
        &self,
        destination: &Destination,
        private_name: &Name,
    ) -> Result<(), NamespaceError> {
        destination.directory().verify()?;
        let identity = regular_identity_any_link(&self.file, destination.directory().identity())?;
        if identity != self.identity
            || self.file.metadata().map_err(NamespaceError::Io)?.len() != self.byte_length
        {
            return Err(NamespaceError::IdentityChanged);
        }
        match regular_link_count(&self.file)? {
            1 => destination
                .directory()
                .verify_name(private_name, self.identity),
            0 => destination.directory().require_absent(private_name),
            links => Err(NamespaceError::LinkCount(links)),
        }
    }

    pub(crate) fn verify_retired(
        &self,
        destination: &Destination,
        private_name: &Name,
    ) -> Result<(), NamespaceError> {
        destination.directory().verify()?;
        destination.directory().require_absent(private_name)?;
        let identity = regular_identity_any_link(&self.file, destination.directory().identity())?;
        if identity != self.identity {
            return Err(NamespaceError::IdentityChanged);
        }
        let metadata = self.file.metadata().map_err(NamespaceError::Io)?;
        if metadata.len() != self.byte_length {
            return Err(NamespaceError::IdentityChanged);
        }
        let links = regular_link_count(&self.file)?;
        if links != 0 {
            return Err(NamespaceError::LinkCount(links));
        }
        Ok(())
    }

    pub(crate) fn verify_content(
        &self,
        destination: &Destination,
        cancellation: Option<&CancellationToken>,
    ) -> Result<(), Error> {
        self.verify_canonical_namespace(destination)?;
        let digest = match cancellation {
            Some(cancellation) => {
                output::digest_cancellable(&self.file, self.byte_length, cancellation)
            }
            None => output::digest(&self.file, self.byte_length),
        }
        .map_err(Error::Output)?;
        self.verify_canonical_namespace(destination)?;
        if digest != self.sha512 {
            return Err(Error::ContentChanged);
        }
        Ok(())
    }
}

fn verify_canonical(
    destination: &Destination,
    file: &File,
    expected: Identity,
    expected_length: Option<u64>,
) -> Result<(), NamespaceError> {
    destination.directory().verify()?;
    destination
        .directory()
        .verify_name(destination.main(), expected)?;
    let identity = regular_identity(file, destination.directory().identity())?;
    if identity != expected {
        return Err(NamespaceError::IdentityChanged);
    }
    if expected_length.is_some_and(|length| {
        file.metadata()
            .map(|metadata| metadata.len() != length)
            .unwrap_or(true)
    }) {
        return Err(NamespaceError::IdentityChanged);
    }
    Ok(())
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
