//! Retained, read-only evidence for the destination main.

use std::fs::File;

use super::{PublicationResidueMain, PublicationResidueMainContent};
use crate::bootstrap::{self, OpenMode};
use crate::cancellation::CancellationToken;
use crate::contract::PAGE_SIZE;
use crate::error::Error;
use crate::live_lock::{self, Mode};
use crate::live_sidecar::MAIN_LIFETIME_LOCK;
use crate::mapping::Mapping;
use crate::publication::namespace::{local_identity as local, Destination, Identity};
use crate::publication::output;
use crate::publication::problem::Problem;
use crate::publication::{
    AccessPolicy, ArtifactKind, DirectoryRole, PublicationDigest, PublicationTuple,
};

#[derive(Debug)]
pub(super) struct Guard {
    file: File,
    mapping: Mapping,
    identity: Identity,
    byte_length: u64,
    pub(super) evidence: PublicationResidueMain,
}

pub(super) fn inspect(
    destination: &Destination,
    cancellation: &CancellationToken,
) -> Result<Option<Guard>, Problem> {
    let Some(regular) = destination
        .directory()
        .open_regular(destination.main(), true)
        .map_err(|error| Problem::namespace(&error))?
    else {
        return Ok(None);
    };
    live_lock::lock_file_cancellable(
        &regular.file,
        MAIN_LIFETIME_LOCK,
        Mode::Exclusive,
        cancellation,
    )
    .map_err(|error| Problem::sdk(&error))?;
    destination
        .directory()
        .verify_name(destination.main(), regular.identity)
        .map_err(|error| Problem::namespace(&error))?;
    let byte_length = regular
        .file
        .metadata()
        .map_err(Error::from)
        .map_err(|error| Problem::sdk(&error))?
        .len();
    let mapping = Mapping::read_only_view(&regular.file, byte_length)
        .map_err(|error| Problem::sdk(&error))?;
    let tuple = read_tuple(&mapping, byte_length)?;
    if let Some(tuple) = tuple {
        crate::publication::gc_barrier::require_source_available(
            destination.directory(),
            tuple.database_id,
            0,
            ArtifactKind::OwnedMain,
            DirectoryRole::MainFile,
            destination.main(),
            regular.identity,
        )?;
    }
    let sha512 = output::digest_cancellable(&mapping, byte_length, cancellation)
        .map_err(|error| Problem::output(&error))?;
    let access_policy = match crate::publication::security::creator_only_commitment(&regular.file) {
        Ok(_) => AccessPolicy::CreatorOnly,
        Err(_) => AccessPolicy::ChangedOrUnproven,
    };
    Ok(Some(Guard {
        file: regular.file,
        mapping,
        identity: regular.identity,
        byte_length,
        evidence: PublicationResidueMain {
            identity: local(regular.identity),
            content: if tuple.is_some() {
                PublicationResidueMainContent::V4
            } else {
                PublicationResidueMainContent::Other
            },
            tuple,
            digest: PublicationDigest {
                byte_length,
                sha512,
            },
            access_policy,
        },
    }))
}

impl Guard {
    pub(super) fn verify(&self, destination: &Destination) -> Result<(), Problem> {
        destination
            .directory()
            .verify_name(destination.main(), self.identity)
            .map_err(|error| Problem::namespace(&error))?;
        if self.mapping.len() != self.byte_length
            || self
                .file
                .metadata()
                .map_err(Error::from)
                .map_err(|error| Problem::sdk(&error))?
                .len()
                != self.byte_length
        {
            return Err(Problem::cleanup_conflict(
                "destination main length changed during removal",
            ));
        }
        Ok(())
    }
}

fn read_tuple(mapping: &Mapping, byte_length: u64) -> Result<Option<PublicationTuple>, Problem> {
    if byte_length < (2 * PAGE_SIZE) as u64 || byte_length % PAGE_SIZE as u64 != 0 {
        return Ok(None);
    }
    let left = mapping.page(0, 2).map_err(|error| Problem::sdk(&error))?;
    let right = mapping.page(1, 2).map_err(|error| Problem::sdk(&error))?;
    let Ok(opened) =
        bootstrap::open_meta_pages(left, right, byte_length, OpenMode::ImmutableReader)
    else {
        return Ok(None);
    };
    Ok(Some(PublicationTuple {
        database_id: opened.meta.database_id,
        transaction_id: opened.meta.txn_id,
        commit_nonce: opened.meta.commit_nonce,
    }))
}
