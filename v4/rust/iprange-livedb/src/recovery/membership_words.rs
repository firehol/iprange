//! Verified bitmap reads from recovered membership locators.

use crate::blob_tree;
use crate::contract::MetaV4;
use crate::error::{Error, Result};
use crate::immutable_output::MembershipWords;
use crate::mapping::{ByteRange, ByteSource, Mapping, PageView};
use crate::membership_dictionary::codec::{self, Record as StoredRecord, Storage};
use crate::slotted_page;

use super::membership_index::Locator;

const WORD_BUFFER: usize = 64;

pub(crate) fn read_inline(
    mapping: &Mapping,
    meta: MetaV4,
    locator: Locator,
) -> Result<ByteRange<ByteRange<PageView<'_>>>> {
    let page = mapping.page(locator.leaf_page, meta.page_count)?;
    if !crate::page_checksum::valid(page) {
        return Err(Error::RecoveryCandidateChanged);
    }
    let header = slotted_page::parse(page, meta.txn_id, codec::ID_LEAF, 0, Some(0))?;
    let cell = slotted_page::record(
        page,
        &header,
        usize::from(locator.leaf_index),
        codec::ID_BASE,
        codec::MAX_ID_RECORD,
    )?;
    let record = codec::decode(cell)?;
    if !matches_inline(record, locator) {
        return Err(Error::RecoveryCandidateChanged);
    }
    codec::inline_bytes(cell, record).map_err(|_| Error::RecoveryCandidateChanged)
}

fn matches_inline(record: StoredRecord, locator: Locator) -> bool {
    record.id == locator.id
        && record.word_count == locator.word_count
        && record.digest == locator.digest
        && record.storage == Storage::Inline
}

pub(crate) struct RecoveredWords<'a> {
    mapping: &'a Mapping,
    meta: MetaV4,
    locator: Locator,
}

impl Locator {
    pub(crate) fn words<'a>(self, mapping: &'a Mapping, meta: MetaV4) -> RecoveredWords<'a> {
        RecoveredWords {
            mapping,
            meta,
            locator: self,
        }
    }

    pub(crate) fn equal(self, other: Self, mapping: &Mapping, meta: MetaV4) -> Result<bool> {
        if self.word_count != other.word_count || self.digest != other.digest {
            return Ok(false);
        }
        let left = self.words(mapping, meta);
        let right = other.words(mapping, meta);
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
            Storage::Inline => {
                read_inline_words(self.mapping, self.meta, self.locator, start, output)
            }
            Storage::Blob(root) => blob_tree::read_words(
                self.mapping,
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
    mapping: &Mapping,
    meta: MetaV4,
    locator: Locator,
    start: u32,
    output: &mut [u64],
) -> Result<()> {
    let bytes = read_inline(mapping, meta, locator)?;
    let start = usize::try_from(start)
        .ok()
        .and_then(|word| word.checked_mul(8))
        .ok_or(Error::ArithmeticOverflow("recovery membership offset"))?;
    for (index, word) in output.iter_mut().enumerate() {
        *word = u64::from_le_bytes(
            bytes
                .array(start + index * 8)
                .ok_or(Error::RecoveryCandidateChanged)?,
        );
    }
    Ok(())
}
