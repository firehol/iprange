//! Publication cleanup through authenticated Windows GC transitions.

use std::fs::File;

use super::{
    early_artifact, EarlyDiscard, OutputOwner, Point, ReservationLocation, ReservationOwner,
    Summary,
};
use crate::publication::gc::{self, Authority};
use crate::publication::gc_codec::Payload;
use crate::publication::namespace::{regular_identity, Identity, Name};
use crate::publication::output::{CreatedOutput, OutputAttempt};
use crate::publication::problem::Problem;
use crate::publication::result::{ArtifactKind, CleanupArtifacts, NameSlot, Seed};
use crate::publication::{
    CreationSecurity, DirectoryRole, Housekeeping, HousekeepingArtifact, PrivateOutputAttempt,
};

pub(super) fn discard_created(
    created: &CreatedOutput,
    facts: PrivateOutputAttempt,
) -> EarlyDiscard {
    let Some(identity) = facts
        .identity
        .and_then(|identity| Identity::decode(identity.bytes))
    else {
        let problem = Problem::cleanup_conflict("private output identity was not established");
        return EarlyDiscard {
            artifact: Some(early_artifact(&facts, problem)),
            output: facts,
            housekeeping: Housekeeping::None,
            visible_housekeeping: Box::default(),
        };
    };
    let retirement = retire_output(
        created.destination().directory(),
        created.attempt_id(),
        created.name(),
        created.file(),
        identity,
        facts.creation_security.clone(),
        None,
    );
    early(facts, retirement)
}

pub(super) fn discard_attempt(
    attempt: &OutputAttempt,
    file: &File,
    facts: PrivateOutputAttempt,
) -> EarlyDiscard {
    let retirement = retire_output(
        attempt.destination().directory(),
        attempt.attempt_id(),
        attempt.name(),
        file,
        attempt.identity(),
        facts.creation_security.clone(),
        None,
    );
    early(facts, retirement)
}

pub(super) fn discard_owners(
    seed: &mut Seed,
    destination: &crate::publication::namespace::Destination,
    output: Option<OutputOwner<'_>>,
    reservation: Option<ReservationOwner<'_>>,
    checkpoint: &mut impl FnMut(Point) -> Result<(), Problem>,
) -> Summary {
    let mut artifacts = CleanupArtifacts::new();
    let mut housekeeping = Housekeeping::None;
    let mut visible = Vec::new();

    if let Some(owner) = output {
        let retirement = match checkpoint(Point::OutputRemoval) {
            Ok(()) => retire_output(
                destination.directory(),
                seed.attempt_id(),
                owner.name,
                owner.file,
                owner.identity,
                seed.creation_security().clone(),
                seed.output_payload(),
            ),
            Err(problem) => gc::Retirement {
                problem: Some(problem),
                housekeeping: Housekeeping::None,
                visible: None,
            },
        };
        absorb(
            seed,
            retirement,
            ArtifactKind::PrivateOutput,
            NameSlot::PrivateOutput,
            Some(owner.identity),
            &mut artifacts,
            &mut housekeeping,
            &mut visible,
        );
    }

    if let Some(owner) = reservation {
        let identity = owner
            .identity
            .or_else(|| regular_identity(owner.file, destination.directory().identity()).ok());
        let source = identity.and_then(|identity| reservation_source(destination, owner, identity));
        let retirement = match (checkpoint(Point::ReservationRemoval), source) {
            (Ok(()), Some((name, kind, _, identity))) => gc::retire(
                destination.directory(),
                Authority {
                    attempt_id: seed.attempt_id(),
                    ordinal: 1,
                    kind,
                    directory_role: DirectoryRole::Destination,
                    source_name: name,
                    source_file: owner.file,
                    identity,
                    creation_security: seed.creation_security().clone(),
                    payload: None,
                },
            ),
            (Err(problem), _) => gc::Retirement {
                problem: Some(problem),
                housekeeping: Housekeeping::None,
                visible: None,
            },
            (Ok(()), None) => gc::Retirement {
                problem: Some(Problem::cleanup_conflict(
                    "reservation cleanup has no exact retained source name",
                )),
                housekeeping: Housekeeping::None,
                visible: None,
            },
        };
        let (kind, slot) = source.map(|(_, kind, slot, _)| (kind, slot)).unwrap_or((
            ArtifactKind::PrivateReservation,
            default_slot(owner.location),
        ));
        absorb(
            seed,
            retirement,
            kind,
            slot,
            identity,
            &mut artifacts,
            &mut housekeeping,
            &mut visible,
        );
    }

    Summary {
        artifacts,
        housekeeping,
        visible_housekeeping: visible,
        main_absent: destination
            .directory()
            .require_absent(destination.main())
            .is_ok(),
        coordination_absent: destination
            .directory()
            .require_absent(destination.coordination())
            .is_ok(),
    }
}

