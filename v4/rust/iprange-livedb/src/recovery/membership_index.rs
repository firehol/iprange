//! Recovery of authoritative membership-ID records and bitmap bytes.

use std::fs::File;
use std::mem::size_of;

use sha2::{Digest, Sha256};

use crate::blob_tree;
use crate::cancellation::CancellationToken;
use crate::contract::{u32_le, MetaV4, PAGE_SIZE};
use crate::crc32c;
use crate::error::{Error, Result};
use crate::file_io;
use crate::immutable_output::MembershipWords;
use crate::membership_dictionary::codec::{self, Record as StoredRecord, Storage as StoredStorage};
use crate::slotted_page;
use crate::validation::{PhysicalByteInterval, ValidationObject, ValidationReason};

use super::bounded_vec;
use super::catalog::Catalog;
use super::membership_blob;
use super::page_set::PageSet;
use super::report::{RecoverySink, Reporter, Unknown};
use super::tree_scan::{self, CellLayout, Codec, TreeEvents};

const WORD_BUFFER: usize = 64;

#[derive(Clone, Copy)]
pub(crate) struct Locator {
    pub(crate) id: u32,
    pub(crate) word_count: u32,
    pub(crate) digest: [u8; 32],
    leaf_page: u32,
    leaf_index: u16,
    storage: StoredStorage,
    rejected: bool,
}

pub(crate) struct MembershipIndex {
    entries: Vec<Locator>,
}

impl MembershipIndex {
    pub(crate) fn get(&self, id: u32) -> Option<Locator> {
        self.entries
            .binary_search_by_key(&id, |entry| entry.id)
            .ok()
            .map(|index| self.entries[index])
    }

    pub(crate) fn retained_bytes(&self) -> u64 {
        self.entries.capacity() as u64 * size_of::<Locator>() as u64
    }
}

pub(crate) fn recover<S: RecoverySink>(
    file: &File,
    meta: MetaV4,
    catalog: &Catalog,
    pages: &mut PageSet,
    max_heap_bytes: u64,
    cancellation: &CancellationToken,
    reporter: &mut Reporter<'_, S>,
) -> Result<MembershipIndex> {
    let retained = pages
        .retained_bytes()
        .checked_add(catalog.retained_bytes())
        .ok_or(Error::ArithmeticOverflow("recovery membership heap"))?;
    let available = max_heap_bytes
        .checked_sub(retained)
        .ok_or(Error::BudgetExceeded("recovery membership table"))?;
    let maximum = bounded_vec::maximum::<Locator>(available);
    let mut entries = Vec::new();
    {
        let mut events = Events {
            meta,
            reporter,
            entries: &mut entries,
            maximum,
        };
        tree_scan::scan::<IdCodec, _>(
            file,
            meta,
            meta.membership_id_root,
            pages,
            cancellation,
            &mut events,
        )?;
    }
    validate_entries(
        file,
        meta,
        catalog,
        pages,
        cancellation,
        reporter,
        &mut entries,
    )?;
    reconcile(entries, reporter)
}

struct Events<'a, 'b, S> {
    meta: MetaV4,
    reporter: &'a mut Reporter<'b, S>,
    entries: &'a mut Vec<Locator>,
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

    fn leaf(&mut self, page: u32, index: usize, cell: Option<&[u8]>) -> Result<()> {
        self.reporter.membership_examined()?;
        let Some(record) = cell.and_then(|cell| codec::decode(cell).ok()) else {
            return self.reporter.membership_rejected(1);
        };
        if !record_fields_valid(record, self.meta) {
            self.reporter.membership_rejected(1)?;
            return emit(
                self.reporter,
                ValidationReason::MembershipInvalid,
                ValidationObject::MembershipDictionary,
                Some(page),
            );
        }
        let leaf_index = u16::try_from(index)
            .map_err(|_| Error::Corrupt("membership leaf index exceeds its page"))?;
        bounded_vec::push(
            self.entries,
            Locator {
                id: record.id,
                word_count: record.word_count,
                digest: record.digest,
                leaf_page: page,
                leaf_index,
                storage: record.storage,
                rejected: false,
            },
            self.maximum,
            "recovery membership table",
        )
    }
}

fn record_fields_valid(record: StoredRecord, meta: MetaV4) -> bool {
    let maximum_words = meta.feed_index_limit.saturating_add(63) / 64;
    u64::from(record.id) < meta.membership_id_limit
        && u64::from(record.word_count) <= maximum_words
        && match record.storage {
            StoredStorage::Inline => true,
            StoredStorage::Blob(root) => u64::from(root) < meta.page_count,
        }
}

