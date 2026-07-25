//! Reconciliation of the redundant recovery-readable feed catalogs.

use std::fs::File;
use std::mem::size_of;

use crate::cancellation::CancellationToken;
use crate::contract::{u32_le, MetaV4, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::feed::{FeedEntry, FeedName};
use crate::feed_catalog;
use crate::validation::{PhysicalByteInterval, ValidationObject, ValidationReason};

use super::page_set::PageSet;
use super::report::{RecoverySink, Reporter, Unknown};
use super::tree_scan::{self, CellLayout, Codec, TreeEvents};
use super::{bounded_vec, bounded_vec::push};

#[derive(Clone, Copy)]
struct Candidate {
    entry: FeedEntry,
    rejected: bool,
}

pub(crate) struct Catalog {
    entries: Vec<Candidate>,
}

impl Catalog {
    pub(crate) fn entries(&self) -> impl Iterator<Item = FeedEntry> + '_ {
        self.entries.iter().map(|candidate| candidate.entry)
    }

    pub(crate) fn contains(&self, index: u32) -> bool {
        self.entries
            .binary_search_by_key(&index, |candidate| candidate.entry.index)
            .is_ok()
    }

    pub(crate) fn active_word(&self, word: u32) -> u64 {
        let first = u64::from(word) * 64;
        let end = first + 64;
        let start = self
            .entries
            .partition_point(|candidate| u64::from(candidate.entry.index) < first);
        let mut active = 0;
        for candidate in &self.entries[start..] {
            let index = u64::from(candidate.entry.index);
            if index >= end {
                break;
            }
            active |= 1u64 << (index - first);
        }
        active
    }

    pub(crate) fn retained_bytes(&self) -> u64 {
        self.entries.capacity() as u64 * size_of::<Candidate>() as u64
    }
}

pub(crate) fn recover<S: RecoverySink>(
    file: &File,
    meta: MetaV4,
    pages: &mut PageSet,
    max_heap_bytes: u64,
    cancellation: &CancellationToken,
    reporter: &mut Reporter<'_, S>,
) -> Result<Catalog> {
    let available = max_heap_bytes
        .checked_sub(pages.retained_bytes())
        .ok_or(Error::BudgetExceeded("recovery catalog candidates"))?;
    let maximum = bounded_vec::maximum::<Candidate>(available);
    let mut candidates = Vec::new();
    {
        let mut events = Events {
            meta,
            object: NameCodec::OBJECT,
            reporter,
            candidates: &mut candidates,
            maximum,
        };
        tree_scan::scan::<NameCodec, _>(
            file,
            meta,
            meta.catalog_name_root,
            pages,
            cancellation,
            &mut events,
        )?;
        events.object = IndexCodec::OBJECT;
        tree_scan::scan::<IndexCodec, _>(
            file,
            meta,
            meta.catalog_index_root,
            pages,
            cancellation,
            &mut events,
        )?;
    }
    reconcile(candidates, reporter)
}

struct Events<'a, 'b, S> {
    meta: MetaV4,
    object: ValidationObject,
    reporter: &'a mut Reporter<'b, S>,
    candidates: &'a mut Vec<Candidate>,
    maximum: usize,
}

impl<S: RecoverySink> TreeEvents for Events<'_, '_, S> {
    fn page_accepted(&mut self) -> Result<()> {
        self.reporter.page_accepted()
    }

    fn page_rejected(&mut self, io_unreadable: bool) -> Result<()> {
        self.reporter.page_rejected(io_unreadable)
    }

    fn unknown(
        &mut self,
        reason: ValidationReason,
        object: ValidationObject,
        page: Option<u32>,
    ) -> Result<()> {
        emit(self.reporter, reason, object, page)
    }

    fn leaf(&mut self, page: u32, _index: usize, cell: Option<&[u8]>) -> Result<()> {
        self.reporter.catalog_examined()?;
        let Some(entry) = cell.and_then(|cell| feed_catalog::decode_entry(cell).ok()) else {
            return self.reporter.catalog_rejected(1);
        };
        if u64::from(entry.index) >= self.meta.feed_index_limit {
            self.reporter.catalog_rejected(1)?;
            return emit(
                self.reporter,
                ValidationReason::CatalogInvalid,
                self.object,
                Some(page),
            );
        }
        push(
            self.candidates,
            Candidate {
                entry,
                rejected: false,
            },
            self.maximum,
            "recovery catalog candidates",
        )
    }
}

fn reconcile<S: RecoverySink>(
    mut candidates: Vec<Candidate>,
    reporter: &mut Reporter<'_, S>,
) -> Result<Catalog> {
    candidates.sort_unstable_by(|left, right| {
        (left.entry.name, left.entry.index).cmp(&(right.entry.name, right.entry.index))
    });
    mark_name_conflicts(&mut candidates, reporter)?;
    candidates.sort_unstable_by(|left, right| {
        (left.entry.index, left.entry.name).cmp(&(right.entry.index, right.entry.name))
    });
    mark_index_conflicts(&mut candidates, reporter)?;
    let rejected = candidates
        .iter()
        .filter(|candidate| candidate.rejected)
        .count() as u64;
    let accepted = candidates.len() as u64 - rejected;
    reporter.catalog_rejected(rejected)?;
    reporter.catalog_accepted(accepted)?;
    candidates.retain(|candidate| !candidate.rejected);
    candidates.dedup_by_key(|candidate| (candidate.entry.index, candidate.entry.name));
    candidates.shrink_to_fit();
    Ok(Catalog {
        entries: candidates,
    })
}

