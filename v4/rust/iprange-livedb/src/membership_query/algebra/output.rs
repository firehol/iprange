//! Direct construction and publication of one algebra result file.

use std::fs::File;
use std::path::Path;

use crate::cancellation::CancellationToken;
use crate::cardinality::Cardinality129;
use crate::contract::{AddressFamily, ValueKind, ValueTag};
use crate::error::{Error, Result};
use crate::heap::Heap;
use crate::immutable_output::{Builder, MembershipWords, OutputBudget, OutputSpec};
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::publication::PublicationPolicy;

use super::scan::{self, SegmentSink};
use super::selection::Selection;
use super::{
    AlgebraAccess, AlgebraOutputBudget, AlgebraOutputMode, AlgebraPreparationFailure,
    AlgebraSetOperation, AlgebraSetOutcome, AlgebraSetReport, AlgebraSetResult,
};

struct BuildFailure {
    file: File,
    cause: Error,
}

struct Prepared {
    heap: Heap,
    plan: Plan,
    catalog_globals: Vec<u32>,
    output_feed_count: usize,
}

enum Plan {
    Union(Selection),
    Intersection(Selection),
    Exclusion {
        included: Selection,
        excluded: Selection,
    },
}

struct Pending<K> {
    from: K,
    to: K,
}

struct OutputSink<'a, K> {
    builder: &'a mut Builder,
    plan: Plan,
    mode: AlgebraOutputMode,
    global_to_output: Vec<u32>,
    current: Vec<u32>,
    pending_positions: Vec<u32>,
    interned_positions: Vec<u32>,
    interned_membership: Option<u32>,
    pending: Option<Pending<K>>,
    output_ranges: u64,
    output_addresses: Cardinality129,
    cancellation_work: u16,
    cache: super::super::cache::SequenceCache,
}

#[allow(clippy::too_many_arguments)]
pub(crate) fn publish(
    algebra: &(impl AlgebraAccess + ?Sized),
    destination: &Path,
    value_tag: ValueTag,
    operation: AlgebraSetOperation<'_>,
    mode: AlgebraOutputMode,
    metadata_json: Option<&[u8]>,
    policy: PublicationPolicy,
    budget: AlgebraOutputBudget,
    reserved_heap_bytes: u64,
    cancellation: &CancellationToken,
) -> AlgebraSetOutcome {
    if let Err(cause) = validate_budget(budget, policy) {
        return Err(Box::new(AlgebraPreparationFailure::early(cause)));
    }
    if let Err(cause) = cancellation.check() {
        return Err(Box::new(AlgebraPreparationFailure::early(cause)));
    }
    let prepared = Prepared::new(algebra, operation, mode, reserved_heap_bytes, cancellation)
        .map_err(|cause| Box::new(AlgebraPreparationFailure::early(cause)))?;
    let (attempt, file) =
        crate::publication::workflow::create(destination, policy).map_err(failure_from_early)?;
    let built = match build(
        algebra,
        file,
        value_tag,
        mode,
        metadata_json,
        budget,
        prepared,
        cancellation,
    ) {
        Ok(built) => built,
        Err(failure) => return Err(discard_attempt(attempt, failure.file, failure.cause)),
    };
    let publication = match crate::publication::workflow::publish(
        attempt,
        built.finished,
        policy,
        cancellation,
    ) {
        Ok(publication) => publication,
        Err(crate::publication::workflow::Failure::Early(failure)) => {
            return Err(failure_from_early(failure));
        }
        Err(crate::publication::workflow::Failure::Publication(failure)) => {
            return Err(Box::new(AlgebraPreparationFailure::from_publication(
                *failure,
            )));
        }
    };
    Ok(AlgebraSetResult {
        report: built.report,
        publication,
    })
}

struct Built {
    finished: crate::immutable_output::Finished,
    report: AlgebraSetReport,
}

