//! Bounded scalar navigation hints for one private range build.

use std::mem::size_of;

use crate::error::{Error, Result};
use crate::fixed_tree;
use crate::heap::Heap;
use crate::key::IpKey;
use crate::range_tree::{RangeCodec, Record as Range};

use super::{account_private_insert, insert_private_gap, EncodedRange, PrivateGap, RangeStore};

const MAX_LOCATOR_BYTES: u64 = 256 * 1024;
// Stop paying locator branches once the input proves to be an overwrite run.
const LOCATOR_CONFLICT_LIMIT: u8 = 8;

#[derive(Clone, Copy, Debug)]
struct LeafHint<K> {
    first: K,
    page_number: u32,
}

#[derive(Clone, Copy, Debug)]
pub(super) struct Candidate {
    index: usize,
    page_number: u32,
}

impl Candidate {
    pub(super) const fn page_number(self) -> u32 {
        self.page_number
    }
}

#[derive(Debug)]
pub(super) struct LeafLocator<K> {
    hints: Vec<LeafHint<K>>,
}

impl<K: Copy + Ord> LeafLocator<K> {
    pub(super) fn new(max_heap_bytes: u64) -> Self {
        let bytes = max_heap_bytes.min(MAX_LOCATOR_BYTES);
        let capacity = usize::try_from(bytes / size_of::<LeafHint<K>>() as u64).unwrap_or(0);
        let hints = Heap::new(bytes)
            .vector(capacity, "private leaf locator")
            .unwrap_or_default();
        Self { hints }
    }

    pub(super) fn enabled(&self) -> bool {
        self.hints.capacity() != 0
    }

    pub(super) fn candidate(&self, key: K) -> Option<Candidate> {
        let index = self.hints.partition_point(|hint| hint.first <= key);
        index.checked_sub(1).map(|index| Candidate {
            index,
            page_number: self.hints[index].page_number,
        })
    }

    pub(super) fn learn(&mut self, first: K, page_number: u32, candidate: Option<Candidate>) {
        let following = candidate.map_or(0, |candidate| candidate.index + 1);
        let existing = candidate
            .filter(|candidate| candidate.page_number == page_number)
            .map(|candidate| candidate.index)
            .or_else(|| {
                self.hints
                    .get(following)
                    .filter(|hint| hint.page_number == page_number)
                    .map(|_| following)
            });
        if let Some(index) = existing {
            if self.hints[index].first == first {
                return;
            }
            self.hints.remove(index);
        }
        let index = self.hints.partition_point(|hint| hint.first < first);
        if let Some(hint) = self.hints.get_mut(index) {
            if hint.first == first {
                hint.page_number = page_number;
                return;
            }
        }
        if self.hints.len() < self.hints.capacity() {
            self.hints.insert(index, LeafHint { first, page_number });
        }
    }

    pub(super) fn clear(&mut self) {
        self.hints.clear();
    }

    pub(super) fn release(&mut self) {
        self.hints = Vec::new();
    }
}

#[derive(Debug)]
pub(crate) struct PrivateInput<K, const ADAPTIVE: bool> {
    locator: LeafLocator<K>,
    probe_locator: bool,
    local_conflicts: u8,
    pending_locator_bytes: u64,
}

pub(crate) type AssignmentInput<K> = PrivateInput<K, false>;
pub(super) type UnionAssignmentInput<K> = PrivateInput<K, true>;

impl<K: IpKey> PrivateInput<K, false> {
    pub(crate) fn new(max_heap_bytes: u64) -> Self {
        // IPv6's shorter leaves make strict-interior hints add more local
        // probes than the tree descents they avoid.
        let locator_bytes = if K::WIDTH == 4 { max_heap_bytes } else { 0 };
        Self {
            locator: LeafLocator::new(locator_bytes),
            probe_locator: true,
            local_conflicts: 0,
            pending_locator_bytes: 0,
        }
    }
}

impl<K: IpKey> PrivateInput<K, true> {
    pub(super) fn lazy(max_heap_bytes: u64) -> Self {
        Self {
            locator: LeafLocator::new(0),
            probe_locator: true,
            local_conflicts: 0,
            pending_locator_bytes: if K::WIDTH == 4 { max_heap_bytes } else { 0 },
        }
    }
}

impl<K: IpKey, const ADAPTIVE: bool> PrivateInput<K, ADAPTIVE> {
    pub(crate) fn enable(&mut self) {
        let bytes = std::mem::take(&mut self.pending_locator_bytes);
        if bytes == 0 {
            return;
        }
        self.locator = LeafLocator::new(bytes);
        self.probe_locator = true;
        self.local_conflicts = 0;
    }

