//! Reconstruction of a locked prepared output from exact persisted evidence.

use std::fs::File;

use crate::contract::MetaV4;

use crate::publication::file_inspection::Inspected;
use crate::publication::namespace::{Destination, Identity};
use crate::publication::replacement::PreviousMain;
use crate::publication::reservation::Policy;

use super::{Error, OutputAttempt, PreparedOutput};

#[derive(Debug)]
pub(in crate::publication) struct ResumedOutput {
    pub(in crate::publication) file: File,
    pub(in crate::publication) identity: Identity,
    pub(in crate::publication) meta: MetaV4,
    pub(in crate::publication) byte_length: u64,
    pub(in crate::publication) sha512: [u8; 64],
}

impl PreparedOutput {
    pub(in crate::publication) fn resume(
        destination: Destination,
        attempt_id: [u8; 16],
        inspected: Inspected,
    ) -> Result<Self, Error> {
        Self::resume_with(
            destination,
            attempt_id,
            ResumedOutput {
                file: inspected.file,
                identity: inspected.identity,
                meta: inspected.meta,
                byte_length: inspected.byte_length,
                sha512: inspected.sha512,
            },
            Policy::FailIfExists,
            None,
        )
    }

    pub(in crate::publication) fn resume_replacement(
        destination: Destination,
        attempt_id: [u8; 16],
        output: ResumedOutput,
        previous: PreviousMain,
        policy: Policy,
    ) -> Result<Self, Error> {
        debug_assert!(policy.is_replacement());
        Self::resume_with(destination, attempt_id, output, policy, Some(previous))
    }

    fn resume_with(
        destination: Destination,
        attempt_id: [u8; 16],
        output: ResumedOutput,
        policy: Policy,
        previous: Option<PreviousMain>,
    ) -> Result<Self, Error> {
        let name = destination
            .output_name(attempt_id)
            .map_err(Error::Namespace)?;
        Ok(Self {
            attempt: OutputAttempt {
                destination,
                attempt_id,
                name,
                identity: output.identity,
            },
            file: output.file,
            meta: output.meta,
            byte_length: output.byte_length,
            sha512: output.sha512,
            policy,
            previous,
        })
    }
}
