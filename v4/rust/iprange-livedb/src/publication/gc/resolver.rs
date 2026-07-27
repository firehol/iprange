//! Idempotent Windows GC move classification and completion.

use std::fs::File;

use crate::error::ErrorCode;
use crate::validation::LocalFileIdentity;

use super::super::namespace::{Directory, Identity, Name, IDENTITY_KIND};
use super::super::problem::Problem;
use super::super::security;
use super::super::{
    ArtifactPresence, CreationSecurity, Housekeeping, HousekeepingArtifact, HousekeepingState,
};
use super::{Authority, Envelope, Retirement};

pub(super) fn resolve(
    directory: &Directory,
    authority: &Authority<'_>,
    envelope: Envelope,
) -> Retirement {
    resolve_envelope(directory, envelope, Some(authority.source_file))
}

pub(in crate::publication) fn resolve_existing(
    directory: &Directory,
    envelope: Envelope,
) -> Retirement {
    resolve_envelope(directory, envelope, None)
}

fn resolve_envelope(
    directory: &Directory,
    envelope: Envelope,
    retained_source: Option<&File>,
) -> Retirement {
    let mut observed = observe_pair(directory, &envelope);
    let mut move_problem = None;
    if observed.state == HousekeepingState::MovePending {
        if let Err(problem) = move_payload(directory, &envelope, retained_source) {
            move_problem = Some(problem);
        }
        observed = observe_pair(directory, &envelope);
    }
    if observed.state != HousekeepingState::Inert {
        let problem = move_problem.unwrap_or_else(|| match observed.state {
            HousekeepingState::MovePending | HousekeepingState::MoveAmbiguous => Problem::new(
                ErrorCode::CleanupInProgress,
                None,
                "GC payload move remains unresolved",
            ),
            HousekeepingState::Conflict | HousekeepingState::Inert => {
                Problem::cleanup_conflict("GC payload names or identities conflict")
            }
        });
        return visible_retirement(directory, &envelope, observed, Some(problem));
    }
    finish_housekeeping(directory, envelope)
}

fn move_payload(
    directory: &Directory,
    envelope: &Envelope,
    retained_source: Option<&File>,
) -> Result<(), Problem> {
    if let Some(file) = retained_source {
        return directory
            .rename_noreplace(&envelope.source_name, file, &envelope.inert_name)
            .map_err(|error| Problem::namespace(&error));
    }
    let regular = directory
        .open_regular(&envelope.source_name, true)
        .map_err(|error| Problem::namespace(&error))?
        .ok_or_else(|| Problem::cleanup_conflict("GC source disappeared before its move"))?;
    let expected = envelope_identity(envelope)?;
    if regular.identity != expected
        || security::creator_only_commitment(&regular.file)
            .map_err(|error| Problem::namespace(&error))?
            != envelope.header.creation_security_commitment
    {
        return Err(Problem::cleanup_conflict(
            "GC source identity or access policy changed",
        ));
    }
    directory
        .rename_noreplace(&envelope.source_name, &regular.file, &envelope.inert_name)
        .map_err(|error| Problem::namespace(&error))
}

fn finish_housekeeping(directory: &Directory, envelope: Envelope) -> Retirement {
    let identity = match envelope_identity(&envelope) {
        Ok(identity) => identity,
        Err(problem) => {
            let observed = observe_pair(directory, &envelope);
            return visible_retirement(directory, &envelope, observed, Some(problem));
        }
    };
    let _ = directory.unlink_exact(&envelope.inert_name, identity);
    let after_payload = observe_pair(directory, &envelope);
    if after_payload.inert.presence == ArtifactPresence::Absent {
        let _ = directory.unlink_exact(&envelope.name, envelope.identity);
    }
    let observed = observe_pair(directory, &envelope);
    let envelope_absent = directory.require_absent(&envelope.name).is_ok();
    if observed.source.presence == ArtifactPresence::Absent
        && observed.inert.presence == ArtifactPresence::Absent
        && envelope_absent
    {
        return Retirement {
            problem: None,
            housekeeping: Housekeeping::CrashReappearancePossible,
            visible: None,
        };
    }
    visible_retirement(directory, &envelope, observed, None)
}

pub(super) struct Observation {
    presence: ArtifactPresence,
    identity: Option<Identity>,
    exact: bool,
}

pub(in crate::publication) struct PairObservation {
    source: Observation,
    inert: Observation,
    pub(in crate::publication) state: HousekeepingState,
}

pub(in crate::publication) fn observe_pair(
    directory: &Directory,
    envelope: &Envelope,
) -> PairObservation {
    let Some(expected) = Identity::decode(envelope.header.artifact_identity) else {
        return unclassified_pair();
    };
    let security = envelope.header.creation_security_commitment;
    let source = observe(directory, &envelope.source_name, expected, security);
    let inert = observe(directory, &envelope.inert_name, expected, security);
    classify(source, inert)
}

fn classify(source: Observation, inert: Observation) -> PairObservation {
    let state = if source.exact && inert.presence == ArtifactPresence::Absent {
        HousekeepingState::MovePending
    } else if source.presence == ArtifactPresence::Absent && inert.exact {
        HousekeepingState::Inert
    } else if source.presence == ArtifactPresence::Absent
        && inert.presence == ArtifactPresence::Absent
    {
        HousekeepingState::MoveAmbiguous
    } else {
        HousekeepingState::Conflict
    };
    PairObservation {
        source,
        inert,
        state,
    }
}

