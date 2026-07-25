//! Atomic main-name publication and exact reservation retirement.

use std::os::unix::fs::MetadataExt;

use crate::error;

use super::namespace::NamespaceError;
use super::output::{self, PreparedOutput};
use super::reservation::Header;
use super::reservation_file::{self, ArmedReservation};

#[derive(Debug)]
pub(crate) enum Error {
    Namespace(NamespaceError),
    Sdk(error::Error),
    Output(output::Error),
    Reservation(reservation_file::Error),
    ReservationLinkCount(u64),
    Injected,
}

#[derive(Debug)]
pub(crate) struct Failure<T> {
    pub(crate) owner: T,
    pub(crate) cause: Error,
}

#[derive(Debug)]
pub(crate) struct MainAttempt {
    pub(crate) output: PreparedOutput,
    pub(crate) reservation: ArmedReservation,
    pub(crate) main_call_started: bool,
    pub(crate) rename_succeeded: bool,
    pub(crate) desired_proven: bool,
}

#[derive(Debug)]
pub(crate) struct PublishedMain {
    pub(crate) output: PreparedOutput,
    pub(crate) reservation: ArmedReservation,
}

#[derive(Debug)]
pub(crate) struct RetiringMain {
    pub(crate) published: PublishedMain,
    pub(crate) reservation_unlinked: bool,
    pub(crate) directory_synced: bool,
    pub(crate) reservation_retired_proven: bool,
}

#[derive(Debug)]
pub(crate) struct PublishedOutput {
    pub(crate) output: PreparedOutput,
    pub(crate) reservation_identity: super::namespace::Identity,
    pub(crate) reservation_header: Header,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Point {
    MainRenamed,
    MainSynced,
    DirectorySynced,
    DesiredProven,
    ReservationUnlinked,
    RetirementSynced,
}

// The fixed owner carries both locked inodes without a failure-path allocation.
#[allow(clippy::result_large_err)]
pub(crate) fn publish(
    output: PreparedOutput,
    reservation: ArmedReservation,
) -> Result<PublishedMain, Failure<MainAttempt>> {
    publish_with(output, reservation, |_| Ok(()))
}

#[allow(clippy::result_large_err)]
fn publish_with(
    output: PreparedOutput,
    reservation: ArmedReservation,
    mut checkpoint: impl FnMut(Point) -> Result<(), Error>,
) -> Result<PublishedMain, Failure<MainAttempt>> {
    let mut owner = MainAttempt {
        output,
        reservation,
        main_call_started: false,
        rename_succeeded: false,
        desired_proven: false,
    };
    match publish_steps(&mut owner, &mut checkpoint) {
        Ok(()) => Ok(PublishedMain {
            output: owner.output,
            reservation: owner.reservation,
        }),
        Err(cause) => Err(Failure { owner, cause }),
    }
}

fn publish_steps(
    owner: &mut MainAttempt,
    checkpoint: &mut impl FnMut(Point) -> Result<(), Error>,
) -> Result<(), Error> {
    verify_before_main(owner)?;
    rename_main(owner)?;
    checkpoint(Point::MainRenamed)?;
    synchronize_main(owner, checkpoint)?;
    prove_main(owner, checkpoint)
}

fn verify_before_main(owner: &MainAttempt) -> Result<(), Error> {
    owner
        .reservation
        .verify_before_main(&owner.output)
        .map_err(Error::Reservation)?;
    let destination = owner.output.attempt.destination();
    destination.directory().require_absent(destination.main())?;
    Ok(())
}

fn rename_main(owner: &mut MainAttempt) -> Result<(), Error> {
    let destination = owner.output.attempt.destination();
    owner.main_call_started = true;
    destination
        .directory()
        .rename_noreplace(owner.output.attempt.name(), destination.main())?;
    owner.rename_succeeded = true;
    crate::fault::crash("publication.after_main_rename");
    Ok(())
}

fn synchronize_main(
    owner: &MainAttempt,
    checkpoint: &mut impl FnMut(Point) -> Result<(), Error>,
) -> Result<(), Error> {
    owner.output.file.sync_all().map_err(error::Error::from)?;
    crate::fault::crash("publication.after_main_sync");
    checkpoint(Point::MainSynced)?;
    owner.output.attempt.destination().directory().sync()?;
    crate::fault::crash("publication.after_main_directory_sync");
    checkpoint(Point::DirectorySynced)
}

fn prove_main(
    owner: &mut MainAttempt,
    checkpoint: &mut impl FnMut(Point) -> Result<(), Error>,
) -> Result<(), Error> {
    owner.output.attempt.destination().directory().verify()?;
    owner.output.verify_main().map_err(Error::Output)?;
    owner.desired_proven = true;
    crate::fault::crash("publication.after_main_proof");
    checkpoint(Point::DesiredProven)?;
    owner
        .reservation
        .verify_after_main(&owner.output)
        .map_err(Error::Reservation)
}

impl PublishedMain {
    // Retirement failure preserves the already-proven main and exact cleanup state.
    #[allow(clippy::result_large_err)]
    pub(crate) fn retire(self) -> Result<PublishedOutput, Failure<RetiringMain>> {
        self.retire_with(|_| Ok(()))
    }

