//! Selected membership-dictionary record reads.

use crate::contract::{u64_le, MetaV4, MAX_TREE_LEVEL};
use crate::error::{Error, Result};
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
    crate::work::tree_lookup(1);
    if id == 0 {
        return Err(Error::Corrupt("stored membership ID is zero"));
    }
    if meta.membership_id_root == 0 {
        return Ok(None);
    }
    let mut page_number = meta.membership_id_root;
    let mut expected = None;
    for _ in 0..=MAX_TREE_LEVEL {
        let page = mapping.page(page_number, meta.page_count)?;
        let header = parse_header(page, meta.txn_id, expected)?;
        let (position, exact) = lower_bound(page, &header, id, meta)?;
        if header.level == 0 {
            crate::work::membership_leaf_read(1);
            return leaf_result(page, &header, page_number, position, exact, meta);
        }
        let Some(position) = position.checked_sub(usize::from(!exact)) else {
            return Ok(None);
        };
        page_number = branch_child(page, &header, position, meta.page_count)?;
        expected = Some(header.level - 1);
        crate::work::tree_descent(1);
    }
    Err(Error::Corrupt(
        "membership ID tree exceeds its maximum height",
    ))
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
        .ok_or(Error::ArithmeticOverflow("membership word range"))?;
    if end > selected.word_count {
        return Err(Error::Corrupt("membership word range exceeds its length"));
    }
    let page = mapping.page(selected.leaf_page, meta.page_count)?;
    let start = selected
        .inline_data_offset
        .checked_add(inline_offset(start)?)
        .ok_or(Error::ArithmeticOverflow("membership word offset"))?;
    let length = output
        .len()
        .checked_mul(8)
        .ok_or(Error::ArithmeticOverflow("membership word length"))?;
    let bytes = ByteRange::new(page, start, length).ok_or(Error::Corrupt(
        "membership inline bitmap is outside its page",
    ))?;
    for (index, word) in output.iter_mut().enumerate() {
        *word = u64_le(bytes, index * 8);
    }
    Ok(())
}

fn inline_offset(word: u32) -> Result<usize> {
    usize::try_from(word)
        .ok()
        .and_then(|word| word.checked_mul(8))
        .ok_or(Error::ArithmeticOverflow("membership word offset"))
}

fn lower_bound<S: ByteSource>(
    page: S,
    header: &Header,
    target: u32,
    meta: &MetaV4,
) -> Result<(usize, bool)> {
    let mut lower = 0;
    let mut upper = header.item_count;
    while lower < upper {
        let middle = lower + (upper - lower) / 2;
        if key_at(page, header, middle, meta)? < target {
            lower = middle + 1;
        } else {
            upper = middle;
        }
    }
    let exact = lower < header.item_count && key_at(page, header, lower, meta)? == target;
    Ok((lower, exact))
}

fn key_at<S: ByteSource>(page: S, header: &Header, index: usize, meta: &MetaV4) -> Result<u32> {
    if header.level == 0 {
        return Ok(decode_record(page, header, 0, index, meta)?.id);
    }
    let cell = slotted_page::cell(page, header, index, codec::ID_BRANCH_SIZE)?;
    let (id, _) = codec::decode_id_branch(cell)?;
    require_id(id, meta.membership_id_limit)?;
    Ok(id)
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

fn branch_child<S: ByteSource>(
    page: S,
    header: &Header,
    index: usize,
    page_count: u64,
) -> Result<u32> {
    let cell = slotted_page::cell(page, header, index, codec::ID_BRANCH_SIZE)?;
    let (_, child) = codec::decode_id_branch(cell)?;
    if child < 2 || u64::from(child) >= page_count {
        return Err(Error::Corrupt(
            "membership dictionary child is outside page bounds",
        ));
    }
    Ok(child)
}

fn parse_header<S: ByteSource>(
    page: S,
    selected_txn: u64,
    expected: Option<u16>,
) -> Result<Header> {
    slotted_page::parse_tree(
        page,
        selected_txn,
        codec::ID_BRANCH,
        codec::ID_LEAF,
        0,
        expected,
    )
}
