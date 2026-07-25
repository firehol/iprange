//! Durable ownership states for a publication reservation inode.

use std::fs::File;

use crate::contract::PAGE_SIZE;
use crate::live_lock::{self, Mode};
use crate::{error, file_io};

use super::namespace::{regular_identity, Identity, Name, NamespaceError};
use super::output::{self, PreparedOutput};
use super::reservation::{self, Header, Policy, SelectError, Selected, State};

const FILE_SIZE: usize = 2 * PAGE_SIZE;
const OPERATION_LOCK: u64 = 0;

#[derive(Debug)]
pub(crate) enum Error {
    Namespace(NamespaceError),
    Sdk(error::Error),
    Output(output::Error),
    Codec(SelectError),
    HeaderChanged,
    HeaderInvariant,
    LengthChanged,
}

#[derive(Debug)]
pub(crate) struct Failure<T> {
    pub(crate) owner: T,
    pub(crate) cause: Error,
}

#[derive(Debug)]
pub(crate) struct ReservationDraft {
    pub(crate) name: Name,
    pub(crate) file: File,
    pub(crate) identity: Option<Identity>,
    pub(crate) header: Option<Header>,
}

impl ReservationDraft {
    pub(crate) fn create(output: &PreparedOutput) -> Result<Self, Error> {
        output.verify_private().map_err(Error::Output)?;
        let destination = output.attempt.destination();
        let name = destination
            .reservation_name(output.attempt.attempt_id())
            .map_err(Error::Namespace)?;
        let file = destination
            .directory()
            .create(&name)
            .map_err(Error::Namespace)?;
        Ok(Self {
            name,
            file,
            identity: None,
            header: None,
        })
    }

    // The inline owner preserves all cleanup facts without allocating.
    #[allow(clippy::result_large_err)]
    pub(crate) fn initialize(
        mut self,
        output: &PreparedOutput,
    ) -> Result<PrivateReservation, Failure<Self>> {
        match initialize(&mut self, output) {
            Ok(()) => Ok(PrivateReservation {
                name: self.name,
                file: self.file,
                identity: self.identity.expect("initialized reservation identity"),
                header: self.header.expect("initialized reservation header"),
            }),
            Err(cause) => Err(Failure { owner: self, cause }),
        }
    }
}

#[derive(Debug)]
pub(crate) struct PrivateReservation {
    pub(crate) name: Name,
    pub(crate) file: File,
    pub(crate) identity: Identity,
    pub(crate) header: Header,
}

impl PrivateReservation {
    // The inline owner keeps the locked descriptor and both exact names on failure.
    #[allow(clippy::result_large_err)]
    pub(crate) fn acquire(
        self,
        output: &PreparedOutput,
    ) -> Result<CanonicalReservation, Failure<AcquiringReservation>> {
        let mut owner = AcquiringReservation {
            reservation: self,
            namespace_call_started: false,
        };
        match acquire(&mut owner, output) {
            Ok(()) => Ok(CanonicalReservation {
                name: owner.reservation.name,
                file: owner.reservation.file,
                identity: owner.reservation.identity,
                header: owner.reservation.header,
            }),
            Err(cause) => Err(Failure { owner, cause }),
        }
    }
}

#[derive(Debug)]
pub(crate) struct AcquiringReservation {
    pub(crate) reservation: PrivateReservation,
    pub(crate) namespace_call_started: bool,
}

#[derive(Debug)]
pub(crate) struct CanonicalReservation {
    pub(crate) name: Name,
    pub(crate) file: File,
    pub(crate) identity: Identity,
    pub(crate) header: Header,
}

impl CanonicalReservation {
    // This failure state records whether durable state 2 was selected.
    #[allow(clippy::result_large_err)]
    pub(crate) fn arm(
        self,
        output: &PreparedOutput,
    ) -> Result<ArmedReservation, Failure<ArmingReservation>> {
        let Some(target) = self.header.state2() else {
            return Err(Failure {
                owner: ArmingReservation {
                    reservation: self,
                    target: None,
                    state2_selected: false,
                },
                cause: Error::HeaderInvariant,
            });
        };
        let mut owner = ArmingReservation {
            reservation: self,
            target: Some(target),
            state2_selected: false,
        };
        match arm(&mut owner, output) {
            Ok(()) => Ok(ArmedReservation {
                name: owner.reservation.name,
                file: owner.reservation.file,
                identity: owner.reservation.identity,
                header: target,
            }),
            Err(cause) => Err(Failure { owner, cause }),
        }
    }
}

