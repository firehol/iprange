//! Exact v4 range-leaf and range-branch views.

use core::marker::PhantomData;

use crate::contract::{
    u32_le, u64_le, AddressFamily, ValueKind, MAX_PAGE_COUNT, MAX_TREE_LEVEL, PAGE_SIZE,
};
use crate::key::IpKey;
use crate::page::{write_crc32c, PageHeader, PageHeaderError, PageType, PAGE_HEADER_SIZE};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum RangePageError {
    Header(PageHeaderError),
    WrongKeyFamily,
    WrongPageType(PageType),
    WrongAux(u32),
    FixedGeometry,
    EmptyBranch,
    IndexOutOfBounds,
    ChildOutOfBounds(u32),
    ReservedNonzero,
    EmptySummaryNonzero,
    SummaryOrder,
    RangeReversed,
    MembershipValueZero,
}

impl From<PageHeaderError> for RangePageError {
    fn from(value: PageHeaderError) -> Self {
        Self::Header(value)
    }
}

/// Input rejected before the range-page encoder changes its destination page.
///
/// This is deliberately separate from [`RangePageError`]: decoder errors
/// describe bytes already on disk, while these errors describe an attempted
/// private COW page that was never written.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum RangePageWriteError {
    BornTransactionZero,
    TooManyRecords { required: usize, actual: usize },
    RangeReversed,
    MembershipValueZero,
    RangeOverlap,
    AdjacentEqualValue,
    BranchLevel { level: u16 },
    PageCount { page_count: u64 },
    EmptyBranch,
    TooManyChildren { required: usize, actual: usize },
    FirstFence,
    FenceBounds,
    FenceOrder,
    ChildOutOfBounds(u32),
    DuplicateChild(u32),
    EmptySummaryNonzero,
    SummaryOrder,
    SummaryBeforeFence,
    SummaryOutsideFence,
    SummaryOverlap,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct RangeRecord<K: IpKey> {
    pub(crate) from: K,
    pub(crate) to: K,
    pub(crate) value: u32,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct RangeBranchEntry<K: IpKey> {
    pub(crate) lower_fence: K,
    pub(crate) child_pgno: u32,
    pub(crate) subtree_record_count: u64,
    pub(crate) first_from: K,
    pub(crate) last_from: K,
    pub(crate) last_to: K,
}

impl<K: IpKey> RangeBranchEntry<K> {
    #[inline]
    pub(crate) const fn is_empty(self) -> bool {
        self.subtree_record_count == 0
    }
}

/// Maximum complete records that fit in one exact range leaf.
#[inline]
pub(crate) const fn leaf_capacity<K: IpKey>() -> usize {
    (PAGE_SIZE - PAGE_HEADER_SIZE as usize) / record_size::<K>()
}

/// Maximum complete entries that fit in one exact range branch.
#[inline]
pub(crate) const fn branch_capacity<K: IpKey>() -> usize {
    (PAGE_SIZE - PAGE_HEADER_SIZE as usize) / branch_entry_size::<K>()
}

/// Encodes one canonical range leaf and seals it with CRC-32C.
///
/// The whole input is checked before the destination is changed. This makes a
/// capacity or canonicality failure safe for a private COW slot to retry.
pub(crate) fn encode_leaf<K: IpKey>(
    page: &mut [u8; PAGE_SIZE],
    born_txn: u64,
    value_kind: ValueKind,
    records: &[RangeRecord<K>],
) -> Result<(), RangePageWriteError> {
    if born_txn == 0 {
        return Err(RangePageWriteError::BornTransactionZero);
    }
    let actual = leaf_capacity::<K>();
    if records.len() > actual {
        return Err(RangePageWriteError::TooManyRecords {
            required: records.len(),
            actual,
        });
    }
    for (index, record) in records.iter().enumerate() {
        if record.from > record.to {
            return Err(RangePageWriteError::RangeReversed);
        }
        if value_kind == ValueKind::Membership && record.value == 0 {
            return Err(RangePageWriteError::MembershipValueZero);
        }
        if index == 0 {
            continue;
        }
        let previous = records[index - 1];
        if previous.to >= record.from {
            return Err(RangePageWriteError::RangeOverlap);
        }
        if previous.value == record.value && previous.to.checked_inc() == Some(record.from) {
            return Err(RangePageWriteError::AdjacentEqualValue);
        }
    }

    page.fill(0);
    let lower = usize::from(PAGE_HEADER_SIZE) + records.len() * record_size::<K>();
    PageHeader {
        page_type: PageType::RangeLeaf,
        born_txn,
        item_count: records.len() as u16,
        level: 0,
        lower: lower as u16,
        upper: PAGE_SIZE as u16,
        aux: K::FAMILY as u32,
        page_crc32c: 0,
    }
    .encode_into(page);
    for (index, record) in records.iter().enumerate() {
        let at = usize::from(PAGE_HEADER_SIZE) + index * record_size::<K>();
        record.from.write_le(&mut page[at..at + K::WIDTH]);
        record
            .to
            .write_le(&mut page[at + K::WIDTH..at + 2 * K::WIDTH]);
        page[at + 2 * K::WIDTH..at + record_size::<K>()]
            .copy_from_slice(&record.value.to_le_bytes());
    }
    write_crc32c(page);
    Ok(())
}

/// Encodes one canonical range branch and seals it with CRC-32C.
///
/// `lower_fence` is the exact lower bound inherited from the parent. Root
/// callers pass `K::MIN`. `upper_fence` is the exclusive upper bound inherited
/// from the parent; `None` represents the unrepresentable endpoint just past
/// the family maximum.
pub(crate) fn encode_branch<K: IpKey>(
    page: &mut [u8; PAGE_SIZE],
    born_txn: u64,
    level: u16,
    page_count: u64,
    lower_fence: K,
    upper_fence: Option<K>,
    entries: &[RangeBranchEntry<K>],
) -> Result<(), RangePageWriteError> {
    if born_txn == 0 {
        return Err(RangePageWriteError::BornTransactionZero);
    }
    if level == 0 || level > MAX_TREE_LEVEL {
        return Err(RangePageWriteError::BranchLevel { level });
    }
    if !(3..=MAX_PAGE_COUNT).contains(&page_count) {
        return Err(RangePageWriteError::PageCount { page_count });
    }
    if entries.is_empty() {
        return Err(RangePageWriteError::EmptyBranch);
    }
    let actual = branch_capacity::<K>();
    if entries.len() > actual {
        return Err(RangePageWriteError::TooManyChildren {
            required: entries.len(),
            actual,
        });
    }
    if entries[0].lower_fence != lower_fence {
        return Err(RangePageWriteError::FirstFence);
    }
    if upper_fence.is_some_and(|upper| lower_fence >= upper) {
        return Err(RangePageWriteError::FenceBounds);
    }

    let mut previous_nonempty: Option<RangeBranchEntry<K>> = None;
    for (index, entry) in entries.iter().copied().enumerate() {
        if upper_fence.is_some_and(|upper| entry.lower_fence >= upper) {
            return Err(RangePageWriteError::FenceBounds);
        }
        if entry.child_pgno < 2 || u64::from(entry.child_pgno) >= page_count {
            return Err(RangePageWriteError::ChildOutOfBounds(entry.child_pgno));
        }
        if entries[..index]
            .iter()
            .any(|prior| prior.child_pgno == entry.child_pgno)
        {
            return Err(RangePageWriteError::DuplicateChild(entry.child_pgno));
        }
        if index != 0 && entries[index - 1].lower_fence >= entry.lower_fence {
            return Err(RangePageWriteError::FenceOrder);
        }
        if entry.is_empty() {
            if entry.first_from != K::MIN || entry.last_from != K::MIN || entry.last_to != K::MIN {
                return Err(RangePageWriteError::EmptySummaryNonzero);
            }
            continue;
        }
        if entry.first_from < entry.lower_fence {
            return Err(RangePageWriteError::SummaryBeforeFence);
        }
        if entry.first_from > entry.last_from || entry.last_from > entry.last_to {
            return Err(RangePageWriteError::SummaryOrder);
        }
        let next_fence = entries
            .get(index + 1)
            .map(|next| next.lower_fence)
            .or(upper_fence);
        if next_fence.is_some_and(|fence| entry.last_from >= fence) {
            return Err(RangePageWriteError::SummaryOutsideFence);
        }
        if let Some(previous) = previous_nonempty {
            if previous.last_to >= entry.first_from {
                return Err(RangePageWriteError::SummaryOverlap);
            }
        }
        previous_nonempty = Some(entry);
    }

    page.fill(0);
    let lower = usize::from(PAGE_HEADER_SIZE) + entries.len() * branch_entry_size::<K>();
    PageHeader {
        page_type: PageType::RangeBranch,
        born_txn,
        item_count: entries.len() as u16,
        level,
        lower: lower as u16,
        upper: PAGE_SIZE as u16,
        aux: K::FAMILY as u32,
        page_crc32c: 0,
    }
    .encode_into(page);
    for (index, entry) in entries.iter().enumerate() {
        let at = usize::from(PAGE_HEADER_SIZE) + index * branch_entry_size::<K>();
        if K::WIDTH == 4 {
            entry.lower_fence.write_le(&mut page[at..at + 4]);
            page[at + 4..at + 8].copy_from_slice(&entry.child_pgno.to_le_bytes());
            page[at + 8..at + 16].copy_from_slice(&entry.subtree_record_count.to_le_bytes());
            entry.first_from.write_le(&mut page[at + 16..at + 20]);
            entry.last_from.write_le(&mut page[at + 20..at + 24]);
            entry.last_to.write_le(&mut page[at + 24..at + 28]);
        } else {
            entry.lower_fence.write_le(&mut page[at..at + 16]);
            page[at + 16..at + 20].copy_from_slice(&entry.child_pgno.to_le_bytes());
            page[at + 24..at + 32].copy_from_slice(&entry.subtree_record_count.to_le_bytes());
            entry.first_from.write_le(&mut page[at + 32..at + 48]);
            entry.last_from.write_le(&mut page[at + 48..at + 64]);
            entry.last_to.write_le(&mut page[at + 64..at + 80]);
        }
    }
    write_crc32c(page);
    Ok(())
}

#[derive(Clone, Copy, Debug)]
pub(crate) struct RangeLeaf<'a, K: IpKey> {
    page: &'a [u8; PAGE_SIZE],
    count: usize,
    value_kind: ValueKind,
    _key: PhantomData<K>,
}