fn mark_name_conflicts<S: RecoverySink>(
    candidates: &mut [Candidate],
    reporter: &mut Reporter<'_, S>,
) -> Result<()> {
    let mut start = 0;
    while start < candidates.len() {
        let mut end = start + 1;
        while end < candidates.len() && candidates[end].entry.name == candidates[start].entry.name {
            end += 1;
        }
        if candidates[start..end]
            .windows(2)
            .any(|pair| pair[0].entry.index != pair[1].entry.index)
        {
            for candidate in &mut candidates[start..end] {
                candidate.rejected = true;
            }
            emit(
                reporter,
                ValidationReason::CatalogInvalid,
                ValidationObject::CatalogNameTree,
                None,
            )?;
        }
        start = end;
    }
    Ok(())
}

fn mark_index_conflicts<S: RecoverySink>(
    candidates: &mut [Candidate],
    reporter: &mut Reporter<'_, S>,
) -> Result<()> {
    let mut start = 0;
    while start < candidates.len() {
        let mut end = start + 1;
        while end < candidates.len() && candidates[end].entry.index == candidates[start].entry.index
        {
            end += 1;
        }
        if candidates[start..end]
            .windows(2)
            .any(|pair| pair[0].entry.name != pair[1].entry.name)
        {
            for candidate in &mut candidates[start..end] {
                candidate.rejected = true;
            }
            emit(
                reporter,
                ValidationReason::CatalogInvalid,
                ValidationObject::CatalogIndexTree,
                None,
            )?;
        }
        start = end;
    }
    Ok(())
}

struct NameCodec;

impl Codec for NameCodec {
    type Key = FeedName;

    const OBJECT: ValidationObject = ValidationObject::CatalogNameTree;
    const BRANCH_TYPE: u8 = feed_catalog::NAME_BRANCH;
    const LEAF_TYPE: u8 = feed_catalog::NAME_LEAF;
    const AUX: u32 = 0;
    const BRANCH_LAYOUT: CellLayout = CellLayout::Variable {
        minimum: feed_catalog::NAME_RECORD_BASE + 1,
        maximum: feed_catalog::MAX_NAME_RECORD,
    };
    const LEAF_LAYOUT: CellLayout = Self::BRANCH_LAYOUT;
    const BRANCH_INVALID: ValidationReason = ValidationReason::CatalogInvalid;
    const LEAF_INVALID: ValidationReason = ValidationReason::CatalogInvalid;

    fn branch(cell: &[u8]) -> Option<(Self::Key, u32)> {
        feed_catalog::decode_entry(cell)
            .ok()
            .map(|entry| (entry.name, entry.index))
    }

    fn leaf_key(cell: &[u8]) -> Option<Self::Key> {
        feed_catalog::decode_entry(cell)
            .ok()
            .map(|entry| entry.name)
    }
}

struct IndexCodec;

impl Codec for IndexCodec {
    type Key = u32;

    const OBJECT: ValidationObject = ValidationObject::CatalogIndexTree;
    const BRANCH_TYPE: u8 = feed_catalog::INDEX_BRANCH;
    const LEAF_TYPE: u8 = feed_catalog::INDEX_LEAF;
    const AUX: u32 = 0;
    const BRANCH_LAYOUT: CellLayout = CellLayout::Fixed(8);
    const LEAF_LAYOUT: CellLayout = CellLayout::Variable {
        minimum: feed_catalog::NAME_RECORD_BASE + 1,
        maximum: feed_catalog::MAX_NAME_RECORD,
    };
    const LEAF_INVALID: ValidationReason = ValidationReason::CatalogInvalid;

    fn branch(cell: &[u8]) -> Option<(Self::Key, u32)> {
        Some((u32_le(cell, 0), u32_le(cell, 4)))
    }

    fn leaf_key(cell: &[u8]) -> Option<Self::Key> {
        feed_catalog::decode_entry(cell)
            .ok()
            .map(|entry| entry.index)
    }
}

fn emit<S: RecoverySink>(
    reporter: &mut Reporter<'_, S>,
    reason: ValidationReason,
    object: ValidationObject,
    page: Option<u32>,
) -> Result<()> {
    reporter.unknown(Unknown {
        reason,
        object,
        page_number: page,
        physical_bytes: page.map(page_interval),
        address_fence: None,
        contributes_to_possible_span: false,
        has_unbounded_extent: false,
    })
}

fn page_interval(page: u32) -> PhysicalByteInterval {
    let start = u64::from(page) * PAGE_SIZE as u64;
    PhysicalByteInterval {
        start,
        end_exclusive: start + PAGE_SIZE as u64,
    }
}