#[derive(Debug)]
pub(crate) struct ArmingReservation {
    pub(crate) reservation: CanonicalReservation,
    pub(crate) target: Option<Header>,
    pub(crate) state2_selected: bool,
}

#[derive(Debug)]
pub(crate) struct ArmedReservation {
    pub(crate) name: Name,
    pub(crate) file: File,
    pub(crate) identity: Identity,
    pub(crate) header: Header,
}

fn initialize(draft: &mut ReservationDraft, output: &PreparedOutput) -> Result<(), Error> {
    prepare_header(draft, output)?;
    write_state1(draft)?;
    lock_state1(draft, output)
}

fn prepare_header(draft: &mut ReservationDraft, output: &PreparedOutput) -> Result<(), Error> {
    output.verify_private().map_err(Error::Output)?;
    let destination = output.attempt.destination();
    let directory = destination.directory();
    let identity = regular_identity(&draft.file, directory.identity().device)?;
    draft.identity = Some(identity);
    directory.verify_name(&draft.name, identity)?;
    destination.secure_created(&draft.file)?;
    draft
        .file
        .set_len(FILE_SIZE as u64)
        .map_err(error::Error::from)?;
    draft.header = Some(header(output, identity)?);
    Ok(())
}

fn write_state1(draft: &ReservationDraft) -> Result<(), Error> {
    let header = draft.header.ok_or(Error::HeaderInvariant)?;
    let mut block = [0; PAGE_SIZE];
    header.encode(&mut block);
    file_io::write_exact_at(&draft.file, &block, 0)?;
    draft.file.sync_all().map_err(error::Error::from)?;
    Ok(())
}

fn lock_state1(draft: &ReservationDraft, output: &PreparedOutput) -> Result<(), Error> {
    let header = draft.header.ok_or(Error::HeaderInvariant)?;
    verify_private(draft, output, header, 0)?;
    live_lock::lock(&draft.file, OPERATION_LOCK, Mode::Exclusive)?;
    verify_private(draft, output, header, 0)
}

fn acquire(owner: &mut AcquiringReservation, output: &PreparedOutput) -> Result<(), Error> {
    verify_private_reservation(&owner.reservation, output)?;
    let destination = output.attempt.destination();
    destination.directory().verify()?;
    owner.namespace_call_started = true;
    destination
        .directory()
        .rename_noreplace(&owner.reservation.name, destination.coordination())?;
    destination.directory().sync()?;
    verify_canonical(
        &owner.reservation.file,
        owner.reservation.identity,
        &owner.reservation.name,
        owner.reservation.header,
        0,
        output,
    )
}

fn arm(owner: &mut ArmingReservation, output: &PreparedOutput) -> Result<(), Error> {
    arm_with(owner, output, || Ok(()))
}

fn arm_with(
    owner: &mut ArmingReservation,
    output: &PreparedOutput,
    after_selection: impl FnOnce() -> Result<(), Error>,
) -> Result<(), Error> {
    let target = owner.target.ok_or(Error::HeaderInvariant)?;
    verify_canonical_reservation(&owner.reservation, output)?;
    let mut block = [0; PAGE_SIZE];
    target.encode(&mut block);
    file_io::write_exact_at(&owner.reservation.file, &block, PAGE_SIZE as u64)?;
    owner
        .reservation
        .file
        .sync_all()
        .map_err(error::Error::from)?;
    select_exact(&owner.reservation.file, target, 1)?;
    owner.state2_selected = true;
    after_selection()?;
    verify_canonical(
        &owner.reservation.file,
        owner.reservation.identity,
        &owner.reservation.name,
        target,
        1,
        output,
    )
}

