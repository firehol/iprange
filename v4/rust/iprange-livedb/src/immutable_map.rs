//! Immutable valued construction through the shared mapped output owner.
use crate::{
    CancellationToken, DirectRange, Ipv4Key, Ipv6Key, PublicationPolicy, RangeSource, ValueTag,
};
use std::path::Path;

/// Resource limits shared by direct and named membership construction.
pub type ImmutableBuildBudget = crate::ImmutableFeedBudget;
/// Preparation failure or a result with explicit publication status.
pub type ImmutableBuildOutcome = crate::ImmutableFeedOutcome;
/// Exact input count, normalized interval count, and address cardinality.
pub type ImmutableBuildReport = crate::ImmutableFeedReport;
/// Construction report and publication/cleanup outcome.
pub type ImmutableBuildResult = crate::ImmutableFeedResult;

/// Select validation-only ordered construction or bounded mapped normalization.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ImmutableInputOrder {
    /// Ascending, nonoverlapping intervals; equal-valued adjacency is coalesced.
    OrderedDisjoint,
    /// Direct maps use last-assignment precedence; membership maps union labels.
    Unordered,
}

macro_rules! direct_api {
    ($name:ident, $key:ty) => {
        /// Construct an immutable direct map without a live database or snapshot pass.
        /// Values, including zero, are opaque. Metadata is published with the map.
        #[allow(clippy::too_many_arguments)]
        pub fn $name<S: RangeSource<DirectRange<$key>>>(
            destination: impl AsRef<Path>,
            value_tag: ValueTag,
            metadata_json: Option<&[u8]>,
            publication_policy: PublicationPolicy,
            source: &mut S,
            order: ImmutableInputOrder,
            budget: &ImmutableBuildBudget,
            cancellation: &CancellationToken,
        ) -> ImmutableBuildOutcome {
            crate::immutable_output::valued::direct::<$key, S>(
                destination.as_ref(),
                value_tag,
                metadata_json,
                publication_policy,
                source,
                order,
                budget,
                cancellation,
            )
        }
    };
}
direct_api!(create_immutable_direct_v4, Ipv4Key);
direct_api!(create_immutable_direct_v6, Ipv6Key);

/// One interval with borrowed logical labels. Repeated labels are idempotent.
#[derive(Clone, Copy, Debug)]
pub struct ImmutableMembershipRange<'a, K> {
    pub from: K,
    pub to: K,
    pub feeds: &'a [crate::FeedName],
}

/// Finite source that may reuse its label buffer after each callback returns.
pub trait ImmutableMembershipSource<K> {
    /// Return a nonempty label set, end of input, or the exact source error.
    fn next_range(&mut self) -> crate::Result<Option<ImmutableMembershipRange<'_, K>>>;
}

macro_rules! membership_api {
    ($name:ident, $key:ty) => {
        /// Construct a named membership map. Unordered overlaps union their labels.
        /// The catalog must contain unique names; unused names remain active.
        #[allow(clippy::too_many_arguments)]
        pub fn $name<S: ImmutableMembershipSource<$key>>(
            destination: impl AsRef<Path>,
            value_tag: ValueTag,
            metadata_json: Option<&[u8]>,
            publication_policy: PublicationPolicy,
            feeds: &[crate::FeedName],
            source: &mut S,
            order: ImmutableInputOrder,
            budget: &ImmutableBuildBudget,
            cancellation: &CancellationToken,
        ) -> ImmutableBuildOutcome {
            crate::immutable_output::named::create::<$key, S>(
                destination.as_ref(),
                value_tag,
                metadata_json,
                publication_policy,
                feeds,
                source,
                order,
                budget,
                cancellation,
            )
        }
    };
}
membership_api!(create_immutable_membership_v4, Ipv4Key);
membership_api!(create_immutable_membership_v6, Ipv6Key);