impl<'a, K: IpKey> RangeLeaf<'a, K> {
    pub(crate) fn open(
        page: &'a [u8; PAGE_SIZE],
        selected_txn: u64,
        family: AddressFamily,
        value_kind: ValueKind,
    ) -> Result<Self, RangePageError> {
        require_key_family::<K>(family)?;
        let header = PageHeader::decode(page, selected_txn)?;
        if header.page_type != PageType::RangeLeaf {
            return Err(RangePageError::WrongPageType(header.page_type));
        }
        if header.aux != family as u32 {
            return Err(RangePageError::WrongAux(header.aux));
        }
        let count = usize::from(header.item_count);
        let lower = usize::from(PAGE_HEADER_SIZE)
            .checked_add(
                count
                    .checked_mul(record_size::<K>())
                    .ok_or(RangePageError::FixedGeometry)?,
            )
            .ok_or(RangePageError::FixedGeometry)?;
        if lower != usize::from(header.lower) || usize::from(header.upper) != PAGE_SIZE {
            return Err(RangePageError::FixedGeometry);
        }
        Ok(Self {
            page,
            count,
            value_kind,
            _key: PhantomData,
        })
    }

    #[inline]
    pub(crate) const fn len(self) -> usize {
        self.count
    }

    #[inline]
    pub(crate) const fn is_empty(self) -> bool {
        self.count == 0
    }

