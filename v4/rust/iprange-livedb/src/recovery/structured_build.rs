//! Canonical immutable output from one structured recovery analysis.

use crate::cancellation::CancellationToken;
use crate::contract::{MetaV4, StructureKind, ValueKind};
use crate::error::{Error, Result};
use crate::immutable_output::Builder;
use crate::mapping::Mapping;

use super::construction::{Construction, Failure};
use super::indirect_build::{self, Mode, OutputContext};
use super::membership_output::MembershipKey;
use super::range_build::{build_ranges, BuildResult};
use super::range_components::Components;
use super::report::RecoverySink;
use super::structure_table::StructureIndex;
use super::structured_output::{NetworkEnrichmentV1Output, StructuredKey};
use super::RecoveryBudget;

struct Structured;

impl Mode for Structured {
    const VALUE_KIND: ValueKind = ValueKind::Structured;

    fn check_structures(structures: Option<&StructureIndex>) -> Result<()> {
        let structures =
            structures.ok_or(Error::Corrupt("structured recovery has no structure index"))?;
        if structures.kind() != StructureKind::NetworkEnrichmentV1 {
            return Err(Error::UnsupportedStructure(structures.kind() as u8));
        }
        Ok(())
    }

    fn output<K, S>(context: OutputContext<'_, '_, '_, S>) -> BuildResult
    where
        K: MembershipKey + StructuredKey,
        S: RecoverySink,
    {
        let structures = context
            .structures
            .expect("structured recovery index was checked");
        let policy = NetworkEnrichmentV1Output::<S, K>::new(
            context.request.mapping,
            context.request.meta,
            context.memberships,
            structures,
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
    indirect_build::construct::<Structured, S>(
        mapping,
        source_meta,
        builder,
        budget,
        cancellation,
        sink,
    )
}

#[cfg(test)]
#[path = "structured_tests.rs"]
mod tests;