    pub(super) fn disabled(&self) -> bool {
        !self.locator.enabled() && self.pending_locator_bytes == 0
    }

    pub(crate) fn release(&mut self) {
        self.locator.release();
        self.probe_locator = false;
        self.pending_locator_bytes = 0;
    }

    #[inline(always)]
    fn note_rejection<R>(&mut self, rejected: &fixed_tree::LocalReject<R>) {
        self.locator.clear();
        if !ADAPTIVE {
            return;
        }
        let local_conflict = rejected.predecessor().is_some() || rejected.successor().is_some();
        if local_conflict {
            self.local_conflicts = self.local_conflicts.saturating_add(1);
            self.probe_locator = false;
        } else {
            self.local_conflicts = 0;
            self.probe_locator = true;
        }
        if self.local_conflicts == LOCATOR_CONFLICT_LIMIT {
            self.locator.release();
        }
    }
}

// Rejections carry the complete positioned tree proof. Keeping it inline
// avoids a heap allocation on the general mutation path.
#[allow(clippy::large_enum_variant)]
pub(super) enum PrivateInputInsert<R> {
    Inserted,
    General(fixed_tree::LocalReject<R>),
}

enum CachedProbe {
    Inserted,
    Continue(Option<Candidate>),
}

#[inline(always)]
pub(super) fn insert_private_input_gap<K: IpKey, S: RangeStore, const ADAPTIVE: bool>(
    store: &mut S,
    root: &mut u32,
    record_count: &mut u64,
    range: Range<K>,
    input: &mut PrivateInput<K, ADAPTIVE>,
) -> Result<PrivateInputInsert<Range<K>>> {
    let Range { from, to, .. } = range;
    if from > to {
        return Err(Error::InvalidArgument("range start is after its end"));
    }
    if input.disabled() {
        return match insert_private_gap(store, root, record_count, range)? {
            fixed_tree::LocalInsert::Inserted(_) => Ok(PrivateInputInsert::Inserted),
            fixed_tree::LocalInsert::General(rejected) => Ok(PrivateInputInsert::General(rejected)),
        };
    }
    let locator_enabled = input.locator.enabled();
    let candidate = match probe_cached(store, root, record_count, range, input)? {
        CachedProbe::Inserted => return Ok(PrivateInputInsert::Inserted),
        CachedProbe::Continue(candidate) => candidate,
    };

    match insert_private_gap(store, root, record_count, range)? {
        fixed_tree::LocalInsert::Inserted(inserted) => {
            if locator_enabled {
                let first = fixed_tree::private_leaf_first::<RangeCodec<K>, S>(
                    store,
                    inserted.page_number(),
                )?;
                input
                    .locator
                    .learn(first, inserted.page_number(), candidate);
                if ADAPTIVE {
                    input.probe_locator = true;
                    input.local_conflicts = 0;
                }
            }
            Ok(PrivateInputInsert::Inserted)
        }
        fixed_tree::LocalInsert::General(rejected) => {
            input.note_rejection(&rejected);
            Ok(PrivateInputInsert::General(rejected))
        }
    }
}

#[inline(always)]
fn probe_cached<K: IpKey, S: RangeStore, const ADAPTIVE: bool>(
    store: &mut S,
    root: &u32,
    record_count: &mut u64,
    range: Range<K>,
    input: &mut PrivateInput<K, ADAPTIVE>,
) -> Result<CachedProbe> {
    if !input.locator.enabled() || (ADAPTIVE && !input.probe_locator) || *root == 0 {
        return Ok(CachedProbe::Continue(None));
    }
    let candidate = input.locator.candidate(range.from);
    let Some(selected) = candidate else {
        crate::work::leaf_locator_miss(1);
        crate::work::leaf_locator_fallback(1);
        return Ok(CachedProbe::Continue(None));
    };
    let encoded = EncodedRange::new(range)?;
    let mut gap = PrivateGap { range };
    if fixed_tree::insert_if_cached_interior_gap::<RangeCodec<K>, S, _>(
        store,
        selected.page_number(),
        encoded.as_slice(),
        &mut gap,
    )? == fixed_tree::CachedInsert::Inserted
    {
        account_private_insert(store, record_count, range.value)?;
        crate::work::leaf_locator_hit(1);
        if ADAPTIVE {
            input.local_conflicts = 0;
        }
        return Ok(CachedProbe::Inserted);
    }
    crate::work::leaf_locator_fallback(1);
    Ok(CachedProbe::Continue(candidate))
}