    pub(crate) fn record(self, index: usize) -> Result<RangeRecord<K>, RangePageError> {
        if index >= self.count {
            return Err(RangePageError::IndexOutOfBounds);
        }
        let offset = usize::from(PAGE_HEADER_SIZE) + index * record_size::<K>();
        let from = K::read_le(&self.page[offset..offset + K::WIDTH]);
        let to = K::read_le(&self.page[offset + K::WIDTH..offset + 2 * K::WIDTH]);
        let value = u32_le(self.page, offset + 2 * K::WIDTH);
        if from > to {
            return Err(RangePageError::RangeReversed);
        }
        if self.value_kind == ValueKind::Membership && value == 0 {
            return Err(RangePageError::MembershipValueZero);
        }
        Ok(RangeRecord { from, to, value })
    }
}

#[derive(Clone, Copy, Debug)]
pub(crate) struct RangeBranch<'a, K: IpKey> {
    page: &'a [u8; PAGE_SIZE],
    count: usize,
    pub(crate) level: u16,
    page_count: u64,
    _key: PhantomData<K>,
}

impl<'a, K: IpKey> RangeBranch<'a, K> {
    pub(crate) fn open(
        page: &'a [u8; PAGE_SIZE],
        selected_txn: u64,
        family: AddressFamily,
        page_count: u64,
    ) -> Result<Self, RangePageError> {
        require_key_family::<K>(family)?;
        let header = PageHeader::decode(page, selected_txn)?;
        if header.page_type != PageType::RangeBranch {
            return Err(RangePageError::WrongPageType(header.page_type));
        }
        if header.aux != family as u32 {
            return Err(RangePageError::WrongAux(header.aux));
        }
        let count = usize::from(header.item_count);
        if count == 0 {
            return Err(RangePageError::EmptyBranch);
        }
        let lower = usize::from(PAGE_HEADER_SIZE)
            .checked_add(
                count
                    .checked_mul(branch_entry_size::<K>())
                    .ok_or(RangePageError::FixedGeometry)?,
            )
            .ok_or(RangePageError::FixedGeometry)?;
        if lower != usize::from(header.lower) || usize::from(header.upper) != PAGE_SIZE {
            return Err(RangePageError::FixedGeometry);
        }
        Ok(Self {
            page,
            count,
            level: header.level,
            page_count,
            _key: PhantomData,
        })
    }

