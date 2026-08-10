//! Public orchestration for unordered immutable single-feed publication.

use std::fs::File;
use std::path::Path;

use crate::cancellation::CancellationToken;
use crate::cardinality::Cardinality129;
use crate::contract::ValueTag;
use crate::error::Error;
use crate::feed::FeedName;
use crate::immutable_output::unordered;
use crate::key::{Ipv4Key, Ipv6Key};
use crate::publication::{CleanupState, PublicationPolicy, PublicationResult};
use crate::source::RangeSource;
use crate::workflow::AddressRange;

pub type ImmutableFeedPreparationFailure = crate::snapshot::SnapshotPreparationFailure;
pub type ImmutableFeedOutcome =
    std::result::Result<ImmutableFeedResult, Box<ImmutableFeedPreparationFailure>>;

/// Explicit resource limits for one private single-feed construction.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct ImmutableFeedBudget {
    pub max_heap_bytes: u64,
    pub max_output_pages: u64,
    pub max_workspace_pages: u64,
    pub max_open_files: u32,
}

impl ImmutableFeedBudget {
    pub const fn new(
        max_heap_bytes: u64,
        max_output_pages: u64,
        max_workspace_pages: u64,
        max_open_files: u32,
    ) -> Self {
        Self {
            max_heap_bytes,
            max_output_pages,
            max_workspace_pages,
            max_open_files,
        }
    }
}

/// Exact semantic work completed before immutable publication.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct ImmutableFeedReport {
    pub input_record_count: u64,
    pub normalized_interval_count: u64,
    pub addresses: Cardinality129,
}

/// Published single-feed result and its exact report.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ImmutableFeedResult {
    pub report: ImmutableFeedReport,
    pub publication: PublicationResult,
}

impl ImmutableFeedResult {
    pub const fn cleanup_state(&self) -> CleanupState {
        self.publication.cleanup_state()
    }
}

/// Normalize one unordered IPv4 feed directly into its immutable destination.
#[allow(clippy::too_many_arguments)]
pub fn create_immutable_feed_v4<S>(
    destination: impl AsRef<Path>,
    value_tag: ValueTag,
    feed_name: FeedName,
    metadata_json: Option<&[u8]>,
    publication_policy: PublicationPolicy,
    source: &mut S,
    budget: &ImmutableFeedBudget,
    cancellation: &CancellationToken,
) -> ImmutableFeedOutcome
where
    S: RangeSource<AddressRange<Ipv4Key>>,
{
    create::<Ipv4Key, S>(
        destination.as_ref(),
        value_tag,
        feed_name,
        metadata_json,
        publication_policy,
        source,
        budget,
        cancellation,
    )
}

/// Normalize one unordered IPv6 feed directly into its immutable destination.
#[allow(clippy::too_many_arguments)]
pub fn create_immutable_feed_v6<S>(
    destination: impl AsRef<Path>,
    value_tag: ValueTag,
    feed_name: FeedName,
    metadata_json: Option<&[u8]>,
    publication_policy: PublicationPolicy,
    source: &mut S,
    budget: &ImmutableFeedBudget,
    cancellation: &CancellationToken,
) -> ImmutableFeedOutcome
where
    S: RangeSource<AddressRange<Ipv6Key>>,
{
    create::<Ipv6Key, S>(
        destination.as_ref(),
        value_tag,
        feed_name,
        metadata_json,
        publication_policy,
        source,
        budget,
        cancellation,
    )
}

#[allow(clippy::too_many_arguments)]
fn create<K, S>(
    destination: &Path,
    value_tag: ValueTag,
    feed_name: FeedName,
    metadata_json: Option<&[u8]>,
    publication_policy: PublicationPolicy,
    source: &mut S,
    budget: &ImmutableFeedBudget,
    cancellation: &CancellationToken,
) -> ImmutableFeedOutcome
where
    K: unordered::FeedKey,
    S: RangeSource<AddressRange<K>>,
{
    let prepared_budget = unordered::prepare_budget(unordered::BudgetInput {
        max_heap_bytes: budget.max_heap_bytes,
        max_output_pages: budget.max_output_pages,
        max_workspace_pages: budget.max_workspace_pages,
        max_open_files: budget.max_open_files,
    })
    .map_err(|cause| Box::new(ImmutableFeedPreparationFailure::early(cause)))?;
    cancellation
        .check()
        .map_err(|cause| Box::new(ImmutableFeedPreparationFailure::early(cause)))?;
    let (attempt, file) = crate::publication::workflow::create(destination, publication_policy)
        .map_err(failure_from_early)?;
    let built = match unordered::build::<K, S>(
        file,
        value_tag,
        feed_name,
        metadata_json,
        source,
        prepared_budget,
        cancellation,
    ) {
        Ok(built) => built,
        Err(failure) => return Err(discard_attempt(attempt, failure.file, failure.cause)),
    };
    let report = ImmutableFeedReport {
        input_record_count: built.input_record_count,
        normalized_interval_count: built.normalized_interval_count,
        addresses: built.addresses,
    };
    match crate::publication::workflow::publish(
        attempt,
        built.finished,
        publication_policy,
        cancellation,
    ) {
        Ok(publication) => Ok(ImmutableFeedResult {
            report,
            publication,
        }),
        Err(crate::publication::workflow::Failure::Early(failure)) => {
            Err(failure_from_early(failure))
        }
        Err(crate::publication::workflow::Failure::Publication(failure)) => Err(Box::new(
            ImmutableFeedPreparationFailure::from_publication(*failure),
        )),
    }
}

fn discard_attempt(
    attempt: crate::publication::output::OutputAttempt,
    file: File,
    cause: Error,
) -> Box<ImmutableFeedPreparationFailure> {
    let discarded = crate::publication::cleanup::discard_attempt(&attempt, &file);
    Box::new(ImmutableFeedPreparationFailure::discarded(
        crate::publication::problem::Problem::sdk(&cause),
        discarded,
        None,
    ))
}

fn failure_from_early(
    failure: crate::publication::workflow::EarlyFailure,
) -> Box<ImmutableFeedPreparationFailure> {
    match failure.discarded {
        Some(discarded) => Box::new(ImmutableFeedPreparationFailure::discarded(
            failure.cause,
            discarded,
            None,
        )),
        None => Box::new(ImmutableFeedPreparationFailure::new(
            failure.cause,
            None,
            None,
            None,
        )),
    }
}
