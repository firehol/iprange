//! Verified bitmap reads from recovered membership locators.

use std::fs::File;

use crate::blob_tree;
use crate::contract::{u32_le, MetaV4, PAGE_SIZE};
use crate::crc32c;
use crate::error::{Error, Result};
use crate::file_io;
use crate::immutable_output::MembershipWords;
use crate::membership_dictionary::codec::{self, Record as StoredRecord, Storage};
use crate::slotted_page;

use super::membership_index::Locator;

const WORD_BUFFER: usize = 64;

pub(crate) struct InlineBytes {
    bytes: [u8; PAGE_SIZE],
    len: usize,
}

impl InlineBytes {
    pub(crate) fn as_slice(&self) -> &[u8] {
        &self.bytes[..self.len]
    }
}

pub(crate) fn read_inline(file: &File, meta: MetaV4, locator: Locator) -> Result<InlineBytes> {
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
        && record.storage == Storage::Inline
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
            Storage::Inline => read_inline_words(self.file, self.meta, self.locator, start, output),
            Storage::Blob(root) => blob_tree::read_words(
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