fn validate_entries<S: RecoverySink>(
    file: &File,
    meta: MetaV4,
    catalog: &Catalog,
    pages: &mut PageSet,
    cancellation: &CancellationToken,
    reporter: &mut Reporter<'_, S>,
    entries: &mut [Locator],
) -> Result<()> {
    for entry in entries {
        cancellation.check()?;
        if !validate_entry(file, meta, catalog, pages, cancellation, reporter, *entry)? {
            entry.rejected = true;
            emit(
                reporter,
                ValidationReason::MembershipInvalid,
                ValidationObject::MembershipDictionary,
                Some(entry.leaf_page),
            )?;
        }
    }
    Ok(())
}

fn validate_entry<S: RecoverySink>(
    file: &File,
    meta: MetaV4,
    catalog: &Catalog,
    pages: &mut PageSet,
    cancellation: &CancellationToken,
    reporter: &mut Reporter<'_, S>,
    entry: Locator,
) -> Result<bool> {
    let mut bitmap = BitmapCheck::new(catalog);
    let complete = match entry.storage {
        StoredStorage::Inline => {
            let bytes = read_inline(file, meta, entry)?;
            bitmap.consume(bytes.as_slice())?;
            true
        }
        StoredStorage::Blob(root) => membership_blob::scan(
            file,
            meta,
            root,
            entry.word_count,
            pages,
            cancellation,
            reporter,
            |bytes| bitmap.consume(bytes),
        )?,
    };
    Ok(complete && bitmap.valid(entry.word_count, entry.digest))
}

struct BitmapCheck<'a> {
    catalog: &'a Catalog,
    hasher: Sha256,
    words: u32,
    last: u64,
    inactive: bool,
}

impl<'a> BitmapCheck<'a> {
    fn new(catalog: &'a Catalog) -> Self {
        Self {
            catalog,
            hasher: Sha256::new(),
            words: 0,
            last: 0,
            inactive: false,
        }
    }

    fn consume(&mut self, bytes: &[u8]) -> Result<()> {
        if bytes.len() % 8 != 0 {
            return Err(Error::Corrupt(
                "recovery membership bytes are not word aligned",
            ));
        }
        self.hasher.update(bytes);
        for bytes in bytes.chunks_exact(8) {
            let value = u64::from_le_bytes(bytes.try_into().expect("eight-byte word"));
            self.inactive |= value & !self.catalog.active_word(self.words) != 0;
            self.last = value;
            self.words = self
                .words
                .checked_add(1)
                .ok_or(Error::ArithmeticOverflow("recovery membership words"))?;
        }
        Ok(())
    }

    fn valid(self, expected_words: u32, expected_digest: [u8; 32]) -> bool {
        self.words == expected_words
            && self.last != 0
            && !self.inactive
            && <Sha256 as Digest>::finalize(self.hasher).as_slice() == expected_digest
    }
}

struct InlineBytes {
    bytes: [u8; PAGE_SIZE],
    len: usize,
}

impl InlineBytes {
    fn as_slice(&self) -> &[u8] {
        &self.bytes[..self.len]
    }
}

fn read_inline(file: &File, meta: MetaV4, locator: Locator) -> Result<InlineBytes> {
    let mut page = [0; PAGE_SIZE];
    file_io::read_page(file, locator.leaf_page, meta.page_count, &mut page)?;
    if crc32c::crc32c_with_zeroed(&page, 28, 4) != Some(u32_le(&page, 28)) {
        return Err(Error::RecoveryCandidateChanged);
    }
    let header = slotted_page::parse(&page, meta.txn_id, codec::ID_LEAF, 0, Some(0))?;
    let cell = slotted_page::record(
        &page,
        &header,
        usize::from(locator.leaf_index),
        codec::ID_BASE,
        codec::MAX_ID_RECORD,
    )?;
    let record = codec::decode(cell)?;
    if !matches_inline(record, locator) {
        return Err(Error::RecoveryCandidateChanged);
    }
    let source = &cell[codec::ID_BASE..];
    let mut bytes = [0; PAGE_SIZE];
    bytes[..source.len()].copy_from_slice(source);
    Ok(InlineBytes {
        bytes,
        len: source.len(),
    })
}

fn matches_inline(record: StoredRecord, locator: Locator) -> bool {
    record.id == locator.id
        && record.word_count == locator.word_count
        && record.digest == locator.digest
        && record.storage == StoredStorage::Inline
}