fn retire_output(
    directory: &crate::publication::namespace::Directory,
    attempt_id: [u8; 16],
    name: &Name,
    file: &File,
    identity: Identity,
    creation_security: CreationSecurity,
    payload: Option<Payload>,
) -> gc::Retirement {
    gc::retire(
        directory,
        Authority {
            attempt_id,
            ordinal: 0,
            kind: ArtifactKind::PrivateOutput,
            directory_role: DirectoryRole::Destination,
            source_name: name,
            source_file: file,
            identity,
            creation_security,
            payload,
        },
    )
}

fn reservation_source<'a>(
    destination: &'a crate::publication::namespace::Destination,
    owner: ReservationOwner<'a>,
    identity: Identity,
) -> Option<(&'a Name, ArtifactKind, NameSlot, Identity)> {
    let private = || {
        destination
            .directory()
            .verify_name(owner.private_name, identity)
            .ok()
            .map(|()| {
                (
                    owner.private_name,
                    ArtifactKind::PrivateReservation,
                    NameSlot::PrivateReservation,
                    identity,
                )
            })
    };
    let canonical = || {
        destination
            .directory()
            .verify_name(destination.coordination(), identity)
            .ok()
            .map(|()| {
                (
                    destination.coordination(),
                    ArtifactKind::OwnedCoordination,
                    NameSlot::Coordination,
                    identity,
                )
            })
    };
    match owner.location {
        ReservationLocation::Private => private(),
        ReservationLocation::Canonical => canonical(),
        ReservationLocation::Either => private().or_else(canonical),
    }
}

fn early(facts: PrivateOutputAttempt, retirement: gc::Retirement) -> EarlyDiscard {
    let artifact = retirement
        .problem
        .map(|problem| early_artifact(&facts, problem));
    let visible_housekeeping = retirement.visible.into_iter().collect::<Vec<_>>();
    EarlyDiscard {
        output: facts,
        artifact,
        housekeeping: retirement.housekeeping,
        visible_housekeeping: visible_housekeeping.into_boxed_slice(),
    }
}

#[allow(clippy::too_many_arguments)]
fn absorb(
    seed: &mut Seed,
    retirement: gc::Retirement,
    kind: ArtifactKind,
    slot: NameSlot,
    identity: Option<Identity>,
    artifacts: &mut CleanupArtifacts,
    housekeeping: &mut Housekeeping,
    visible: &mut Vec<HousekeepingArtifact>,
) {
    if let Some(problem) = retirement.problem {
        artifacts.push(seed.artifact(kind, slot, identity, problem));
    }
    *housekeeping = housekeeping.merge(retirement.housekeeping);
    visible.extend(retirement.visible);
}

const fn default_slot(location: ReservationLocation) -> NameSlot {
    match location {
        ReservationLocation::Private | ReservationLocation::Either => NameSlot::PrivateReservation,
        ReservationLocation::Canonical => NameSlot::Coordination,
    }
}