#[allow(clippy::too_many_arguments)]
fn build(
    algebra: &(impl AlgebraAccess + ?Sized),
    file: File,
    value_tag: ValueTag,
    mode: AlgebraOutputMode,
    metadata_json: Option<&[u8]>,
    budget: AlgebraOutputBudget,
    mut prepared: Prepared,
    cancellation: &CancellationToken,
) -> std::result::Result<Built, BuildFailure> {
    let spec = match OutputSpec::fresh(
        algebra.state().family(),
        ValueKind::Membership,
        value_tag,
        prepared.output_feed_count as u64,
    ) {
        Ok(spec) => spec,
        Err(cause) => return Err(BuildFailure { file, cause }),
    };
    let output_budget = OutputBudget {
        max_output_pages: budget.max_output_pages,
    };
    let mut builder = Builder::new_owned_with_heap(file, spec, output_budget, &mut prepared.heap)
        .map_err(|failure| BuildFailure {
        file: failure.file,
        cause: failure.cause,
    })?;
    if let Some(metadata) = metadata_json {
        if let Err(cause) = builder.write_metadata_with_budget(metadata, prepared.heap.remaining())
        {
            return Err(BuildFailure {
                file: builder.into_file(),
                cause,
            });
        }
    }
    let result = build_mapped(
        algebra,
        &mut builder,
        prepared.plan,
        mode,
        &prepared.catalog_globals,
        &mut prepared.heap,
        cancellation,
    );
    let report = match result {
        Ok(report) => report,
        Err(cause) => {
            return Err(BuildFailure {
                file: builder.into_file(),
                cause,
            });
        }
    };
    let finished = builder.finish_owned().map_err(|failure| BuildFailure {
        file: failure.builder.into_file(),
        cause: failure.cause,
    })?;
    Ok(Built { finished, report })
}

#[allow(clippy::too_many_arguments)]
fn build_mapped(
    algebra: &(impl AlgebraAccess + ?Sized),
    builder: &mut Builder,
    plan: Plan,
    mode: AlgebraOutputMode,
    catalog_globals: &[u32],
    heap: &mut Heap,
    cancellation: &CancellationToken,
) -> Result<AlgebraSetReport> {
    match mode {
        AlgebraOutputMode::PreserveFeeds => {
            for (index, &global) in catalog_globals.iter().enumerate() {
                if index & 4095 == 4095 {
                    cancellation.check()?;
                }
                builder.push_feed(algebra.state().names()[global as usize], index as u32)?;
            }
        }
        AlgebraOutputMode::Flat(name) => builder.push_feed(name, 0)?,
    }
    let mut global_to_output = heap.filled(
        algebra.state().names().len(),
        u32::MAX,
        "membership algebra output heap",
    )?;
    if mode == AlgebraOutputMode::PreserveFeeds {
        for (output, &global) in catalog_globals.iter().enumerate() {
            if output & 4095 == 4095 {
                cancellation.check()?;
            }
            global_to_output[global as usize] = output as u32;
        }
    }
    let capacity = output_capacity(output_feed_count(mode, catalog_globals.len()))?;
    let current = heap.vector(capacity, "membership algebra output heap")?;
    let pending_positions = heap.vector(capacity, "membership algebra output heap")?;
    let interned_positions = heap.vector(capacity, "membership algebra output heap")?;
    let output = match algebra.state().family() {
        AddressFamily::Ipv4 => build_family::<Ipv4Key>(
            algebra,
            builder,
            plan,
            mode,
            global_to_output,
            current,
            pending_positions,
            interned_positions,
            heap,
            cancellation,
        )?,
        AddressFamily::Ipv6 => build_family::<Ipv6Key>(
            algebra,
            builder,
            plan,
            mode,
            global_to_output,
            current,
            pending_positions,
            interned_positions,
            heap,
            cancellation,
        )?,
    };
    Ok(AlgebraSetReport {
        source_count: algebra.state().input_count() as u64,
        source_range_count: output.scanned.source_ranges,
        joined_segment_count: output.scanned.segments,
        output_feed_count: output_feed_count(mode, catalog_globals.len()) as u64,
        output_range_count: output.ranges,
        output_addresses: output.addresses,
    })
}

