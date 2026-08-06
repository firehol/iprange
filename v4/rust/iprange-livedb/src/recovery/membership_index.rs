//! Recovery of authoritative membership-ID records and bitmap bytes.

use sha2::{Digest, Sha256};

use crate::cancellation::CancellationToken;
use crate::contract::{u32_le, MetaV4, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::mapping::{ByteSource, Mapping};
use crate::membership_dictionary::codec::{self, Record as StoredRecord, Storage as StoredStorage};
use crate::validation::{PhysicalByteInterval, ValidationObject, ValidationReason};

use super::catalog::Catalog;
use super::membership_blob;
use super::membership_table::Insert;
pub(crate) use super::membership_table::MembershipIndex;
use super::membership_words::read_inline;
use super::page_set::PageSet;
use super::report::{RecoverySink, Reporter, Unknown};
use super::tables::Tables;
use super::tree_scan::{self, CellLayout, Codec, TreeEvents};

#[derive(Clone, Copy)]
pub(crate) struct Locator {
    pub(crate) id: u32,
    pub(crate) word_count: u32,
    pub(crate) digest: [u8; 32],
    pub(crate) leaf_page: u32,
    pub(crate) leaf_index: u16,
    pub(crate) storage: StoredStorage,
    pub(crate) rejected: bool,
}

pub(crate) fn count(
    mapping: &Mapping,
    meta: MetaV4,
    pages: &mut PageSet,
    cancellation: &CancellationToken,
) -> Result<u64> {
    let mut events = CountEvents { meta, count: 0 };
    tree_scan::scan::<IdCodec, _>(
        mapping,
        meta,
        meta.membership_id_root,
        pages,
        cancellation,
        &mut events,
    )?;
    Ok(events.count)
}

pub(crate) fn recover<S: RecoverySink>(
    mapping: &Mapping,
    meta: MetaV4,
    catalog: &Catalog,
    pages: &mut PageSet,
    tables: &mut Tables,
    cancellation: &CancellationToken,
    reporter: &mut Reporter<'_, S>,
) -> Result<MembershipIndex> {
    let mut entries = MembershipIndex::new(tables);
    {
        let mut events = Events {
            meta,
            reporter,
            entries: &mut entries,
            tables,
        };
        tree_scan::scan::<IdCodec, _>(
            mapping,
            meta,
            meta.membership_id_root,
            pages,
            cancellation,
            &mut events,
        )?;
    }
    Validation {
        mapping,
        meta,
        catalog,
        pages,
        tables,
        cancellation,
        reporter,
    }
    .entries(&entries)?;
    finish(entries, tables, reporter)
}

struct Events<'a, 'b, S> {
    meta: MetaV4,
    reporter: &'a mut Reporter<'b, S>,
    entries: &'a mut MembershipIndex,
    tables: &'a mut Tables,
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

    fn leaf<P: ByteSource>(&mut self, page: u32, index: usize, cell: Option<P>) -> Result<()> {
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
        self.entries.push(
            self.tables,
            Locator {
                id: record.id,
                word_count: record.word_count,
                digest: record.digest,
                leaf_page: page,
                leaf_index,
                storage: record.storage,
                rejected: false,
            },
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

struct CountEvents {
    meta: MetaV4,
    count: u64,
}

impl TreeEvents for CountEvents {
    fn page_accepted(&mut self) -> Result<()> {
        Ok(())
    }

    fn page_rejected(&mut self, _io_unreadable: bool) -> Result<()> {
        Ok(())
    }

    fn unknown(
        &mut self,
        _reason: ValidationReason,
        _object: ValidationObject,
        _page: Option<u32>,
    ) -> Result<()> {
        Ok(())
    }

    fn leaf<P: ByteSource>(&mut self, _page: u32, _index: usize, cell: Option<P>) -> Result<()> {
        let Some(record) = cell.and_then(|cell| codec::decode(cell).ok()) else {
            return Ok(());
        };
        if record_fields_valid(record, self.meta) {
            self.count = self
                .count
                .checked_add(1)
                .ok_or(Error::ArithmeticOverflow("recovery membership count"))?;
        }
        Ok(())
    }
}

struct Validation<'a, 'b, S> {
    mapping: &'a Mapping,
    meta: MetaV4,
    catalog: &'a Catalog,
    pages: &'a mut PageSet,
    tables: &'a mut Tables,
    cancellation: &'a CancellationToken,
    reporter: &'a mut Reporter<'b, S>,
}

impl<S: RecoverySink> Validation<'_, '_, S> {
    fn entries(&mut self, entries: &MembershipIndex) -> Result<()> {
        for index in 0..entries.records_len() {
            self.one(entries, index)?;
        }
        Ok(())
    }

    fn one(&mut self, entries: &MembershipIndex, index: u64) -> Result<()> {
        self.cancellation.check()?;
        let entry = entries.record(self.tables, index)?;
        if !self.entry(entry)? {
            entries.reject(self.tables, index)?;
            emit(
                self.reporter,
                ValidationReason::MembershipInvalid,
                ValidationObject::MembershipDictionary,
                Some(entry.leaf_page),
            )?;
        }
        self.register_id(entries, entry.id, index)
    }

    fn register_id(&mut self, entries: &MembershipIndex, id: u32, index: u64) -> Result<()> {
        let Insert::Duplicate {
            first,
            newly_conflicted,
        } = entries.insert_id(self.tables, id, index)?
        else {
            return Ok(());
        };
        entries.reject(self.tables, first)?;
        entries.reject(self.tables, index)?;
        if newly_conflicted {
            emit(
                self.reporter,
                ValidationReason::MembershipInvalid,
                ValidationObject::MembershipDictionary,
                None,
            )?;
        }
        Ok(())
    }

    fn entry(&mut self, entry: Locator) -> Result<bool> {
        let mut bitmap = BitmapCheck::new(self.catalog, self.tables);
        let complete = match entry.storage {
            StoredStorage::Inline => {
                let bytes = read_inline(self.mapping, self.meta, entry)?;
                bitmap.consume(bytes)?;
                true
            }
            StoredStorage::Blob(root) => membership_blob::scan(
                self.mapping,
                self.meta,
                root,
                entry.word_count,
                self.pages,
                self.cancellation,
                self.reporter,
                |bytes| bitmap.consume(bytes),
            )?,
        };
        Ok(complete && bitmap.valid(entry.word_count, entry.digest))
    }
}

struct BitmapCheck<'a> {
    catalog: &'a Catalog,
    tables: &'a Tables,
    hasher: Sha256,
    words: u32,
    last: u64,
    inactive: bool,
}

impl<'a> BitmapCheck<'a> {
    fn new(catalog: &'a Catalog, tables: &'a Tables) -> Self {
        Self {
            catalog,
            tables,
            hasher: Sha256::new(),
            words: 0,
            last: 0,
            inactive: false,
        }
    }

    fn consume<P: ByteSource>(&mut self, bytes: P) -> Result<()> {
        if bytes.len() % 8 != 0 {
            return Err(Error::Corrupt(
                "recovery membership bytes are not word aligned",
            ));
        }
        for offset in (0..bytes.len()).step_by(8) {
            let value = crate::contract::u64_le(bytes, offset);
            self.hasher.update(value.to_le_bytes());
            let mut remaining = value;
            while remaining != 0 {
                let bit = remaining.trailing_zeros();
                let index = u64::from(self.words) * 64 + u64::from(bit);
                let index = u32::try_from(index)
                    .map_err(|_| Error::Corrupt("recovery membership feed bit is invalid"))?;
                self.inactive |= !self.catalog.contains(self.tables, index)?;
                remaining &= remaining - 1;
            }
            self.last = value;
            self.words = self
                .words
                .checked_add(1)
                .ok_or(Error::ArithmeticOverflow("recovery membership words"))?;
        }
        Ok(())
    }

    fn valid(self, expected_words: u32, expected_digest: [u8; 32]) -> bool {
        let digest = <Sha256 as Digest>::finalize(self.hasher);
        self.words == expected_words
            && self.last != 0
            && !self.inactive
            && digest.as_slice() == expected_digest
    }
}

fn finish<S: RecoverySink>(
    entries: MembershipIndex,
    tables: &Tables,
    reporter: &mut Reporter<'_, S>,
) -> Result<MembershipIndex> {
    let mut rejected = 0u64;
    for index in 0..entries.records_len() {
        rejected += u64::from(entries.record(tables, index)?.rejected);
    }
    reporter.membership_rejected(rejected)?;
    reporter.membership_accepted(entries.records_len() - rejected)?;
    Ok(entries)
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

    fn branch<P: ByteSource>(cell: P) -> Option<(Self::Key, u32)> {
        Some((u32_le(cell, 0), u32_le(cell, 4)))
    }

    fn leaf_key<P: ByteSource>(cell: P) -> Option<Self::Key> {
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
