//! Direct ID-indexed mapped structure records.

use crate::contract::PAGE_SIZE;
use crate::error::{Error, Result};
use crate::fixed_tree::{PageSource, RetiredPages, RetiringStore, Store};
use crate::format::page_type;
use crate::mapping::{ByteRange, ByteSource};
use crate::page_header;
use crate::page_io::{PageEdit, PageSink};

use super::codec::{self, PayloadCodec, Record, REFCOUNT_OFFSET};

const BRANCH_CHILDREN: usize = 512;
const BRANCH_BYTES: usize = BRANCH_CHILDREN * 4;
const BRANCH_END: usize = page_header::SIZE + BRANCH_BYTES;
const MAX_LEVEL: u16 = 3;

#[derive(Clone, Copy)]
pub(crate) struct Header {
    pub(crate) level: u16,
    pub(crate) item_count: usize,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum HeaderProblem {
    Header,
    Born,
    Type,
    Level,
    Shape,
}

#[derive(Clone, Copy)]
struct Frame {
    page_number: u32,
    child_index: usize,
    item_count: usize,
}

const EMPTY_FRAME: Frame = Frame {
    page_number: 0,
    child_index: 0,
    item_count: 0,
};

struct Path {
    frames: [Frame; MAX_LEVEL as usize],
    depth: usize,
}

impl Path {
    const fn new() -> Self {
        Self {
            frames: [EMPTY_FRAME; MAX_LEVEL as usize],
            depth: 0,
        }
    }

