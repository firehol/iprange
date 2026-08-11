//! Canonical immutable output from one membership recovery analysis.

use crate::cancellation::CancellationToken;
use crate::contract::{MetaV4, ValueKind};
use crate::immutable_output::Builder;
use crate::mapping::Mapping;

use super::construction::{Construction, Failure};
use super::indirect_build::{self, Mode, OutputContext};
use super::membership_output::{MembershipKey, MembershipOutput};
use super::range_build::{build_ranges, BuildResult};
use super::range_components::Components;
use super::report::RecoverySink;
use super::structure_table::StructureIndex;
use super::structured_output::StructuredKey;
use super::RecoveryBudget;

struct Membership;

impl Mode for Membership {
    const VALUE_KIND: ValueKind = ValueKind::Membership;

    fn check_structures(structures: Option<&StructureIndex>) -> crate::error::Result<()> {
        if structures.is_some() {
            return Err(crate::error::Error::Corrupt(
                "membership recovery unexpectedly has a structure index",
            ));
        }
        Ok(())
    }

    fn output<K, S>(context: OutputContext<'_, '_, '_, S>) -> BuildResult
    where
        K: MembershipKey + StructuredKey,
        S: RecoverySink,
    {
        let policy = MembershipOutput::<S, K>::new(
            context.request.mapping,
            context.request.meta,
            context.memberships,
            context.tables,
            context.builder,
            context.reporter,
        );
        let mut output = Components::new(context.request.cancellation, policy);
        build_ranges(context.request, context.pages, &mut output)
    }
}

#[allow(clippy::result_large_err)]
pub(crate) fn construct<S: RecoverySink>(
    mapping: &Mapping,
    source_meta: MetaV4,
    builder: Builder,
    budget: &RecoveryBudget,
    cancellation: &CancellationToken,
    sink: &mut S,
) -> std::result::Result<Construction, Failure> {
    indirect_build::construct::<Membership, S>(
        mapping,
        source_meta,
        builder,
        budget,
        cancellation,
        sink,
    )
}

#[cfg(test)]
#[path = "membership_tests.rs"]
mod tests;