fn header(output: &PreparedOutput, identity: Identity) -> Result<Header, Error> {
    let destination = output.attempt.destination();
    let basename_len =
        u32::try_from(destination.main().bytes().len()).map_err(|_| Error::HeaderInvariant)?;
    Ok(Header {
        state: State::Prepared,
        database_id: output.meta.database_id,
        transaction_id: output.meta.txn_id,
        commit_nonce: output.meta.commit_nonce,
        attempt_id: output.attempt.attempt_id(),
        reservation_identity: identity.encode(),
        policy: Policy::FailIfExists,
        output_byte_length: output.byte_length,
        output_identity: output.attempt.identity().encode(),
        output_sha512: output.sha512,
        previous: None,
        basename_len,
        basename_commitment: destination.basename_commitment(),
        security_commitment: destination.security_commitment(),
        sequence: 1,
    })
}

fn verify_private(
    draft: &ReservationDraft,
    output: &PreparedOutput,
    header: Header,
    block: usize,
) -> Result<(), Error> {
    verify(
        &draft.file,
        draft.identity.ok_or(Error::HeaderInvariant)?,
        &draft.name,
        header,
        block,
        output,
        false,
    )
}

fn verify_private_reservation(
    reservation: &PrivateReservation,
    output: &PreparedOutput,
) -> Result<(), Error> {
    verify(
        &reservation.file,
        reservation.identity,
        &reservation.name,
        reservation.header,
        0,
        output,
        false,
    )
}

fn verify_canonical_reservation(
    reservation: &CanonicalReservation,
    output: &PreparedOutput,
) -> Result<(), Error> {
    verify_canonical(
        &reservation.file,
        reservation.identity,
        &reservation.name,
        reservation.header,
        0,
        output,
    )
}

fn verify_canonical(
    file: &File,
    identity: Identity,
    private_name: &Name,
    header: Header,
    block: usize,
    output: &PreparedOutput,
) -> Result<(), Error> {
    verify(file, identity, private_name, header, block, output, true)
}

fn verify(
    file: &File,
    identity: Identity,
    private_name: &Name,
    header: Header,
    block: usize,
    output: &PreparedOutput,
    canonical: bool,
) -> Result<(), Error> {
    verify_inode(file, identity, output)?;
    verify_location(private_name, identity, output, canonical)?;
    verify_contents(file, header, block)
}

fn verify_inode(file: &File, identity: Identity, output: &PreparedOutput) -> Result<(), Error> {
    output.verify_private().map_err(Error::Output)?;
    let destination = output.attempt.destination();
    let directory = destination.directory();
    directory.verify()?;
    if regular_identity(file, directory.identity().device)? != identity {
        return Err(NamespaceError::IdentityChanged.into());
    }
    Ok(destination.verify_created(file)?)
}

fn verify_location(
    private_name: &Name,
    identity: Identity,
    output: &PreparedOutput,
    canonical: bool,
) -> Result<(), Error> {
    let destination = output.attempt.destination();
    let directory = destination.directory();
    let name = if canonical {
        destination.coordination()
    } else {
        private_name
    };
    directory.verify_name(name, identity)?;
    if canonical {
        directory.require_absent(private_name)?;
    }
    Ok(())
}

fn verify_contents(file: &File, header: Header, block: usize) -> Result<(), Error> {
    if file.metadata().map_err(error::Error::from)?.len() != FILE_SIZE as u64 {
        return Err(Error::LengthChanged);
    }
    select_exact(file, header, block)
}

fn select_exact(file: &File, header: Header, block: usize) -> Result<(), Error> {
    let mut bytes = [0; FILE_SIZE];
    file_io::read_exact_at(file, &mut bytes, 0)?;
    if reservation::select(&bytes).map_err(Error::Codec)? != (Selected { header, block }) {
        return Err(Error::HeaderChanged);
    }
    Ok(())
}

impl From<NamespaceError> for Error {
    fn from(value: NamespaceError) -> Self {
        Self::Namespace(value)
    }
}

impl From<error::Error> for Error {
    fn from(value: error::Error) -> Self {
        Self::Sdk(value)
    }
}

#[cfg(test)]
#[path = "reservation_file_tests.rs"]
mod tests;