struct FamilyReport {
    scanned: scan::ScanReport,
    ranges: u64,
    addresses: Cardinality129,
}

#[allow(clippy::too_many_arguments)]
fn build_family<K: OutputKey>(
    algebra: &(impl AlgebraAccess + ?Sized),
    builder: &mut Builder,
    plan: Plan,
    mode: AlgebraOutputMode,
    global_to_output: Vec<u32>,
    current: Vec<u32>,
    pending_positions: Vec<u32>,
    interned_positions: Vec<u32>,
    heap: &mut Heap,
    cancellation: &CancellationToken,
) -> Result<FamilyReport> {
    let mut sink = OutputSink::<K> {
        builder,
        plan,
        mode,
        global_to_output,
        current,
        pending_positions,
        interned_positions,
        interned_membership: None,
        pending: None,
        output_ranges: 0,
        output_addresses: Cardinality129::ZERO,
        cancellation_work: 0,
        cache: super::super::cache::SequenceCache::empty(),
    };
    let scanned = scan::run::<K, _>(algebra, heap, &mut sink, cancellation)?;
    sink.finish(cancellation)?;
    Ok(FamilyReport {
        scanned,
        ranges: sink.output_ranges,
        addresses: sink.output_addresses,
    })
}

impl Prepared {
    fn new(
        algebra: &(impl AlgebraAccess + ?Sized),
        operation: AlgebraSetOperation<'_>,
        mode: AlgebraOutputMode,
        reserved_heap_bytes: u64,
        cancellation: &CancellationToken,
    ) -> Result<Self> {
        let mut heap = algebra.operation_heap_reserved(reserved_heap_bytes)?;
        let plan = Plan::resolve(algebra, operation, &mut heap, cancellation)?;
        let catalog_globals = plan.catalog_positions(&mut heap, cancellation)?;
        let output_feed_count = output_feed_count(mode, catalog_globals.len());
        if output_feed_count > u32::MAX as usize {
            return Err(Error::BudgetExceeded("membership algebra output feeds"));
        }
        Ok(Self {
            heap,
            plan,
            catalog_globals,
            output_feed_count,
        })
    }
}

impl Plan {
    fn resolve(
        algebra: &(impl AlgebraAccess + ?Sized),
        operation: AlgebraSetOperation<'_>,
        heap: &mut Heap,
        cancellation: &CancellationToken,
    ) -> Result<Self> {
        Ok(match operation {
            AlgebraSetOperation::Union(feeds) => {
                Self::Union(Selection::resolve(algebra, feeds, heap, cancellation)?)
            }
            AlgebraSetOperation::Intersection(feeds) => {
                let selection = Selection::resolve(algebra, feeds, heap, cancellation)?;
                if selection.len() == 0 {
                    return Err(Error::InvalidArgument(
                        "membership algebra intersection is empty",
                    ));
                }
                Self::Intersection(selection)
            }
            AlgebraSetOperation::Exclusion { included, excluded } => Self::Exclusion {
                included: Selection::resolve(algebra, included, heap, cancellation)?,
                excluded: Selection::resolve(algebra, excluded, heap, cancellation)?,
            },
        })
    }

    fn catalog_positions(
        &self,
        heap: &mut Heap,
        cancellation: &CancellationToken,
    ) -> Result<Vec<u32>> {
        let selection = match self {
            Self::Union(selection) | Self::Intersection(selection) => selection,
            Self::Exclusion { included, .. } => included,
        };
        let mut positions = heap.vector(selection.len(), "membership algebra output heap")?;
        selection.for_each_position(cancellation, |position| {
            positions.push(position);
            Ok(())
        })?;
        Ok(positions)
    }