    #[allow(clippy::result_large_err)]
    fn retire_with(
        self,
        mut checkpoint: impl FnMut(Point) -> Result<(), Error>,
    ) -> Result<PublishedOutput, Failure<RetiringMain>> {
        let mut owner = RetiringMain {
            published: self,
            reservation_unlinked: false,
            directory_synced: false,
            reservation_retired_proven: false,
        };
        match retire_steps(&mut owner, &mut checkpoint) {
            Ok(()) => {
                let RetiringMain { published, .. } = owner;
                Ok(PublishedOutput {
                    reservation_identity: published.reservation.identity,
                    reservation_header: published.reservation.header,
                    output: published.output,
                })
            }
            Err(cause) => Err(Failure { owner, cause }),
        }
    }
}

fn retire_steps(
    owner: &mut RetiringMain,
    checkpoint: &mut impl FnMut(Point) -> Result<(), Error>,
) -> Result<(), Error> {
    verify_published(&owner.published)?;
    unlink_reservation(owner)?;
    checkpoint(Point::ReservationUnlinked)?;
    sync_retirement(owner)?;
    checkpoint(Point::RetirementSynced)?;
    verify_retired(owner)
}

fn verify_published(published: &PublishedMain) -> Result<(), Error> {
    published.output.verify_main().map_err(Error::Output)?;
    published
        .reservation
        .verify_after_main(&published.output)
        .map_err(Error::Reservation)
}

fn unlink_reservation(owner: &mut RetiringMain) -> Result<(), Error> {
    let published = &owner.published;
    let destination = published.output.attempt.destination();
    if !destination
        .directory()
        .unlink_exact(destination.coordination(), published.reservation.identity)?
    {
        return Err(NamespaceError::Missing.into());
    }
    owner.reservation_unlinked = true;
    let links = published
        .reservation
        .file
        .metadata()
        .map_err(error::Error::from)?
        .nlink();
    if links != 0 {
        return Err(Error::ReservationLinkCount(links));
    }
    crate::fault::crash("publication.after_reservation_unlink");
    Ok(())
}

fn sync_retirement(owner: &mut RetiringMain) -> Result<(), Error> {
    owner
        .published
        .output
        .attempt
        .destination()
        .directory()
        .sync()?;
    owner.directory_synced = true;
    crate::fault::crash("publication.after_retirement_sync");
    Ok(())
}

fn verify_retired(owner: &mut RetiringMain) -> Result<(), Error> {
    let output = &owner.published.output;
    let destination = output.attempt.destination();
    destination.directory().verify()?;
    destination
        .directory()
        .require_absent(destination.coordination())?;
    owner.reservation_retired_proven = true;
    output.verify_main().map_err(Error::Output)
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
#[path = "main_file_tests.rs"]
mod tests;
