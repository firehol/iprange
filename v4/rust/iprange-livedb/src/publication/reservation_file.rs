//! Durable ownership states for a publication reservation inode.

use std::fs::File;

use crate::contract::PAGE_SIZE;
use crate::live_lock::{self, Mode};
use crate::{error, file_io};

use super::namespace::{regular_identity, sync_file, Identity, Name, NamespaceError};
use super::output::{self, PreparedOutput};
use super::reservation::{Header, Policy, SelectError, State};

#[path = "reservation_verify.rs"]
mod verification;
use verification::{select_exact, Expected, OutputLocation, ReservationLocation};

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
    pub(crate) state1_selected: bool,
}

impl ReservationDraft {
    pub(crate) fn create(output: &PreparedOutput) -> Result<Self, Error> {
        output.verify_private().map_err(Error::Output)?;
        let destination = output.attempt.destination();
        let name = destination
            .reservation_name(output.attempt.attempt_id())
            .map_err(Error::Namespace)?;
        let file = destination.create(&name).map_err(Error::Namespace)?;
        Ok(Self {
            name,
            file,
            identity: None,
            header: None,
            state1_selected: false,
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

    #[allow(clippy::result_large_err)]
    pub(crate) fn resume_armed(
        self,
        output: &PreparedOutput,
    ) -> Result<ArmedReservation, Failure<Self>> {
        if self.header.state != State::MainMayHaveBeenAttempted || self.header.sequence != 2 {
            return Err(Failure {
                owner: self,
                cause: Error::HeaderInvariant,
            });
        }
        if let Err(cause) = verify_canonical_reservation(&self, output) {
            return Err(Failure { owner: self, cause });
        }
        Ok(ArmedReservation {
            name: self.name,
            file: self.file,
            identity: self.identity,
            header: self.header,
        })
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

impl ArmedReservation {
    pub(crate) fn verify_before_main(&self, output: &PreparedOutput) -> Result<(), Error> {
        verify_canonical(
            &self.file,
            self.identity,
            &self.name,
            self.header,
            1,
            output,
            OutputLocation::Private,
        )
    }

    pub(crate) fn verify_after_main(&self, output: &PreparedOutput) -> Result<(), Error> {
        verify_canonical(
            &self.file,
            self.identity,
            &self.name,
            self.header,
            1,
            output,
            OutputLocation::Main,
        )
    }
}

fn initialize(draft: &mut ReservationDraft, output: &PreparedOutput) -> Result<(), Error> {
    prepare_header(draft, output)?;
    write_state1(draft)?;
    lock_state1(draft, output)
}

fn prepare_header(draft: &mut ReservationDraft, output: &PreparedOutput) -> Result<(), Error> {
    output.verify_private().map_err(Error::Output)?;
    if output.previous.is_some() {
        output
            .verify_destination_before_main()
            .map_err(Error::Output)?;
    }
    let destination = output.attempt.destination();
    let directory = destination.directory();
    let identity = regular_identity(&draft.file, directory.identity())?;
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
    sync_file(&draft.file).map_err(error::Error::from)?;
    crate::fault::crash("publication.after_reservation_state1_sync");
    Ok(())
}

fn lock_state1(draft: &mut ReservationDraft, output: &PreparedOutput) -> Result<(), Error> {
    lock_state1_with(draft, output, || Ok(()))
}

fn lock_state1_with(
    draft: &mut ReservationDraft,
    output: &PreparedOutput,
    after_selection: impl FnOnce() -> Result<(), Error>,
) -> Result<(), Error> {
    let header = draft.header.ok_or(Error::HeaderInvariant)?;
    verify_private(draft, output, header, 0)?;
    draft.state1_selected = true;
    after_selection()?;
    live_lock::lock(&draft.file, OPERATION_LOCK, Mode::Exclusive)?;
    verify_private(draft, output, header, 0)
}

fn acquire(owner: &mut AcquiringReservation, output: &PreparedOutput) -> Result<(), Error> {
    verify_private_reservation(&owner.reservation, output)?;
    let destination = output.attempt.destination();
    destination.directory().verify()?;
    owner.namespace_call_started = true;
    destination.directory().rename_noreplace(
        &owner.reservation.name,
        &owner.reservation.file,
        destination.coordination(),
    )?;
    crate::fault::crash("publication.after_reservation_rename");
    destination.directory().sync()?;
    crate::fault::crash("publication.after_reservation_directory_sync");
    verify_canonical(
        &owner.reservation.file,
        owner.reservation.identity,
        &owner.reservation.name,
        owner.reservation.header,
        selected_block(owner.reservation.header),
        output,
        OutputLocation::Private,
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
    if output.previous.is_some() {
        output
            .verify_destination_before_main()
            .map_err(Error::Output)?;
    } else {
        let destination = output.attempt.destination();
        destination.directory().require_absent(destination.main())?;
    }
    let mut block = [0; PAGE_SIZE];
    target.encode(&mut block);
    file_io::write_exact_at(&owner.reservation.file, &block, PAGE_SIZE as u64)?;
    crate::fault::crash("publication.after_reservation_state2_write");
    sync_file(&owner.reservation.file).map_err(error::Error::from)?;
    crate::fault::crash("publication.after_reservation_state2_sync");
    select_exact(&owner.reservation.file, target, 1)?;
    owner.state2_selected = true;
    crate::fault::crash("publication.after_reservation_state2_selection");
    after_selection()?;
    verify_canonical(
        &owner.reservation.file,
        owner.reservation.identity,
        &owner.reservation.name,
        target,
        1,
        output,
        OutputLocation::Private,
    )
}

fn header(output: &PreparedOutput, identity: Identity) -> Result<Header, Error> {
    let destination = output.attempt.destination();
    let basename_len =
        u32::try_from(destination.main().bytes().len()).map_err(|_| Error::HeaderInvariant)?;
    let previous = output
        .previous
        .as_ref()
        .map(|previous| super::reservation::Previous {
            identity: previous.identity.encode(),
            byte_length: previous.byte_length,
            sha512: previous.sha512,
        });
    Ok(Header {
        state: State::Prepared,
        database_id: output.meta.database_id,
        transaction_id: output.meta.txn_id,
        commit_nonce: output.meta.commit_nonce,
        attempt_id: output.attempt.attempt_id(),
        reservation_identity: identity.encode(),
        policy: if previous.is_some() {
            Policy::ReplaceExisting
        } else {
            Policy::FailIfExists
        },
        output_byte_length: output.byte_length,
        output_identity: output.attempt.identity().encode(),
        output_sha512: output.sha512,
        previous,
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
    verification::verify(
        &draft.file,
        output,
        Expected {
            identity: draft.identity.ok_or(Error::HeaderInvariant)?,
            private_name: &draft.name,
            header,
            block,
            reservation_location: ReservationLocation::Private,
            output_location: OutputLocation::Private,
        },
    )
}

fn verify_private_reservation(
    reservation: &PrivateReservation,
    output: &PreparedOutput,
) -> Result<(), Error> {
    verification::verify(
        &reservation.file,
        output,
        Expected {
            identity: reservation.identity,
            private_name: &reservation.name,
            header: reservation.header,
            block: selected_block(reservation.header),
            reservation_location: ReservationLocation::Private,
            output_location: OutputLocation::Private,
        },
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
        selected_block(reservation.header),
        output,
        OutputLocation::Private,
    )
}

fn selected_block(header: Header) -> usize {
    usize::try_from(header.sequence - 1).expect("selected sequence is one or two")
}

fn verify_canonical(
    file: &File,
    identity: Identity,
    private_name: &Name,
    header: Header,
    block: usize,
    output: &PreparedOutput,
    output_location: OutputLocation,
) -> Result<(), Error> {
    verification::verify(
        file,
        output,
        Expected {
            identity,
            private_name,
            header,
            block,
            reservation_location: ReservationLocation::Canonical,
            output_location,
        },
    )
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
