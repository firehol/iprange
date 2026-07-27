//! Atomic main-name publication and exact reservation retirement.

use crate::error;

#[cfg(unix)]
use super::namespace::regular_link_count;
use super::namespace::{sync_file, NamespaceError};
use super::output::{self, PreparedOutput};
use super::problem::Problem;
use super::reservation::{Header, Policy};
use super::reservation_file::{self, ArmedReservation};
use super::{Housekeeping, HousekeepingArtifact};

#[derive(Debug)]
pub(crate) enum Error {
    Namespace(NamespaceError),
    Sdk(error::Error),
    Output(output::Error),
    Reservation(reservation_file::Error),
    PreviousLinkCount(u64),
    ReservationLinkCount(u64),
    Gc(Problem),
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
    pub(crate) previous_unlinked: bool,
    pub(crate) previous_retired_proven: bool,
    pub(crate) reservation_unlinked: bool,
    pub(crate) directory_synced: bool,
    pub(crate) reservation_retired_proven: bool,
    pub(crate) housekeeping: Housekeeping,
    pub(crate) visible_housekeeping: Vec<HousekeepingArtifact>,
}

#[derive(Debug)]
pub(crate) struct PublishedOutput {
    pub(crate) output: PreparedOutput,
    pub(crate) reservation_identity: super::namespace::Identity,
    pub(crate) reservation_header: Header,
    pub(crate) housekeeping: Housekeeping,
    pub(crate) visible_housekeeping: Vec<HousekeepingArtifact>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Point {
    MainRenamed,
    MainSynced,
    DirectorySynced,
    DesiredProven,
    PreviousUnlinked,
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
    if owner.output.previous.is_some() {
        owner
            .output
            .verify_destination_before_main()
            .map_err(Error::Output)
    } else {
        let destination = owner.output.attempt.destination();
        destination
            .directory()
            .require_absent(destination.main())
            .map_err(Error::Namespace)
    }
}

fn rename_main(owner: &mut MainAttempt) -> Result<(), Error> {
    let destination = owner.output.attempt.destination();
    owner.main_call_started = true;
    match owner.output.policy {
        Policy::FailIfExists => destination.directory().rename_noreplace(
            owner.output.attempt.name(),
            &owner.output.file,
            destination.main(),
        )?,
        Policy::ReplaceExisting => destination.directory().exchange(
            owner.output.attempt.name(),
            &owner.output.file,
            destination.main(),
        )?,
        Policy::ReplaceExistingNoRollback => {
            destination.directory().replace_discarding_destination(
                owner.output.attempt.name(),
                &owner.output.file,
                destination.main(),
            )?
        }
    }
    owner.rename_succeeded = true;
    crate::fault::crash("publication.after_main_rename");
    Ok(())
}

fn synchronize_main(
    owner: &MainAttempt,
    checkpoint: &mut impl FnMut(Point) -> Result<(), Error>,
) -> Result<(), Error> {
    sync_file(&owner.output.file).map_err(error::Error::from)?;
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
            previous_unlinked: false,
            previous_retired_proven: false,
            reservation_unlinked: false,
            directory_synced: false,
            reservation_retired_proven: false,
            housekeeping: Housekeeping::None,
            visible_housekeeping: Vec::new(),
        };
        match retire_steps(&mut owner, &mut checkpoint) {
            Ok(()) => {
                let RetiringMain {
                    published,
                    housekeeping,
                    visible_housekeeping,
                    ..
                } = owner;
                Ok(PublishedOutput {
                    reservation_identity: published.reservation.identity,
                    reservation_header: published.reservation.header,
                    output: published.output,
                    housekeeping,
                    visible_housekeeping,
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
    if unlink_previous(owner)? {
        checkpoint(Point::PreviousUnlinked)?;
    }
    unlink_reservation(owner)?;
    checkpoint(Point::ReservationUnlinked)?;
    sync_retirement(owner)?;
    checkpoint(Point::RetirementSynced)?;
    verify_retired(owner)
}

#[cfg(unix)]
fn unlink_previous(owner: &mut RetiringMain) -> Result<bool, Error> {
    let published = &owner.published;
    let Some(previous) = &published.output.previous else {
        owner.previous_retired_proven = true;
        return Ok(false);
    };
    let destination = published.output.attempt.destination();
    previous.verify_private_or_retired(destination, published.output.attempt.name())?;
    if regular_link_count(&previous.file)? == 0 {
        owner.previous_unlinked = true;
        return Ok(false);
    }
    if !destination
        .directory()
        .unlink_exact(published.output.attempt.name(), previous.identity)?
    {
        return Err(NamespaceError::Missing.into());
    }
    owner.previous_unlinked = true;
    let links = regular_link_count(&previous.file)?;
    if links != 0 {
        return Err(Error::PreviousLinkCount(links));
    }
    crate::fault::crash("publication.after_previous_unlink");
    Ok(true)
}

#[cfg(windows)]
fn unlink_previous(owner: &mut RetiringMain) -> Result<bool, Error> {
    use super::gc::{self, Authority};
    use super::gc_codec::Payload;
    use super::{ArtifactKind, CreationSecurity, DirectoryRole};

    let published = &owner.published;
    let Some(previous) = &published.output.previous else {
        owner.previous_retired_proven = true;
        return Ok(false);
    };
    let destination = published.output.attempt.destination();
    previous.verify_private_or_retired(destination, published.output.attempt.name())?;
    let retirement = gc::retire(
        destination.directory(),
        Authority {
            attempt_id: published.output.attempt.attempt_id(),
            ordinal: 0,
            kind: ArtifactKind::PrivateOutput,
            directory_role: DirectoryRole::Destination,
            source_name: published.output.attempt.name(),
            source_file: &previous.file,
            identity: previous.identity,
            creation_security: CreationSecurity {
                kind: super::namespace::CREATION_SECURITY_KIND,
                commitment: destination.security_commitment(),
            },
            payload: Some(Payload {
                byte_length: previous.byte_length,
                sha512: previous.sha512,
                database_id: [0; 16],
                transaction_id: 0,
                commit_nonce: [0; 16],
            }),
        },
    );
    absorb_gc(owner, retirement)?;
    owner.previous_unlinked = true;
    owner.previous_retired_proven = true;
    crate::fault::crash("publication.after_previous_unlink");
    Ok(true)
}

fn verify_published(published: &PublishedMain) -> Result<(), Error> {
    published.output.verify_main().map_err(Error::Output)?;
    published
        .reservation
        .verify_after_main(&published.output)
        .map_err(Error::Reservation)
}

#[cfg(unix)]
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
    let links = regular_link_count(&published.reservation.file)?;
    if links != 0 {
        return Err(Error::ReservationLinkCount(links));
    }
    crate::fault::crash("publication.after_reservation_unlink");
    Ok(())
}

#[cfg(windows)]
fn unlink_reservation(owner: &mut RetiringMain) -> Result<(), Error> {
    use super::gc::{self, Authority};
    use super::{ArtifactKind, CreationSecurity, DirectoryRole};

    let published = &owner.published;
    let destination = published.output.attempt.destination();
    let retirement = gc::retire(
        destination.directory(),
        Authority {
            attempt_id: published.output.attempt.attempt_id(),
            ordinal: 1,
            kind: ArtifactKind::OwnedCoordination,
            directory_role: DirectoryRole::Destination,
            source_name: destination.coordination(),
            source_file: &published.reservation.file,
            identity: published.reservation.identity,
            creation_security: CreationSecurity {
                kind: super::namespace::CREATION_SECURITY_KIND,
                commitment: destination.security_commitment(),
            },
            payload: None,
        },
    );
    absorb_gc(owner, retirement)?;
    owner.reservation_unlinked = true;
    owner.reservation_retired_proven = true;
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
    if !owner.previous_retired_proven {
        if let Some(previous) = &output.previous {
            previous.verify_retired(destination, output.attempt.name())?;
        }
    }
    owner.previous_retired_proven = true;
    owner.reservation_retired_proven = true;
    output.verify_main().map_err(Error::Output)
}

#[cfg(windows)]
fn absorb_gc(owner: &mut RetiringMain, retirement: super::gc::Retirement) -> Result<(), Error> {
    owner.housekeeping = merge_housekeeping(owner.housekeeping, retirement.housekeeping);
    owner.visible_housekeeping.extend(retirement.visible);
    match retirement.problem {
        Some(problem) => Err(Error::Gc(problem)),
        None => Ok(()),
    }
}

#[cfg(windows)]
fn merge_housekeeping(left: Housekeeping, right: Housekeeping) -> Housekeeping {
    if matches!(left, Housekeeping::Visible) || matches!(right, Housekeeping::Visible) {
        Housekeeping::Visible
    } else if matches!(left, Housekeeping::CrashReappearancePossible)
        || matches!(right, Housekeeping::CrashReappearancePossible)
    {
        Housekeeping::CrashReappearancePossible
    } else {
        Housekeeping::None
    }
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

#[cfg(all(test, unix))]
#[path = "main_file_tests.rs"]
mod tests;