    fn qualifies(
        &self,
        present: &[u32],
        counts: &[u32],
        cancellation: &CancellationToken,
    ) -> Result<bool> {
        match self {
            Self::Union(selection) => selection.any(present, counts, cancellation),
            Self::Intersection(selection) => selection.all(present, counts, cancellation),
            Self::Exclusion { included, excluded } => {
                Ok(included.any(present, counts, cancellation)?
                    && !excluded.any(present, counts, cancellation)?)
            }
        }
    }

    fn fill_output(
        &self,
        present: &[u32],
        counts: &[u32],
        global_to_output: &[u32],
        output: &mut Vec<u32>,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        let mut push = |global: u32| {
            let mapped = global_to_output[global as usize];
            if mapped == u32::MAX {
                return Err(Error::Corrupt("membership algebra output feed disappeared"));
            }
            output.push(mapped);
            Ok(())
        };
        let sorted = match self {
            Self::Intersection(selection) => {
                selection.for_each_position(cancellation, &mut push)?;
                true
            }
            Self::Union(selection) => {
                selection.for_each_present(present, counts, cancellation, &mut push)?
            }
            Self::Exclusion { included, .. } => {
                included.for_each_present(present, counts, cancellation, &mut push)?
            }
        };
        if !sorted {
            output.sort_unstable();
        }
        Ok(())
    }
}

impl<K: OutputKey> SegmentSink<K> for OutputSink<'_, K> {
    fn enable_cache(&mut self, heap: &mut Heap, max_bytes: u64) -> Result<()> {
        self.cache.enable(heap, max_bytes)
    }

    fn segment(
        &mut self,
        from: K,
        to: K,
        present: &[u32],
        counts: &[u32],
        cancellation: &CancellationToken,
    ) -> Result<()> {
        self.cancellation_work += 1;
        if self.cancellation_work == 4096 {
            self.cancellation_work = 0;
            cancellation.check()?;
        }
        if !self.plan.qualifies(present, counts, cancellation)? {
            return self.flush(cancellation);
        }
        self.current.clear();
        match self.mode {
            AlgebraOutputMode::Flat(_) => self.current.push(0),
            AlgebraOutputMode::PreserveFeeds => {
                self.plan.fill_output(
                    present,
                    counts,
                    &self.global_to_output,
                    &mut self.current,
                    cancellation,
                )?;
            }
        }
        if self.current.is_empty() {
            return Err(Error::Corrupt(
                "membership algebra selected empty output membership",
            ));
        }
        let addresses = from.inclusive_cardinality(to)?;
        self.output_addresses = add(self.output_addresses, addresses)?;
        if let Some(pending) = &mut self.pending {
            if pending.to.checked_next() == Some(from)
                && self.current.as_slice() == self.pending_positions.as_slice()
            {
                pending.to = to;
                return Ok(());
            }
        }
        self.flush(cancellation)?;
        self.pending_positions.extend_from_slice(&self.current);
        self.pending = Some(Pending { from, to });
        Ok(())
    }
}

impl<K: OutputKey> OutputSink<'_, K> {
    fn finish(&mut self, cancellation: &CancellationToken) -> Result<()> {
        cancellation.check()?;
        self.flush(cancellation)
    }

    fn flush(&mut self, cancellation: &CancellationToken) -> Result<()> {
        let Some(pending) = self.pending.take() else {
            return Ok(());
        };
        let membership = if self.pending_positions == self.interned_positions {
            self.interned_membership
                .ok_or_else(|| Error::Corrupt("algebra output membership cache is empty"))?
        } else if let Some(membership) = self
            .cache
            .sequence_value(&self.pending_positions, cancellation)?
        {
            crate::work::membership_intern_cache_hit(1);
            membership
        } else {
            let words = PositionWords(&self.pending_positions);
            let membership = self.builder.intern_membership_value(&words)?;
            self.cache
                .insert_sequence(&self.pending_positions, membership, cancellation)?;
            membership
        };
        if self.pending_positions != self.interned_positions {
            self.interned_positions.clear();
            self.interned_positions
                .extend_from_slice(&self.pending_positions);
            self.interned_membership = Some(membership);
        }
        K::push_interned(self.builder, pending.from, pending.to, membership)?;
        self.output_ranges = self
            .output_ranges
            .checked_add(1)
            .ok_or_else(|| Error::ArithmeticOverflow("membership algebra output range count"))?;
        self.pending_positions.clear();
        Ok(())
    }
}