    fn push(&mut self, frame: Frame) -> Result<()> {
        let slot = self.frames.get_mut(self.depth).ok_or(Error::Corrupt(
            "structure table path exceeds maximum height",
        ))?;
        *slot = frame;
        self.depth += 1;
        Ok(())
    }
}

pub(crate) fn find<P: PayloadCodec, S: PageSource>(
    source: &S,
    root: u32,
    id_limit: u64,
    id: u32,
) -> Result<Option<Record>> {
    crate::work::structure_lookup(1);
    let Some(page_number) = locate_leaf::<P, S>(source, root, id_limit, id)? else {
        return Ok(None);
    };
    source.view_page(page_number, |page| read_record::<P, _>(page, id))
}

pub(crate) fn inspect<'a, P, S, T, F>(
    source: &'a S,
    root: u32,
    id_limit: u64,
    id: u32,
    inspect: F,
) -> Result<Option<T>>
where
    P: PayloadCodec,
    S: PageSource,
    F: FnOnce(ByteRange<S::Page<'a>>) -> Result<T>,
{
    crate::work::structure_lookup(1);
    let Some(page_number) = locate_leaf::<P, S>(source, root, id_limit, id)? else {
        return Ok(None);
    };
    source.view_page(page_number, |page| {
        let cell = leaf_cell::<P, _>(page, leaf_index::<P>(id))?;
        let stored_id = crate::contract::u32_le(cell, 4);
        if stored_id == 0 {
            if cell.all_zero(0, cell.len()) {
                return Ok(None);
            }
            return Err(Error::Corrupt("empty structure table slot is nonzero"));
        }
        codec::payload_source::<P, _>(cell, id)?;
        let payload = ByteRange::new(
            page,
            record_offset::<P>(id) + codec::PAYLOAD_OFFSET,
            P::PAYLOAD_SIZE,
        )
        .ok_or(Error::Corrupt("structure payload is outside its page"))?;
        inspect(payload).map(Some)
    })
}

pub(crate) fn insert<P: PayloadCodec, S: RetiringStore>(
    store: &mut S,
    root: &mut u32,
    id_limit: u64,
    record: &[u8],
) -> Result<()> {
    let decoded = codec::decode_record::<P, _>(record)?;
    if u64::from(decoded.id) >= id_limit {
        return Err(Error::Corrupt(
            "structure table insertion exceeds its limit",
        ));
    }
    let target_level = required_level::<P>(id_limit)?;
    let mut retired = RetiredPages::new();
    grow_root::<P, S>(store, root, target_level)?;
    if *root == 0 {
        *root = new_subtree::<P, S>(store, target_level, decoded.id, record)?;
        return Ok(());
    }

    let (private_root, mut header) = touch::<P, S>(store, *root, target_level, &mut retired)?;
    *root = private_root;
    let mut page_number = private_root;
    while header.level > 0 {
        let index = child_index::<P>(decoded.id, header.level)?;
        let child = store.inspect_page(page_number, |page| {
            branch_child::<P, _>(page, &header, index, store.page_limit())
        })?;
        if child == 0 {
            let child = new_subtree::<P, S>(store, header.level - 1, decoded.id, record)?;
            store.update_page(page_number, |page| {
                set_branch_child(page, &header, index, child)
            })?;
            store.retire_pages(retired.as_slice())?;
            return Ok(());
        }
        let (private_child, next_header) =
            touch::<P, S>(store, child, header.level - 1, &mut retired)?;
        if private_child != child {
            store.update_page(page_number, |page| {
                replace_branch_child(page, &header, index, private_child)
            })?;
        }
        page_number = private_child;
        header = next_header;
    }
    store.update_page(page_number, |page| {
        insert_leaf::<P, _>(page, &header, record)
    })?;
    store.retire_pages(retired.as_slice())
}

pub(crate) fn change_refcount<P: PayloadCodec, S: RetiringStore>(
    store: &mut S,
    root: &mut u32,
    id_limit: u64,
    id: u32,
    change: i64,
) -> Result<(Record, bool)> {
    if id == 0 || u64::from(id) >= id_limit || *root == 0 {
        return Err(Error::Corrupt("structure refcount names an absent ID"));
    }
    let expected_level = required_level::<P>(id_limit)?;
    let mut retired = RetiredPages::new();
    let (private_root, mut header) = touch::<P, S>(store, *root, expected_level, &mut retired)?;
    *root = private_root;
    let mut page_number = private_root;
    let mut path = Path::new();
    while header.level > 0 {
        let index = child_index::<P>(id, header.level)?;
        let child = store.inspect_page(page_number, |page| {
            branch_child::<P, _>(page, &header, index, store.page_limit())
        })?;
        if child == 0 {
            return Err(Error::Corrupt("structure refcount path is missing"));
        }
        path.push(Frame {
            page_number,
            child_index: index,
            item_count: header.item_count,
        })?;
        let (private_child, next_header) =
            touch::<P, S>(store, child, header.level - 1, &mut retired)?;
        if private_child != child {
            store.update_page(page_number, |page| {
                replace_branch_child(page, &header, index, private_child)
            })?;
        }
        page_number = private_child;
        header = next_header;
    }

    let record = store.inspect_page(page_number, |page| {
        read_record::<P, _>(page, id)?.ok_or(Error::Corrupt("structure refcount ID is missing"))
    })?;
    let next = changed_refcount(record.refcount, change)?;
    if next != 0 {
        store.update_page(page_number, |page| {
            page.put_u64(record_offset::<P>(id) + REFCOUNT_OFFSET, next)
        })?;
        store.retire_pages(retired.as_slice())?;
        return Ok((record, false));
    }

    store.update_page(page_number, |page| delete_leaf::<P, _>(page, &header, id))?;
    remove_empty_path(store, root, page_number, header.item_count - 1, &path)?;
    store.retire_pages(retired.as_slice())?;
    Ok((record, true))
}

pub(crate) fn shrink<P: PayloadCodec, S: RetiringStore>(
    store: &mut S,
    root: &mut u32,
    id_limit: u64,
) -> Result<()> {
    if *root == 0 {
        if id_limit != 1 {
            return Err(Error::Corrupt("empty structure table has a nonempty limit"));
        }
        return Ok(());
    }
    let wanted = required_level::<P>(id_limit)?;
    let mut retired = RetiredPages::new();
    loop {
        let header = store.inspect_page(*root, |page| {
            let header = parse::<P, _>(page, store.target_txn(), None)?;
            Ok(header)
        })?;
        if header.level == wanted {
            return store.retire_pages(retired.as_slice());
        }
        if header.level < wanted || header.item_count != 1 {
            return Err(Error::Corrupt("structure table root cannot shrink"));
        }
        let (private, private_header) = touch::<P, S>(store, *root, header.level, &mut retired)?;
        *root = private;
        let child = store.inspect_page(private, |page| {
            let child = branch_child::<P, _>(page, &private_header, 0, store.page_limit())?;
            for index in 1..BRANCH_CHILDREN {
                if raw_branch_child(page, index)? != 0 {
                    return Err(Error::Corrupt(
                        "structure table root has data above its new limit",
                    ));
                }
            }
            Ok(child)
        })?;
        let child = (child != 0).then_some(child).ok_or(Error::Corrupt(
            "structure table shrinking root has no first child",
        ))?;
        *root = child;
        store.discard_private(private)?;
    }
}

pub(crate) fn parse<P: PayloadCodec, S: ByteSource>(
    page: S,
    selected_txn: u64,
    expected_level: Option<u16>,
) -> Result<Header> {
    inspect_header::<P, _>(page, selected_txn, expected_level).map_err(|problem| match problem {
        HeaderProblem::Header => Error::Corrupt("structure table page header is invalid"),
        HeaderProblem::Born => Error::Corrupt("structure table page transaction is invalid"),
        HeaderProblem::Type => Error::Corrupt("structure table page type is invalid"),
        HeaderProblem::Level => Error::Corrupt("structure table page level is invalid"),
        HeaderProblem::Shape => Error::Corrupt("structure table page shape is invalid"),
    })
}

pub(crate) fn inspect_header<P: PayloadCodec, S: ByteSource>(
    page: S,
    selected_txn: u64,
    expected_level: Option<u16>,
) -> std::result::Result<Header, HeaderProblem> {
    if !page_header::common_valid(page) {
        return Err(HeaderProblem::Header);
    }
    if !page_header::born_valid(page, selected_txn) {
        return Err(HeaderProblem::Born);
    }
    let level = page_header::level(page);
    if level > MAX_LEVEL || expected_level.is_some_and(|expected| expected != level) {
        return Err(HeaderProblem::Level);
    }
    let page_type = if level == 0 {
        page_type::STRUCTURE_ID_LEAF
    } else {
        page_type::STRUCTURE_ID_BRANCH
    };
    if !page_header::kind_valid(page, page_type, P::KIND as u32) {
        return Err(HeaderProblem::Type);
    }
    let lower = if level == 0 {
        leaf_end::<P>()
    } else {
        BRANCH_END
    };
    if page_header::lower(page) != lower || page_header::upper(page) != PAGE_SIZE {
        return Err(HeaderProblem::Shape);
    }
    let item_count = page_header::item_count(page);
    let maximum = if level == 0 {
        leaf_slots::<P>()
    } else {
        BRANCH_CHILDREN
    };
    if item_count == 0 || item_count > maximum {
        return Err(HeaderProblem::Shape);
    }
    Ok(Header { level, item_count })
}

pub(crate) fn reserved_zero<P: PayloadCodec, S: ByteSource>(page: S, level: u16) -> bool {
    let lower = if level == 0 {
        leaf_end::<P>()
    } else {
        BRANCH_END
    };
    page.all_zero(lower, PAGE_SIZE - lower)
}

pub(crate) const fn branch_slots() -> usize {
    BRANCH_CHILDREN
}

pub(crate) const fn leaf_slots<P: PayloadCodec>() -> usize {
    (PAGE_SIZE - page_header::SIZE) / codec::record_size::<P>()
}

pub(crate) fn branch_child<P: PayloadCodec, S: ByteSource>(
    page: S,
    header: &Header,
    index: usize,
    page_limit: u64,
) -> Result<u32> {
    if header.level == 0 || index >= BRANCH_CHILDREN {
        return Err(Error::Corrupt("structure table child index is invalid"));
    }
    let child = raw_branch_child(page, index)?;
    if child != 0 && (child < 2 || u64::from(child) >= page_limit) {
        return Err(Error::Corrupt(
            "structure table child is outside page bounds",
        ));
    }
    Ok(child)
}

pub(crate) fn raw_branch_child<S: ByteSource>(page: S, index: usize) -> Result<u32> {
    if index >= BRANCH_CHILDREN {
        return Err(Error::Corrupt("structure table child index is invalid"));
    }
    Ok(crate::contract::u32_le(page, page_header::SIZE + index * 4))
}

pub(crate) fn leaf_cell<P: PayloadCodec, S: ByteSource>(
    page: S,
    slot: usize,
) -> Result<ByteRange<S>> {
    if slot >= leaf_slots::<P>() {
        return Err(Error::Corrupt("structure table record slot is invalid"));
    }
    ByteRange::new(
        page,
        page_header::SIZE + slot * codec::record_size::<P>(),
        codec::record_size::<P>(),
    )
    .ok_or(Error::Corrupt("structure table record is outside its page"))
}

pub(crate) fn required_level<P: PayloadCodec>(id_limit: u64) -> Result<u16> {
    if !(1..=1u64 << 32).contains(&id_limit) {
        return Err(Error::Corrupt("structure table ID limit is invalid"));
    }
    let mut level = 0u16;
    let mut span = leaf_slots::<P>() as u64;
    while id_limit > span {
        level += 1;
        if level > MAX_LEVEL {
            return Err(Error::Corrupt("structure table exceeds its maximum height"));
        }
        span = span
            .checked_mul(BRANCH_CHILDREN as u64)
            .ok_or(Error::ArithmeticOverflow("structure table coverage"))?;
    }
    Ok(level)
}

fn grow_root<P: PayloadCodec, S: Store>(store: &mut S, root: &mut u32, wanted: u16) -> Result<()> {
    if *root == 0 {
        return Ok(());
    }
    let mut level = store.inspect_page(*root, |page| {
        Ok(parse::<P, _>(page, store.target_txn(), None)?.level)
    })?;
    if level > wanted {
        return Err(Error::Corrupt(
            "structure table root level exceeds its limit",
        ));
    }
    while level < wanted {
        let next = store.allocate()?;
        let target_txn = store.target_txn();
        store.update_page(next, |page| {
            initialize::<P, _>(page, target_txn, level + 1, 1)?;
            page.put_u32(page_header::SIZE, *root)
        })?;
        *root = next;
        level += 1;
    }
    Ok(())
}

fn new_subtree<P: PayloadCodec, S: Store>(
    store: &mut S,
    level: u16,
    id: u32,
    record: &[u8],
) -> Result<u32> {
    let page_number = store.allocate()?;
    let target_txn = store.target_txn();
    if level == 0 {
        store.update_page(page_number, |page| {
            initialize::<P, _>(page, target_txn, 0, 1)?;
            page.write(record_offset::<P>(id), record)
        })?;
        return Ok(page_number);
    }
    let child = new_subtree::<P, S>(store, level - 1, id, record)?;
    let index = child_index::<P>(id, level)?;
    store.update_page(page_number, |page| {
        initialize::<P, _>(page, target_txn, level, 1)?;
        page.put_u32(page_header::SIZE + index * 4, child)
    })?;
    Ok(page_number)
}

fn touch<P: PayloadCodec, S: Store>(
    store: &mut S,
    page_number: u32,
    level: u16,
    retired: &mut RetiredPages,
) -> Result<(u32, Header)> {
    let (header, private) = store.inspect_page(page_number, |page| {
        let header = parse::<P, _>(page, store.target_txn(), Some(level))?;
        Ok((header, page_header::born_txn(page) == store.target_txn()))
    })?;
    if private {
        return Ok((page_number, header));
    }
    let copy = store.allocate()?;
    store.copy_for_cow(page_number, copy)?;
    retired.push(page_number)?;
    Ok((copy, header))
}

fn insert_leaf<P: PayloadCodec, D: PageEdit>(
    page: &mut D,
    header: &Header,
    record: &[u8],
) -> Result<()> {
    let decoded = codec::decode_record::<P, _>(record)?;
    let at = record_offset::<P>(decoded.id);
    let current = leaf_cell::<P, _>(page.view(), leaf_index::<P>(decoded.id))?;
    if !current.all_zero(0, current.len()) {
        return Err(Error::Corrupt("structure table ID already exists"));
    }
    page.write(at, record)?;
    page.put_u16(page_header::ITEM_COUNT, (header.item_count + 1) as u16)
}

fn delete_leaf<P: PayloadCodec, D: PageEdit>(page: &mut D, header: &Header, id: u32) -> Result<()> {
    page.zero(record_offset::<P>(id), codec::record_size::<P>())?;
    page.put_u16(page_header::ITEM_COUNT, (header.item_count - 1) as u16)
}

fn remove_empty_path<S: Store>(
    store: &mut S,
    root: &mut u32,
    mut child: u32,
    mut child_count: usize,
    path: &Path,
) -> Result<()> {
    if child_count != 0 {
        return Ok(());
    }
    store.discard_private(child)?;
    for depth in (0..path.depth).rev() {
        let frame = path.frames[depth];
        store.update_page(frame.page_number, |page| {
            page.put_u32(page_header::SIZE + frame.child_index * 4, 0)?;
            page.put_u16(page_header::ITEM_COUNT, (frame.item_count - 1) as u16)
        })?;
        child = frame.page_number;
        child_count = frame.item_count - 1;
        if child_count != 0 {
            return Ok(());
        }
        store.discard_private(child)?;
    }
    *root = 0;
    Ok(())
}

fn read_record<P: PayloadCodec, S: ByteSource>(page: S, id: u32) -> Result<Option<Record>> {
    let cell = leaf_cell::<P, _>(page, leaf_index::<P>(id))?;
    let stored_id = crate::contract::u32_le(cell, 4);
    if stored_id == 0 {
        if cell.all_zero(0, cell.len()) {
            return Ok(None);
        }
        return Err(Error::Corrupt("empty structure table slot is nonzero"));
    }
    let record = codec::decode_record::<P, _>(cell)?;
    if record.id != id {
        return Err(Error::Corrupt(
            "structure table record is in the wrong slot",
        ));
    }
    Ok(Some(record))
}

fn locate_leaf<P: PayloadCodec, S: PageSource>(
    source: &S,
    root: u32,
    id_limit: u64,
    id: u32,
) -> Result<Option<u32>> {
    if id == 0 || u64::from(id) >= id_limit || root == 0 {
        return Ok(None);
    }
    require_root(root, source.selected_page_limit())?;
    let mut page_number = root;
    let mut level = required_level::<P>(id_limit)?;
    while level > 0 {
        let child = source.view_page(page_number, |page| {
            let header = parse::<P, _>(page, source.selected_txn(), Some(level))?;
            let index = child_index::<P>(id, level)?;
            branch_child::<P, _>(page, &header, index, source.selected_page_limit())
        })?;
        if child == 0 {
            return Ok(None);
        }
        page_number = child;
        level -= 1;
        crate::work::tree_descent(1);
    }
    Ok(Some(page_number))
}

fn replace_branch_child<D: PageEdit>(
    page: &mut D,
    header: &Header,
    index: usize,
    child: u32,
) -> Result<()> {
    if header.level == 0 || index >= BRANCH_CHILDREN || child == 0 {
        return Err(Error::Corrupt(
            "structure table child replacement is invalid",
        ));
    }
    page.put_u32(page_header::SIZE + index * 4, child)
}

fn set_branch_child<D: PageEdit>(
    page: &mut D,
    header: &Header,
    index: usize,
    child: u32,
) -> Result<()> {
    if header.level == 0 || index >= BRANCH_CHILDREN || child == 0 {
        return Err(Error::Corrupt("structure table child insertion is invalid"));
    }
    let at = page_header::SIZE + index * 4;
    if crate::contract::u32_le(page.view(), at) != 0 {
        return Err(Error::Corrupt("structure table child already exists"));
    }
    page.put_u32(at, child)?;
    page.put_u16(page_header::ITEM_COUNT, (header.item_count + 1) as u16)
}

fn initialize<P: PayloadCodec, D: PageSink>(
    page: &mut D,
    txn: u64,
    level: u16,
    item_count: usize,
) -> Result<()> {
    page_header::initialize(
        page,
        page_header::Fields {
            page_type: if level == 0 {
                page_type::STRUCTURE_ID_LEAF
            } else {
                page_type::STRUCTURE_ID_BRANCH
            },
            born_txn: txn,
            item_count: item_count as u16,
            level,
            lower: if level == 0 {
                leaf_end::<P>() as u16
            } else {
                BRANCH_END as u16
            },
            upper: PAGE_SIZE as u16,
            aux: P::KIND as u32,
        },
    )
}

fn child_index<P: PayloadCodec>(id: u32, level: u16) -> Result<usize> {
    if level == 0 || level > MAX_LEVEL {
        return Err(Error::Corrupt("structure table branch level is invalid"));
    }
    let span = coverage::<P>(level - 1)?;
    Ok(((u64::from(id) / span) % BRANCH_CHILDREN as u64) as usize)
}

pub(crate) fn coverage<P: PayloadCodec>(level: u16) -> Result<u64> {
    let mut span = leaf_slots::<P>() as u64;
    for _ in 0..level {
        span = span
            .checked_mul(BRANCH_CHILDREN as u64)
            .ok_or(Error::ArithmeticOverflow("structure table coverage"))?;
    }
    Ok(span)
}

const fn leaf_end<P: PayloadCodec>() -> usize {
    page_header::SIZE + leaf_slots::<P>() * codec::record_size::<P>()
}

const fn leaf_index<P: PayloadCodec>(id: u32) -> usize {
    id as usize % leaf_slots::<P>()
}

const fn record_offset<P: PayloadCodec>(id: u32) -> usize {
    page_header::SIZE + leaf_index::<P>(id) * codec::record_size::<P>()
}

fn require_root(root: u32, page_limit: u64) -> Result<()> {
    if root < 2 || u64::from(root) >= page_limit {
        Err(Error::Corrupt(
            "structure table root is outside page bounds",
        ))
    } else {
        Ok(())
    }
}

fn changed_refcount(current: u64, change: i64) -> Result<u64> {
    if change >= 0 {
        current
            .checked_add(change as u64)
            .ok_or(Error::ArithmeticOverflow("structure refcount"))
    } else {
        current
            .checked_sub(change.unsigned_abs())
            .ok_or(Error::ArithmeticOverflow("structure refcount"))
    }
}

const _: () =
    assert!(page_header::SIZE + codec::PAYLOAD_OFFSET + codec::MAX_PAYLOAD_SIZE <= PAGE_SIZE);
const _: () = assert!(BRANCH_END <= PAGE_SIZE);

#[cfg(test)]
mod tests {
    use super::*;
    use crate::structured_value::NetworkEnrichmentV1Codec;

    #[test]
    fn v1_radix_levels_cover_the_complete_u32_namespace() {
        assert_eq!(leaf_slots::<NetworkEnrichmentV1Codec>(), 50);
        assert_eq!(required_level::<NetworkEnrichmentV1Codec>(1).unwrap(), 0);
        assert_eq!(required_level::<NetworkEnrichmentV1Codec>(50).unwrap(), 0);
        assert_eq!(required_level::<NetworkEnrichmentV1Codec>(51).unwrap(), 1);
        assert_eq!(
            required_level::<NetworkEnrichmentV1Codec>(25_600).unwrap(),
            1
        );
        assert_eq!(
            required_level::<NetworkEnrichmentV1Codec>(25_601).unwrap(),
            2
        );
        assert_eq!(
            required_level::<NetworkEnrichmentV1Codec>(13_107_200).unwrap(),
            2
        );
        assert_eq!(
            required_level::<NetworkEnrichmentV1Codec>(13_107_201).unwrap(),
            3
        );
        assert_eq!(
            required_level::<NetworkEnrichmentV1Codec>(1u64 << 32).unwrap(),
            3
        );
    }
}