    #[inline]
    pub(crate) const fn len(self) -> usize {
        self.count
    }

    pub(crate) fn entry(self, index: usize) -> Result<RangeBranchEntry<K>, RangePageError> {
        if index >= self.count {
            return Err(RangePageError::IndexOutOfBounds);
        }
        let offset = usize::from(PAGE_HEADER_SIZE) + index * branch_entry_size::<K>();
        let entry = if K::WIDTH == 4 {
            if u32_le(self.page, offset + 28) != 0 {
                return Err(RangePageError::ReservedNonzero);
            }
            RangeBranchEntry {
                lower_fence: K::read_le(&self.page[offset..offset + 4]),
                child_pgno: u32_le(self.page, offset + 4),
                subtree_record_count: u64_le(self.page, offset + 8),
                first_from: K::read_le(&self.page[offset + 16..offset + 20]),
                last_from: K::read_le(&self.page[offset + 20..offset + 24]),
                last_to: K::read_le(&self.page[offset + 24..offset + 28]),
            }
        } else {
            if u32_le(self.page, offset + 20) != 0 {
                return Err(RangePageError::ReservedNonzero);
            }
            RangeBranchEntry {
                lower_fence: K::read_le(&self.page[offset..offset + 16]),
                child_pgno: u32_le(self.page, offset + 16),
                subtree_record_count: u64_le(self.page, offset + 24),
                first_from: K::read_le(&self.page[offset + 32..offset + 48]),
                last_from: K::read_le(&self.page[offset + 48..offset + 64]),
                last_to: K::read_le(&self.page[offset + 64..offset + 80]),
            }
        };
        if entry.child_pgno < 2 || u64::from(entry.child_pgno) >= self.page_count {
            return Err(RangePageError::ChildOutOfBounds(entry.child_pgno));
        }
        if entry.is_empty() {
            if entry.first_from != K::MIN || entry.last_from != K::MIN || entry.last_to != K::MIN {
                return Err(RangePageError::EmptySummaryNonzero);
            }
        } else if entry.first_from > entry.last_from || entry.last_from > entry.last_to {
            return Err(RangePageError::SummaryOrder);
        }
        Ok(entry)
    }

    pub(crate) fn first_nonempty(self) -> Result<Option<usize>, RangePageError> {
        self.next_nonempty(0)
    }

    pub(crate) fn next_nonempty(self, from: usize) -> Result<Option<usize>, RangePageError> {
        for index in from..self.count {
            if !self.entry(index)?.is_empty() {
                return Ok(Some(index));
            }
        }
        Ok(None)
    }

    pub(crate) fn previous_nonempty(self, before: usize) -> Result<Option<usize>, RangePageError> {
        let mut index = core::cmp::min(before, self.count);
        while index != 0 {
            index -= 1;
            if !self.entry(index)?.is_empty() {
                return Ok(Some(index));
            }
        }
        Ok(None)
    }

