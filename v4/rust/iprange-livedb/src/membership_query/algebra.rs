//! Reusable global-name algebra over pinned membership scopes.

#[path = "algebra/analysis.rs"]
pub(crate) mod analysis;
#[path = "algebra/output.rs"]
pub(crate) mod output;
#[path = "algebra/scan.rs"]
mod scan;
#[path = "algebra/selection.rs"]
mod selection;

use crate::cancellation::CancellationToken;
use crate::cardinality::Cardinality129;
use crate::contract::AddressFamily;
use crate::error::{Error, Result};
use crate::feed::FeedName;
use crate::heap::Heap;
use crate::publication::{CleanupState, PublicationResult};
use crate::reader_core::GenerationReader;

use super::scope::ScopeData;
use super::{MembershipScope, MembershipScopeState};

pub type AlgebraPreparationFailure = crate::snapshot::SnapshotPreparationFailure;
pub type AlgebraSetOutcome = std::result::Result<AlgebraSetResult, Box<AlgebraPreparationFailure>>;

/// Bounded reusable state for a set of pinned membership scopes.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct MembershipAlgebraBudget {
    pub max_heap_bytes: u64,
    pub max_sources: u32,
}

/// Output mapping and descriptor limits for one published algebra result.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct AlgebraOutputBudget {
    pub max_output_pages: u64,
    pub max_open_files: u32,
}

/// Global logical feeds selected from every input scope.
#[derive(Clone, Copy, Debug)]
pub enum FeedSelection<'a> {
    All,
    Named(&'a [FeedName]),
}

/// One set operation over virtual global feeds.
#[derive(Clone, Copy, Debug)]
pub enum AlgebraSetOperation<'a> {
    Union(FeedSelection<'a>),
    Intersection(FeedSelection<'a>),
    Exclusion {
        included: FeedSelection<'a>,
        excluded: FeedSelection<'a>,
    },
}

/// Catalog shape of one materialized result.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[allow(clippy::large_enum_variant)] // Flat feed names stay inline and allocation-free.
pub enum AlgebraOutputMode {
    PreserveFeeds,
    Flat(FeedName),
}

/// Exact union cardinality for one global selection.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct AlgebraCountReport {
    pub source_count: u64,
    pub source_range_count: u64,
    pub joined_segment_count: u64,
    pub addresses: Cardinality129,
}

/// Exact comparison of two global selections.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct AlgebraComparisonReport {
    pub source_count: u64,
    pub source_range_count: u64,
    pub joined_segment_count: u64,
    pub left_addresses: Cardinality129,
    pub right_addresses: Cardinality129,
    pub overlap_addresses: Cardinality129,
    pub left_only_addresses: Cardinality129,
    pub right_only_addresses: Cardinality129,
    pub union_addresses: Cardinality129,
    pub equal: bool,
}

/// Exact work and content facts for one published set result.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct AlgebraSetReport {
    pub source_count: u64,
    pub source_range_count: u64,
    pub joined_segment_count: u64,
    pub output_feed_count: u64,
    pub output_range_count: u64,
    pub output_addresses: Cardinality129,
}

/// Published v4 algebra output and its exact semantic report.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct AlgebraSetResult {
    pub report: AlgebraSetReport,
    pub publication: PublicationResult,
}

impl AlgebraSetResult {
    pub const fn cleanup_state(&self) -> CleanupState {
        self.publication.cleanup_state()
    }
}

struct Source<'a> {
    reader: GenerationReader<'a>,
    scope: &'a ScopeData,
}

pub(crate) struct InputState {
    expected_ranges: u64,
    local_to_global: Vec<u32>,
}

pub(crate) struct MembershipAlgebraState {
    family: AddressFamily,
    inputs: Vec<InputState>,
    names: Vec<FeedName>,
    max_heap_bytes: u64,
    heap_used: u64,
}

pub(crate) struct AlgebraInput<'a> {
    pub(crate) reader: GenerationReader<'a>,
    pub(crate) scope: &'a ScopeData,
    pub(crate) expected_ranges: u64,
    pub(crate) local_to_global: &'a [u32],
}

pub(crate) trait AlgebraAccess {
    fn state(&self) -> &MembershipAlgebraState;
    fn source(&self, index: usize) -> Result<AlgebraInput<'_>>;

    fn operation_heap_reserved(&self, reserved: u64) -> Result<Heap> {
        let state = self.state();
        let remaining = state
            .max_heap_bytes
            .checked_sub(state.heap_used)
            .and_then(|remaining| remaining.checked_sub(reserved))
            .ok_or(Error::BudgetExceeded("membership algebra heap"))?;
        Ok(Heap::new(remaining))
    }
}