fn observe(
    directory: &Directory,
    name: &Name,
    expected: Identity,
    expected_security: [u8; 32],
) -> Observation {
    match directory.entry(name) {
        Ok(None) => Observation {
            presence: ArtifactPresence::Absent,
            identity: None,
            exact: false,
        },
        Ok(Some(entry)) => {
            let exact = entry.regular
                && entry.links == 1
                && entry.identity == expected
                && directory
                    .open_regular(name, false)
                    .ok()
                    .flatten()
                    .is_some_and(|regular| {
                        regular.identity == expected
                            && regular.creator_only_commitment().ok() == Some(expected_security)
                    });
            Observation {
                presence: ArtifactPresence::Present,
                identity: Some(entry.identity),
                exact,
            }
        }
        Err(_) => Observation {
            presence: ArtifactPresence::Unclassified,
            identity: None,
            exact: false,
        },
    }
}

fn unclassified_pair() -> PairObservation {
    PairObservation {
        source: Observation {
            presence: ArtifactPresence::Unclassified,
            identity: None,
            exact: false,
        },
        inert: Observation {
            presence: ArtifactPresence::Unclassified,
            identity: None,
            exact: false,
        },
        state: HousekeepingState::Conflict,
    }
}

fn visible_retirement(
    directory: &Directory,
    envelope: &Envelope,
    observed: PairObservation,
    problem: Option<Problem>,
) -> Retirement {
    Retirement {
        problem,
        housekeeping: Housekeeping::Visible,
        visible: Some(artifact(directory.identity(), envelope, observed)),
    }
}

pub(super) fn failed(
    directory: &Directory,
    authority: &Authority<'_>,
    envelope_name: Option<&Name>,
    inert_name: Option<&Name>,
    problem: Problem,
) -> Retirement {
    let Some(envelope_name) = envelope_name else {
        return Retirement {
            problem: Some(problem),
            housekeeping: Housekeeping::None,
            visible: None,
        };
    };
    let Some(envelope_identity) = directory
        .entry(envelope_name)
        .ok()
        .flatten()
        .map(|entry| entry.identity)
    else {
        return Retirement {
            problem: Some(problem),
            housekeeping: Housekeeping::None,
            visible: None,
        };
    };
    let inert_name = inert_name.unwrap_or(envelope_name);
    let source = observe(
        directory,
        authority.source_name,
        authority.identity,
        authority.creation_security.commitment,
    );
    let inert = observe(
        directory,
        inert_name,
        authority.identity,
        authority.creation_security.commitment,
    );
    let observed = classify(source, inert);
    Retirement {
        problem: Some(problem),
        housekeeping: Housekeeping::Visible,
        visible: Some(failed_artifact(
            directory.identity(),
            authority,
            envelope_name,
            envelope_identity,
            inert_name,
            observed,
        )),
    }
}

pub(in crate::publication) fn artifact(
    directory_identity: Identity,
    envelope: &Envelope,
    observed: PairObservation,
) -> HousekeepingArtifact {
    HousekeepingArtifact {
        state: observed.state,
        directory_role: envelope.header.directory_role,
        directory_identity: local(directory_identity),
        basename_encoding: super::super::namespace::BASENAME_ENCODING_KIND,
        attempt_id: envelope.header.attempt_id,
        ordinal: envelope.header.ordinal,
        envelope_basename: envelope.name.bytes().into(),
        envelope_identity: local(envelope.identity),
        source_basename: envelope.source_name.bytes().into(),
        inert_basename: envelope.inert_name.bytes().into(),
        source_presence: observed.source.presence,
        source_identity: observed.source.identity.map(local),
        inert_presence: observed.inert.presence,
        inert_identity: observed.inert.identity.map(local),
        kind: envelope.header.kind,
        creation_security: CreationSecurity {
            kind: envelope.header.creation_security_kind,
            commitment: envelope.header.creation_security_commitment,
        },
        selected_envelope_sequence: envelope.header.sequence,
    }
}

fn failed_artifact(
    directory_identity: Identity,
    authority: &Authority<'_>,
    envelope_name: &Name,
    envelope_identity: Identity,
    inert_name: &Name,
    observed: PairObservation,
) -> HousekeepingArtifact {
    HousekeepingArtifact {
        state: observed.state,
        directory_role: authority.directory_role,
        directory_identity: local(directory_identity),
        basename_encoding: super::super::namespace::BASENAME_ENCODING_KIND,
        attempt_id: authority.attempt_id,
        ordinal: authority.ordinal,
        envelope_basename: envelope_name.bytes().into(),
        envelope_identity: local(envelope_identity),
        source_basename: authority.source_name.bytes().into(),
        inert_basename: inert_name.bytes().into(),
        source_presence: observed.source.presence,
        source_identity: observed.source.identity.map(local),
        inert_presence: observed.inert.presence,
        inert_identity: observed.inert.identity.map(local),
        kind: authority.kind,
        creation_security: authority.creation_security.clone(),
        selected_envelope_sequence: 0,
    }
}

fn envelope_identity(envelope: &Envelope) -> Result<Identity, Problem> {
    Identity::decode(envelope.header.artifact_identity)
        .ok_or_else(|| Problem::cleanup_conflict("GC artifact identity is malformed"))
}

fn local(identity: Identity) -> LocalFileIdentity {
    LocalFileIdentity {
        kind: IDENTITY_KIND,
        bytes: identity.encode(),
    }
}