fn reconcile<S: RecoverySink>(
    mut entries: Vec<Locator>,
    reporter: &mut Reporter<'_, S>,
) -> Result<MembershipIndex> {
    entries.sort_unstable_by_key(|entry| entry.id);
    let mut start = 0;
    while start < entries.len() {
        let mut end = start + 1;
        while end < entries.len() && entries[end].id == entries[start].id {
            end += 1;
        }
        if end - start > 1 {
            for entry in &mut entries[start..end] {
                entry.rejected = true;
            }
            emit(
                reporter,
                ValidationReason::MembershipInvalid,
                ValidationObject::MembershipDictionary,
                None,
            )?;
        }
        start = end;
    }
    let rejected = entries.iter().filter(|entry| entry.rejected).count() as u64;
    reporter.membership_rejected(rejected)?;
    reporter.membership_accepted(entries.len() as u64 - rejected)?;
    entries.retain(|entry| !entry.rejected);
    entries.shrink_to_fit();
    Ok(MembershipIndex { entries })
}

pub(crate) struct RecoveredWords<'a> {
    file: &'a File,
    meta: MetaV4,
    locator: Locator,
}

impl Locator {
    pub(crate) fn words<'a>(self, file: &'a File, meta: MetaV4) -> RecoveredWords<'a> {
        RecoveredWords {
            file,
            meta,
            locator: self,
        }
    }

    pub(crate) fn equal(self, other: Self, file: &File, meta: MetaV4) -> Result<bool> {
        if self.word_count != other.word_count || self.digest != other.digest {
            return Ok(false);
        }
        let left = self.words(file, meta);
        let right = other.words(file, meta);
        let mut left_words = [0; WORD_BUFFER];
        let mut right_words = [0; WORD_BUFFER];
        let mut start = 0;
        while start < self.word_count {
            let count = (self.word_count - start).min(WORD_BUFFER as u32) as usize;
            left.read_words(start, &mut left_words[..count])?;
            right.read_words(start, &mut right_words[..count])?;
            if left_words[..count] != right_words[..count] {
                return Ok(false);
            }
            start += count as u32;
        }
        Ok(true)
    }
}

impl MembershipWords for RecoveredWords<'_> {
    fn word_count(&self) -> u32 {
        self.locator.word_count
    }

    fn read_words(&self, start: u32, output: &mut [u64]) -> Result<()> {
        let end = start
            .checked_add(output.len() as u32)
            .ok_or(Error::ArithmeticOverflow("recovery membership read"))?;
        if end > self.locator.word_count {
            return Err(Error::Corrupt(
                "recovery membership read exceeds its bitmap",
            ));
        }
        match self.locator.storage {
            StoredStorage::Inline => {
                read_inline_words(self.file, self.meta, self.locator, start, output)
            }
            StoredStorage::Blob(root) => blob_tree::read_words(
                self.file,
                &self.meta,
                root,
                self.locator.word_count,
                start,
                output,
            ),
        }
    }
}

fn read_inline_words(
    file: &File,
    meta: MetaV4,
    locator: Locator,
    start: u32,
    output: &mut [u64],
) -> Result<()> {
    let bytes = read_inline(file, meta, locator)?;
    let bytes = bytes.as_slice();
    let start = usize::try_from(start)
        .ok()
        .and_then(|word| word.checked_mul(8))
        .ok_or(Error::ArithmeticOverflow("recovery membership offset"))?;
    for (index, word) in output.iter_mut().enumerate() {
        *word = u64::from_le_bytes(
            bytes[start + index * 8..start + (index + 1) * 8]
                .try_into()
                .expect("eight-byte word"),
        );
    }
    Ok(())
}

struct IdCodec;

impl Codec for IdCodec {
    type Key = u32;

    const OBJECT: ValidationObject = ValidationObject::MembershipDictionary;
    const BRANCH_TYPE: u8 = codec::ID_BRANCH;
    const LEAF_TYPE: u8 = codec::ID_LEAF;
    const AUX: u32 = 0;
    const BRANCH_LAYOUT: CellLayout = CellLayout::Fixed(8);
    const LEAF_LAYOUT: CellLayout = CellLayout::Variable {
        minimum: codec::ID_BASE,
        maximum: codec::MAX_ID_RECORD,
    };
    const LEAF_INVALID: ValidationReason = ValidationReason::MembershipInvalid;

    fn branch(cell: &[u8]) -> Option<(Self::Key, u32)> {
        Some((u32_le(cell, 0), u32_le(cell, 4)))
    }

    fn leaf_key(cell: &[u8]) -> Option<Self::Key> {
        codec::decode(cell).ok().map(|record| record.id)
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