/// Pinned, reusable virtual catalog over one or more membership databases.
pub struct MembershipAlgebra<'a> {
    state: MembershipAlgebraState,
    sources: Vec<Source<'a>>,
}

impl std::fmt::Debug for MembershipAlgebra<'_> {
    fn fmt(&self, output: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        output
            .debug_struct("MembershipAlgebra")
            .field("family", &self.state.family)
            .field("source_count", &self.sources.len())
            .field("feed_count", &self.state.names.len())
            .finish_non_exhaustive()
    }
}

impl<'a> MembershipAlgebra<'a> {
    /// Resolve same-named feeds across every supplied scope into one global identity.
    pub fn new(
        scopes: &[&'a MembershipScope<'a>],
        budget: MembershipAlgebraBudget,
        cancellation: &CancellationToken,
    ) -> Result<Self> {
        MembershipAlgebraState::require_source_count(scopes.len(), budget)?;
        let minimum_source_bytes = scopes
            .len()
            .checked_mul(std::mem::size_of::<Source<'a>>())
            .ok_or(Error::BudgetExceeded("membership algebra source heap"))?;
        let mut source_heap = Heap::new(budget.max_heap_bytes);
        source_heap.reserve_bytes(minimum_source_bytes, "membership algebra source heap")?;
        let mut sources = Vec::new();
        sources
            .try_reserve_exact(scopes.len())
            .map_err(|_| Error::BudgetExceeded("membership algebra source heap"))?;
        sources.extend(scopes.iter().map(|scope| Source {
            reader: scope.reader,
            scope: &scope.state.data,
        }));
        let retained_source_bytes = sources
            .capacity()
            .checked_mul(std::mem::size_of::<Source<'a>>())
            .ok_or(Error::BudgetExceeded("membership algebra source heap"))?;
        let state = MembershipAlgebraState::new(
            scopes.len(),
            |index| &scopes[index].state,
            budget,
            retained_source_bytes,
            cancellation,
        )?;
        Ok(Self { state, sources })
    }

    /// Address family shared by every source.
    pub const fn address_family(&self) -> AddressFamily {
        self.state.family
    }

    /// Number of unique global feed names.
    pub fn feed_count(&self) -> usize {
        self.state.names.len()
    }

    /// Unique global feed names in lexical order.
    pub fn feeds(&self) -> impl ExactSizeIterator<Item = FeedName> + '_ {
        self.state.names.iter().copied()
    }

    /// Count the address union of one global feed selection in one ordered pass.
    pub fn count(
        &self,
        feeds: FeedSelection<'_>,
        cancellation: &CancellationToken,
    ) -> Result<AlgebraCountReport> {
        analysis::count(self, feeds, 0, cancellation)
    }

    /// Compare two global feed selections in one ordered pass.
    pub fn compare(
        &self,
        left: FeedSelection<'_>,
        right: FeedSelection<'_>,
        cancellation: &CancellationToken,
    ) -> Result<AlgebraComparisonReport> {
        analysis::compare(self, left, right, 0, cancellation)
    }

    /// Materialize one set operation directly into its final immutable v4 file.
    #[allow(clippy::too_many_arguments)]
    pub fn publish_set(
        &self,
        destination: impl AsRef<std::path::Path>,
        value_tag: crate::ValueTag,
        operation: AlgebraSetOperation<'_>,
        mode: AlgebraOutputMode,
        metadata_json: Option<&[u8]>,
        publication_policy: crate::PublicationPolicy,
        budget: AlgebraOutputBudget,
        cancellation: &CancellationToken,
    ) -> AlgebraSetOutcome {
        output::publish(
            self,
            destination.as_ref(),
            value_tag,
            operation,
            mode,
            metadata_json,
            publication_policy,
            budget,
            0,
            cancellation,
        )
    }
}

impl AlgebraAccess for MembershipAlgebra<'_> {
    fn state(&self) -> &MembershipAlgebraState {
        &self.state
    }

    fn source(&self, index: usize) -> Result<AlgebraInput<'_>> {
        let source = self
            .sources
            .get(index)
            .ok_or(Error::Corrupt("membership algebra source disappeared"))?;
        self.state.input(index, source.reader, source.scope)
    }
}

