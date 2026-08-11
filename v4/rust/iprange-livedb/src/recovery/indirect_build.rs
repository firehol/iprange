//! Shared immutable construction for membership-backed recovery modes.

use crate::cancellation::CancellationToken;
use crate::contract::{AddressFamily, MetaV4, ValueKind};
use crate::error::{Error, Result};
use crate::immutable_output::Builder;
use crate::key::{Ipv4Key, Ipv6Key};
use crate::mapping::Mapping;

use super::construction::{self, Construction, Failure};
use super::membership::{analyze, IndirectAnalysis};
use super::membership_index::MembershipIndex;
use super::membership_output::MembershipKey;
use super::page_set::PageSet;
use super::range_build::{
    failed_pages, retained_metadata_bytes, BuildResult, RangeBuild, SortReuse,
};
use super::report::{RecoveryReport, RecoverySink, Reporter};
use super::structure_table::StructureIndex;
use super::structured_output::StructuredKey;
use super::tables::Tables;
use super::RecoveryBudget;

pub(super) struct OutputContext<'a, 'b, 'c, S> {
    pub(super) request: RangeBuild<'a>,
    pub(super) pages: PageSet,
    pub(super) memberships: &'a MembershipIndex,
    pub(super) structures: Option<&'a StructureIndex>,
    pub(super) tables: &'a Tables,
    pub(super) builder: &'b mut Builder,
    pub(super) reporter: &'b mut Reporter<'c, S>,
}

pub(super) trait Mode {
    const VALUE_KIND: ValueKind;

    fn check_structures(structures: Option<&StructureIndex>) -> Result<()>;

    #[allow(clippy::result_large_err)]
    fn output<K, S>(context: OutputContext<'_, '_, '_, S>) -> BuildResult
    where
        K: MembershipKey + StructuredKey,
        S: RecoverySink;
}

#[allow(clippy::result_large_err)]
pub(super) fn construct<M: Mode, S: RecoverySink>(
    mapping: &Mapping,
    source_meta: MetaV4,
    builder: Builder,
    budget: &RecoveryBudget,
    cancellation: &CancellationToken,
    sink: &mut S,
) -> std::result::Result<Construction, Failure> {
    let (builder, analysis) = construction::prepare(builder, source_meta, M::VALUE_KIND, || {
        analyze(
            mapping,
            source_meta,
            budget,
            cancellation,
            sink,
            M::VALUE_KIND,
        )
    })?;
    match source_meta.address_family {
        AddressFamily::Ipv4 => build::<M, Ipv4Key, S>(
            mapping,
            source_meta,
            builder,
            budget,
            cancellation,
            sink,
            analysis,
        ),
        AddressFamily::Ipv6 => build::<M, Ipv6Key, S>(
            mapping,
            source_meta,
            builder,
            budget,
            cancellation,
            sink,
            analysis,
        ),
    }
}

#[allow(clippy::too_many_arguments, clippy::result_large_err)]
fn build<M, K, S>(
    mapping: &Mapping,
    source_meta: MetaV4,
    mut builder: Builder,
    budget: &RecoveryBudget,
    cancellation: &CancellationToken,
    sink: &mut S,
    analysis: IndirectAnalysis,
) -> std::result::Result<Construction, Failure>
where
    M: Mode,
    K: MembershipKey + StructuredKey,
    S: RecoverySink,
{
    let IndirectAnalysis {
        report,
        readable_records,
        ordered,
        catalog,
        memberships,
        structures,
        tables,
        metadata,
        pages,
    } = analysis;
    if let Err(cause) = M::check_structures(structures.as_ref()) {
        return Err(failure(builder, pages, cause, report));
    }
    if let Err(cause) =
        catalog.for_each(&tables, |entry| builder.push_feed(entry.name, entry.index))
    {
        return Err(failure(builder, pages, cause, report));
    }
    let retained = match retained_bytes(&tables, &metadata) {
        Ok(retained) => retained,
        Err(cause) => return Err(failure(builder, pages, cause, report)),
    };
    #[cfg(any(unix, windows))]
    let sort_reuse = SortReuse::area(tables.scratch_region());
    #[cfg(not(any(unix, windows)))]
    let sort_reuse = SortReuse::none();
    construction::complete_ranges(
        builder,
        metadata.as_deref(),
        budget.max_heap_bytes,
        retained_metadata_bytes(&metadata),
        report,
        sink,
        move |builder, reporter| {
            let result = M::output::<K, S>(OutputContext {
                request: RangeBuild {
                    mapping,
                    meta: source_meta,
                    budget,
                    cancellation,
                    readable_records,
                    ordered,
                    retained_heap_bytes: retained,
                    sort_reuse,
                },
                pages,
                memberships: &memberships,
                structures: structures.as_ref(),
                tables: &tables,
                builder,
                reporter,
            });
            drop(tables);
            result
        },
    )
}

fn retained_bytes(tables: &Tables, metadata: &Option<Vec<u8>>) -> Result<u64> {
    tables
        .retained_bytes()
        .checked_add(retained_metadata_bytes(metadata))
        .ok_or(Error::ArithmeticOverflow("recovery retained heap"))
}

fn failure(builder: Builder, pages: PageSet, cause: Error, report: RecoveryReport) -> Failure {
    let failed = failed_pages(pages, cause);
    construction::failure(builder, failed.cause, report, failed.scratch)
}