trait OutputKey: IpKey {
    fn push_interned(builder: &mut Builder, from: Self, to: Self, membership: u32) -> Result<()>;
}

impl OutputKey for Ipv4Key {
    fn push_interned(builder: &mut Builder, from: Self, to: Self, membership: u32) -> Result<()> {
        builder.push_interned_membership_v4(from, to, membership)
    }
}

impl OutputKey for Ipv6Key {
    fn push_interned(builder: &mut Builder, from: Self, to: Self, membership: u32) -> Result<()> {
        builder.push_interned_membership_v6(from, to, membership)
    }
}

struct PositionWords<'a>(&'a [u32]);

impl MembershipWords for PositionWords<'_> {
    fn word_count(&self) -> u32 {
        self.0.last().map_or(0, |position| position / 64 + 1)
    }

    fn read_words(&self, start: u32, output: &mut [u64]) -> Result<()> {
        output.fill(0);
        let end = start
            .checked_add(
                u32::try_from(output.len())
                    .map_err(|_| Error::ArithmeticOverflow("membership algebra word range"))?,
            )
            .ok_or_else(|| Error::ArithmeticOverflow("membership algebra word range"))?;
        let first_bit = start
            .checked_mul(64)
            .ok_or_else(|| Error::ArithmeticOverflow("membership algebra bit range"))?;
        let mut index = self.0.partition_point(|&position| position < first_bit);
        while let Some(&position) = self.0.get(index) {
            let word = position / 64;
            if word >= end {
                break;
            }
            output[(word - start) as usize] |= 1u64 << (position % 64);
            index += 1;
        }
        Ok(())
    }
}

fn output_feed_count(mode: AlgebraOutputMode, preserved: usize) -> usize {
    match mode {
        AlgebraOutputMode::PreserveFeeds => preserved,
        AlgebraOutputMode::Flat(_) => 1,
    }
}

fn output_capacity(feeds: usize) -> Result<usize> {
    if feeds > u32::MAX as usize {
        Err(Error::BudgetExceeded("membership algebra output feeds"))
    } else {
        Ok(feeds)
    }
}

fn validate_budget(budget: AlgebraOutputBudget, policy: PublicationPolicy) -> Result<()> {
    if budget.max_output_pages < 2 {
        return Err(Error::BudgetExceeded("membership algebra output pages"));
    }
    let required_files = if policy == PublicationPolicy::FailIfExists {
        2
    } else {
        3
    };
    if budget.max_open_files < required_files {
        return Err(Error::BudgetExceeded("membership algebra output files"));
    }
    Ok(())
}

fn add(left: Cardinality129, right: Cardinality129) -> Result<Cardinality129> {
    left.checked_add(right)
        .map_err(|_| Error::ArithmeticOverflow("membership algebra output addresses"))
}

fn discard_attempt(
    attempt: crate::publication::output::OutputAttempt,
    file: File,
    cause: Error,
) -> Box<AlgebraPreparationFailure> {
    let discarded = crate::publication::cleanup::discard_attempt(&attempt, &file);
    Box::new(AlgebraPreparationFailure::discarded(
        crate::publication::problem::Problem::sdk(&cause),
        discarded,
        None,
    ))
}

fn failure_from_early(
    failure: crate::publication::workflow::EarlyFailure,
) -> Box<AlgebraPreparationFailure> {
    match failure.discarded {
        Some(discarded) => Box::new(AlgebraPreparationFailure::discarded(
            failure.cause,
            discarded,
            None,
        )),
        None => Box::new(AlgebraPreparationFailure::new(
            failure.cause,
            None,
            None,
            None,
        )),
    }
}
