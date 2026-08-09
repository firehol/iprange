//! Selected membership-dictionary record reads.

use crate::contract::{u64_le, MetaV4};
use crate::error::{Error, Result};
use crate::fixed_tree::{self, LeafQuery};
use crate::format::Generation;
use crate::mapping::{ByteRange, ByteSource, Mapping};
use crate::membership_dictionary::codec;
use crate::slotted_page::{self, Header};

pub(crate) use codec::Storage;

#[derive(Clone, Copy, Debug)]
pub(crate) struct Record {
    pub(crate) id: u32,
    pub(crate) word_count: u32,
    pub(crate) leaf_page: u32,
    #[cfg(test)]
    pub(crate) leaf_index: usize,
    pub(crate) inline_data_offset: usize,
    pub(crate) storage: Storage,
}

pub(crate) fn find(mapping: &Mapping, meta: &MetaV4, id: u32) -> Result<Option<Record>> {
    crate::work::membership_lookup(1);
    if id == 0 {
        return Err(Error::Corrupt("stored membership ID is zero"));
    }
    fixed_tree::query::<codec::IdCodec, _, _>(
        &Generation::new(mapping, *meta),
        meta.membership_id_root,
        id,
        &mut MembershipLookup { meta },
    )
}

struct MembershipLookup<'a> {
    meta: &'a MetaV4,
}

impl LeafQuery<codec::IdCodec> for MembershipLookup<'_> {
    type Output = Record;

    fn inspect<S: ByteSource>(
        &mut self,
        page: S,
        header: &Header,
        page_number: u32,
        position: usize,
        exact: bool,
    ) -> Result<Option<Self::Output>> {
        crate::work::membership_leaf_read(1);
        leaf_result(page, header, page_number, position, exact, self.meta)
    }
}

pub(crate) fn read_inline_words(
    mapping: &Mapping,
    meta: &MetaV4,
    selected: Record,
    start: u32,
    output: &mut [u64],
) -> Result<()> {
    if selected.storage != Storage::Inline {
        return Err(Error::Corrupt("membership dictionary entry is not inline"));
    }
    let end = start
        .checked_add(output.len() as u32)
        .ok_or_else(|| Error::arithmetic_overflow("membership word range"))?;
    if end > selected.word_count {
        return Err(Error::Corrupt("membership word range exceeds its length"));
    }
    let page = mapping.page(selected.leaf_page, meta.page_count)?;
    let start = selected
        .inline_data_offset
        .checked_add(inline_offset(start)?)
        .ok_or_else(|| Error::arithmetic_overflow("membership word offset"))?;
    let length = output
        .len()
        .checked_mul(8)
        .ok_or_else(|| Error::arithmetic_overflow("membership word length"))?;
    let bytes = ByteRange::new(page, start, length)
        .ok_or_else(|| Error::corrupt("membership inline bitmap is outside its page"))?;
    for (index, word) in output.iter_mut().enumerate() {
        *word = u64_le(bytes, index * 8);
    }
    Ok(())
}

fn inline_offset(word: u32) -> Result<usize> {
    usize::try_from(word)
        .ok()
        .and_then(|word| word.checked_mul(8))
        .ok_or_else(|| Error::arithmetic_overflow("membership word offset"))
}

fn leaf_result<S: ByteSource>(
    page: S,
    header: &Header,
    page_number: u32,
    position: usize,
    exact: bool,
    meta: &MetaV4,
) -> Result<Option<Record>> {
    if !exact {
        return Ok(None);
    }
    decode_record(page, header, page_number, position, meta).map(Some)
}

fn decode_record<S: ByteSource>(
    page: S,
    header: &Header,
    page_number: u32,
    index: usize,
    meta: &MetaV4,
) -> Result<Record> {
    let bytes = slotted_page::record(page, header, index, codec::ID_BASE, codec::MAX_ID_RECORD)?;
    let decoded = codec::decode(bytes)?;
    require_record_fields(decoded, meta)?;
    let storage = decoded.storage;
    let inline_data_offset = if storage == Storage::Inline {
        codec::inline_page_offset(bytes, decoded)?
    } else {
        0
    };
    Ok(Record {
        id: decoded.id,
        word_count: decoded.word_count,
        leaf_page: page_number,
        #[cfg(test)]
        leaf_index: index,
        inline_data_offset,
        storage,
    })
}

fn require_record_fields(record: codec::Record, meta: &MetaV4) -> Result<()> {
    require_id(record.id, meta.membership_id_limit)?;
    if record.refcount == 0
        || u64::from(record.word_count) > maximum_words(meta.feed_index_limit)
        || matches!(record.storage, Storage::Blob(root) if u64::from(root) >= meta.page_count)
    {
        return Err(Error::Corrupt("membership dictionary record is malformed"));
    }
    Ok(())
}

fn require_id(id: u32, limit: u64) -> Result<()> {
    if id == 0 || u64::from(id) >= limit {
        return Err(Error::Corrupt(
            "membership ID is outside the declared namespace",
        ));
    }
    Ok(())
}

fn maximum_words(feed_index_limit: u64) -> u64 {
    feed_index_limit.saturating_add(63) / 64
}