    pub(crate) fn predecessor_for(self, target: K) -> Result<Option<usize>, RangePageError> {
        let mut result = None;
        for index in 0..self.count {
            let entry = self.entry(index)?;
            if !entry.is_empty() && entry.first_from <= target {
                result = Some(index);
            }
        }
        Ok(result)
    }
}

#[inline]
pub(crate) const fn record_size<K: IpKey>() -> usize {
    2 * K::WIDTH + 4
}

#[inline]
pub(crate) const fn branch_entry_size<K: IpKey>() -> usize {
    if K::WIDTH == 4 {
        32
    } else {
        80
    }
}

fn require_key_family<K: IpKey>(family: AddressFamily) -> Result<(), RangePageError> {
    if K::FAMILY != family || !matches!(K::WIDTH, 4 | 16) {
        return Err(RangePageError::WrongKeyFamily);
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::key::{Ipv4Key, Ipv6Key};
    use crate::page::{write_crc32c, PageHeader};

    fn header(page_type: PageType, count: u16, level: u16, lower: u16, aux: u32) -> PageHeader {
        PageHeader {
            page_type,
            born_txn: 3,
            item_count: count,
            level,
            lower,
            upper: PAGE_SIZE as u16,
            aux,
            page_crc32c: 0,
        }
    }

    fn empty_leaf<K: IpKey>() -> [u8; PAGE_SIZE] {
        let mut page = [0u8; PAGE_SIZE];
        header(
            PageType::RangeLeaf,
            0,
            0,
            PAGE_HEADER_SIZE,
            K::FAMILY as u32,
        )
        .encode_into(&mut page);
        write_crc32c(&mut page);
        page
    }

    #[test]
    fn legal_empty_leaves_open_for_both_families() {
        let v4 = empty_leaf::<Ipv4Key>();
        let v6 = empty_leaf::<Ipv6Key>();
        assert!(
            RangeLeaf::<Ipv4Key>::open(&v4, 3, AddressFamily::Ipv4, ValueKind::Direct)
                .unwrap()
                .is_empty()
        );
        assert!(
            RangeLeaf::<Ipv6Key>::open(&v6, 3, AddressFamily::Ipv6, ValueKind::Membership)
                .unwrap()
                .is_empty()
        );
    }

    #[test]
    fn leaf_geometry_and_record_semantics_are_checked() {
        let mut page = [0u8; PAGE_SIZE];
        header(PageType::RangeLeaf, 1, 0, 44, 4).encode_into(&mut page);
        page[32..36].copy_from_slice(&10u32.to_le_bytes());
        page[36..40].copy_from_slice(&20u32.to_le_bytes());
        page[40..44].copy_from_slice(&7u32.to_le_bytes());
        let leaf = RangeLeaf::<Ipv4Key>::open(&page, 3, AddressFamily::Ipv4, ValueKind::Membership)
            .unwrap();
        assert_eq!(
            leaf.record(0).unwrap(),
            RangeRecord {
                from: Ipv4Key(10),
                to: Ipv4Key(20),
                value: 7,
            }
        );

        page[40..44].fill(0);
        let leaf = RangeLeaf::<Ipv4Key>::open(&page, 3, AddressFamily::Ipv4, ValueKind::Membership)
            .unwrap();
        assert_eq!(leaf.record(0), Err(RangePageError::MembershipValueZero));

        page[32..36].copy_from_slice(&21u32.to_le_bytes());
        page[36..40].copy_from_slice(&20u32.to_le_bytes());
        let leaf =
            RangeLeaf::<Ipv4Key>::open(&page, 3, AddressFamily::Ipv4, ValueKind::Direct).unwrap();
        assert_eq!(leaf.record(0), Err(RangePageError::RangeReversed));

        page[20..22].copy_from_slice(&43u16.to_le_bytes());
        assert!(matches!(
            RangeLeaf::<Ipv4Key>::open(&page, 3, AddressFamily::Ipv4, ValueKind::Direct),
            Err(RangePageError::FixedGeometry)
        ));
    }

    fn ipv4_branch(entries: &[(u32, u32, u64, u32, u32, u32)]) -> [u8; PAGE_SIZE] {
        let mut page = [0u8; PAGE_SIZE];
        let lower = usize::from(PAGE_HEADER_SIZE) + entries.len() * 32;
        header(
            PageType::RangeBranch,
            entries.len() as u16,
            1,
            lower as u16,
            4,
        )
        .encode_into(&mut page);
        for (index, &(fence, child, count, first, last, to)) in entries.iter().enumerate() {
            let at = usize::from(PAGE_HEADER_SIZE) + index * 32;
            page[at..at + 4].copy_from_slice(&fence.to_le_bytes());
            page[at + 4..at + 8].copy_from_slice(&child.to_le_bytes());
            page[at + 8..at + 16].copy_from_slice(&count.to_le_bytes());
            page[at + 16..at + 20].copy_from_slice(&first.to_le_bytes());
            page[at + 20..at + 24].copy_from_slice(&last.to_le_bytes());
            page[at + 24..at + 28].copy_from_slice(&to.to_le_bytes());
        }
        page
    }

    #[test]
    fn branch_navigation_skips_empty_children_in_bounded_page_scan() {
        let page = ipv4_branch(&[
            (0, 3, 0, 0, 0, 0),
            (100, 4, 2, 120, 150, 160),
            (200, 5, 0, 0, 0, 0),
            (300, 6, 1, 310, 310, 320),
        ]);
        let branch = RangeBranch::<Ipv4Key>::open(&page, 3, AddressFamily::Ipv4, 7).unwrap();
        assert_eq!(branch.len(), 4);
        assert_eq!(branch.first_nonempty().unwrap(), Some(1));
        assert_eq!(branch.next_nonempty(2).unwrap(), Some(3));
        assert_eq!(branch.previous_nonempty(3).unwrap(), Some(1));
        assert_eq!(branch.predecessor_for(Ipv4Key(309)).unwrap(), Some(1));
        assert_eq!(branch.predecessor_for(Ipv4Key(310)).unwrap(), Some(3));
    }

    #[test]
    fn all_empty_branch_is_legal_but_zero_child_branch_is_not() {
        let page = ipv4_branch(&[(0, 3, 0, 0, 0, 0), (100, 4, 0, 0, 0, 0)]);
        let branch = RangeBranch::<Ipv4Key>::open(&page, 3, AddressFamily::Ipv4, 5).unwrap();
        assert_eq!(branch.first_nonempty().unwrap(), None);

        let mut zero = [0u8; PAGE_SIZE];
        header(PageType::RangeBranch, 0, 1, PAGE_HEADER_SIZE, 4).encode_into(&mut zero);
        assert!(matches!(
            RangeBranch::<Ipv4Key>::open(&zero, 3, AddressFamily::Ipv4, 5),
            Err(RangePageError::EmptyBranch)
        ));
    }

    #[test]
    fn branch_rejects_bad_child_reserved_and_summaries() {
        let mut page = ipv4_branch(&[(0, 1, 1, 10, 10, 20)]);
        let branch = RangeBranch::<Ipv4Key>::open(&page, 3, AddressFamily::Ipv4, 5).unwrap();
        assert_eq!(branch.entry(0), Err(RangePageError::ChildOutOfBounds(1)));

        page = ipv4_branch(&[(0, 3, 0, 1, 0, 0)]);
        let branch = RangeBranch::<Ipv4Key>::open(&page, 3, AddressFamily::Ipv4, 5).unwrap();
        assert_eq!(branch.entry(0), Err(RangePageError::EmptySummaryNonzero));

        page = ipv4_branch(&[(0, 3, 1, 20, 10, 30)]);
        let branch = RangeBranch::<Ipv4Key>::open(&page, 3, AddressFamily::Ipv4, 5).unwrap();
        assert_eq!(branch.entry(0), Err(RangePageError::SummaryOrder));

        page = ipv4_branch(&[(0, 3, 1, 10, 10, 20)]);
        page[60] = 1;
        let branch = RangeBranch::<Ipv4Key>::open(&page, 3, AddressFamily::Ipv4, 5).unwrap();
        assert_eq!(branch.entry(0), Err(RangePageError::ReservedNonzero));
    }

    #[test]
    fn ipv6_branch_entry_uses_exact_eighty_byte_layout() {
        let mut page = [0u8; PAGE_SIZE];
        header(PageType::RangeBranch, 1, 2, 112, 6).encode_into(&mut page);
        let fence = Ipv6Key { hi: 1, lo: 2 };
        let first = Ipv6Key { hi: 3, lo: 4 };
        let last = Ipv6Key { hi: 5, lo: 6 };
        let to = Ipv6Key { hi: 5, lo: 9 };
        fence.write_le(&mut page[32..48]);
        page[48..52].copy_from_slice(&3u32.to_le_bytes());
        page[56..64].copy_from_slice(&7u64.to_le_bytes());
        first.write_le(&mut page[64..80]);
        last.write_le(&mut page[80..96]);
        to.write_le(&mut page[96..112]);

        let branch = RangeBranch::<Ipv6Key>::open(&page, 3, AddressFamily::Ipv6, 4).unwrap();
        assert_eq!(
            branch.entry(0).unwrap(),
            RangeBranchEntry {
                lower_fence: fence,
                child_pgno: 3,
                subtree_record_count: 7,
                first_from: first,
                last_from: last,
                last_to: to,
            }
        );
    }

    #[test]
    fn leaf_encoder_is_canonical_atomic_and_round_trips() {
        assert_eq!(leaf_capacity::<Ipv4Key>(), 338);
        assert_eq!(leaf_capacity::<Ipv6Key>(), 112);

        let records = [
            RangeRecord {
                from: Ipv4Key(10),
                to: Ipv4Key(20),
                value: 0,
            },
            RangeRecord {
                from: Ipv4Key(21),
                to: Ipv4Key(30),
                value: 7,
            },
        ];
        let mut page = [0xa5; PAGE_SIZE];
        encode_leaf(&mut page, 7, ValueKind::Direct, &records).unwrap();
        assert!(crate::page::verify_crc32c(&page));
        let header = PageHeader::decode(&page, 7).unwrap();
        assert_eq!(header.page_type, PageType::RangeLeaf);
        assert_eq!(header.item_count, 2);
        assert!(page[usize::from(header.lower)..]
            .iter()
            .all(|&byte| byte == 0));
        let leaf =
            RangeLeaf::<Ipv4Key>::open(&page, 7, AddressFamily::Ipv4, ValueKind::Direct).unwrap();
        assert_eq!(leaf.record(0).unwrap(), records[0]);
        assert_eq!(leaf.record(1).unwrap(), records[1]);

        let before = page;
        assert_eq!(
            encode_leaf(
                &mut page,
                7,
                ValueKind::Membership,
                &[RangeRecord {
                    from: Ipv4Key(10),
                    to: Ipv4Key(20),
                    value: 0,
                }],
            ),
            Err(RangePageWriteError::MembershipValueZero)
        );
        assert_eq!(page, before);

        assert_eq!(
            encode_leaf(
                &mut page,
                7,
                ValueKind::Direct,
                &[RangeRecord {
                    from: Ipv4Key(20),
                    to: Ipv4Key(10),
                    value: 7,
                }],
            ),
            Err(RangePageWriteError::RangeReversed)
        );
        assert_eq!(page, before);

        assert_eq!(
            encode_leaf(
                &mut page,
                7,
                ValueKind::Direct,
                &[
                    RangeRecord {
                        from: Ipv4Key(10),
                        to: Ipv4Key(20),
                        value: 1,
                    },
                    RangeRecord {
                        from: Ipv4Key(20),
                        to: Ipv4Key(30),
                        value: 2,
                    },
                ],
            ),
            Err(RangePageWriteError::RangeOverlap)
        );
        assert_eq!(page, before);

        assert_eq!(
            encode_leaf(
                &mut page,
                7,
                ValueKind::Direct,
                &[
                    RangeRecord {
                        from: Ipv4Key(10),
                        to: Ipv4Key(20),
                        value: 7,
                    },
                    RangeRecord {
                        from: Ipv4Key(21),
                        to: Ipv4Key(30),
                        value: 7,
                    },
                ],
            ),
            Err(RangePageWriteError::AdjacentEqualValue)
        );
        assert_eq!(page, before);

        let too_many = std::vec![
            RangeRecord {
                from: Ipv4Key(0),
                to: Ipv4Key(0),
                value: 1,
            };
            leaf_capacity::<Ipv4Key>() + 1
        ];
        assert_eq!(
            encode_leaf(&mut page, 7, ValueKind::Direct, &too_many),
            Err(RangePageWriteError::TooManyRecords {
                required: leaf_capacity::<Ipv4Key>() + 1,
                actual: leaf_capacity::<Ipv4Key>(),
            })
        );
        assert_eq!(page, before);
    }

    #[test]
    fn branch_encoder_is_canonical_atomic_and_round_trips() {
        assert_eq!(branch_capacity::<Ipv4Key>(), 127);
        assert_eq!(branch_capacity::<Ipv6Key>(), 50);

        let entries = [
            RangeBranchEntry {
                lower_fence: Ipv4Key::MIN,
                child_pgno: 2,
                subtree_record_count: 1,
                first_from: Ipv4Key(10),
                last_from: Ipv4Key(10),
                last_to: Ipv4Key(20),
            },
            RangeBranchEntry {
                lower_fence: Ipv4Key(100),
                child_pgno: 3,
                subtree_record_count: 0,
                first_from: Ipv4Key::MIN,
                last_from: Ipv4Key::MIN,
                last_to: Ipv4Key::MIN,
            },
            RangeBranchEntry {
                lower_fence: Ipv4Key(200),
                child_pgno: 4,
                subtree_record_count: 1,
                first_from: Ipv4Key(210),
                last_from: Ipv4Key(210),
                last_to: Ipv4Key(220),
            },
        ];
        let mut page = [0x5a; PAGE_SIZE];
        encode_branch(&mut page, 7, 1, 6, Ipv4Key::MIN, None, &entries).unwrap();
        assert!(crate::page::verify_crc32c(&page));
        let branch = RangeBranch::<Ipv4Key>::open(&page, 7, AddressFamily::Ipv4, 6).unwrap();
        assert_eq!(branch.entry(0).unwrap(), entries[0]);
        assert_eq!(branch.entry(1).unwrap(), entries[1]);
        assert_eq!(branch.entry(2).unwrap(), entries[2]);

        let before = page;
        let mut wrong_first = entries;
        wrong_first[0].lower_fence = Ipv4Key(1);
        assert_eq!(
            encode_branch(&mut page, 7, 1, 6, Ipv4Key::MIN, None, &wrong_first),
            Err(RangePageWriteError::FirstFence)
        );
        assert_eq!(page, before);

        let invalid_bounds = [RangeBranchEntry {
            lower_fence: Ipv4Key(100),
            child_pgno: 2,
            subtree_record_count: 1,
            first_from: Ipv4Key(110),
            last_from: Ipv4Key(110),
            last_to: Ipv4Key(120),
        }];
        assert_eq!(
            encode_branch(
                &mut page,
                7,
                1,
                4,
                Ipv4Key(100),
                Some(Ipv4Key(100)),
                &invalid_bounds,
            ),
            Err(RangePageWriteError::FenceBounds)
        );
        assert_eq!(page, before);

        let mut overlapping = entries;
        overlapping[0].last_to = Ipv4Key(220);
        assert_eq!(
            encode_branch(&mut page, 7, 1, 6, Ipv4Key::MIN, None, &overlapping),
            Err(RangePageWriteError::SummaryOverlap)
        );
        assert_eq!(page, before);

        let outside_fence = [RangeBranchEntry {
            lower_fence: Ipv4Key(100),
            child_pgno: 2,
            subtree_record_count: 1,
            first_from: Ipv4Key(110),
            last_from: Ipv4Key(200),
            last_to: Ipv4Key(220),
        }];
        assert_eq!(
            encode_branch(
                &mut page,
                7,
                1,
                4,
                Ipv4Key(100),
                Some(Ipv4Key(200)),
                &outside_fence,
            ),
            Err(RangePageWriteError::SummaryOutsideFence)
        );
        assert_eq!(page, before);

        let v6 = [RangeBranchEntry {
            lower_fence: Ipv6Key::MIN,
            child_pgno: 3,
            subtree_record_count: 1,
            first_from: Ipv6Key { hi: 1, lo: 2 },
            last_from: Ipv6Key { hi: 1, lo: 2 },
            last_to: Ipv6Key { hi: 1, lo: 3 },
        }];
        encode_branch(&mut page, 7, 1, 4, Ipv6Key::MIN, None, &v6).unwrap();
        assert!(page[52..56].iter().all(|&byte| byte == 0));
        let branch = RangeBranch::<Ipv6Key>::open(&page, 7, AddressFamily::Ipv6, 4).unwrap();
        assert_eq!(branch.entry(0).unwrap(), v6[0]);
    }
}