impl MembershipAlgebraState {
    pub(crate) fn new<'a>(
        source_count: usize,
        mut scope: impl FnMut(usize) -> &'a MembershipScopeState,
        budget: MembershipAlgebraBudget,
        retained_source_bytes: usize,
        cancellation: &CancellationToken,
    ) -> Result<Self> {
        cancellation.check()?;
        Self::require_source_count(source_count, budget)?;
        let (family, total_entries) = inspect_sources(source_count, &mut scope, cancellation)?;

        let mut heap = Heap::new(budget.max_heap_bytes);
        heap.reserve_bytes(retained_source_bytes, "membership algebra source heap")?;
        let names = collect_names(
            source_count,
            total_entries,
            &mut scope,
            &mut heap,
            cancellation,
        )?;
        let inputs = build_inputs(source_count, &mut scope, &names, &mut heap, cancellation)?;
        Ok(Self {
            family,
            inputs,
            names,
            max_heap_bytes: budget.max_heap_bytes,
            heap_used: heap.used(),
        })
    }

    fn require_source_count(source_count: usize, budget: MembershipAlgebraBudget) -> Result<()> {
        if source_count == 0 {
            return Err(Error::InvalidArgument("membership algebra has no sources"));
        }
        if source_count > budget.max_sources as usize {
            return Err(Error::BudgetExceeded("membership algebra sources"));
        }
        Ok(())
    }

    pub(crate) const fn family(&self) -> AddressFamily {
        self.family
    }

    pub(crate) fn input_count(&self) -> usize {
        self.inputs.len()
    }

    pub(crate) fn names(&self) -> &[FeedName] {
        &self.names
    }

    pub(crate) fn input<'a>(
        &'a self,
        index: usize,
        reader: GenerationReader<'a>,
        scope: &'a ScopeData,
    ) -> Result<AlgebraInput<'a>> {
        let input = self
            .inputs
            .get(index)
            .ok_or(Error::Corrupt("membership algebra input disappeared"))?;
        Ok(AlgebraInput {
            reader,
            scope,
            expected_ranges: input.expected_ranges,
            local_to_global: &input.local_to_global,
        })
    }
}

fn inspect_sources<'a>(
    source_count: usize,
    scope: &mut impl FnMut(usize) -> &'a MembershipScopeState,
    cancellation: &CancellationToken,
) -> Result<(AddressFamily, usize)> {
    let family = scope(0).address_family;
    let mut total_entries = 0usize;
    for index in 0..source_count {
        if index & 4095 == 4095 {
            cancellation.check()?;
        }
        let current = scope(index);
        if current.address_family != family {
            return Err(Error::WrongAddressFamily(
                "membership algebra source families differ",
            ));
        }
        total_entries = total_entries
            .checked_add(current.data.entries.len())
            .ok_or(Error::BudgetExceeded("membership algebra catalog heap"))?;
    }
    Ok((family, total_entries))
}

fn collect_names<'a>(
    source_count: usize,
    total_entries: usize,
    scope: &mut impl FnMut(usize) -> &'a MembershipScopeState,
    heap: &mut Heap,
    cancellation: &CancellationToken,
) -> Result<Vec<FeedName>> {
    let mut names = heap.vector(total_entries, "membership algebra catalog heap")?;
    let mut entry_work = 0usize;
    for index in 0..source_count {
        if index & 4095 == 4095 {
            cancellation.check()?;
        }
        for entries in scope(index).data.entries.chunks(4096) {
            names.extend(entries.iter().map(|entry| entry.name));
            entry_work += entries.len();
            if entry_work >= 4096 {
                entry_work -= 4096;
                cancellation.check()?;
            }
        }
    }
    names.sort_unstable();
    names.dedup();
    Ok(names)
}

fn build_inputs<'a>(
    source_count: usize,
    scope: &mut impl FnMut(usize) -> &'a MembershipScopeState,
    names: &[FeedName],
    heap: &mut Heap,
    cancellation: &CancellationToken,
) -> Result<Vec<InputState>> {
    let mut inputs = heap.vector(source_count, "membership algebra source heap")?;
    for index in 0..source_count {
        if index & 4095 == 4095 {
            cancellation.check()?;
        }
        let current = scope(index);
        let mut local_to_global =
            heap.vector(current.data.entries.len(), "membership algebra source heap")?;
        for (work, entry) in current.data.entries.iter().enumerate() {
            if work & 4095 == 4095 {
                cancellation.check()?;
            }
            let position = names
                .binary_search(&entry.name)
                .map_err(|_| Error::Corrupt("global feed name disappeared"))?;
            local_to_global.push(
                u32::try_from(position)
                    .map_err(|_| Error::BudgetExceeded("membership algebra feeds"))?,
            );
        }
        inputs.push(InputState {
            expected_ranges: current.range_record_count,
            local_to_global,
        });
    }
    Ok(inputs)
}
