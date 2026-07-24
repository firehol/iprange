//! Fixed-page ordered collection of private physical page numbers.
//!
//! The logical pages here belong to a writer workspace only. They have no v4
//! physical page identity and are never visible from a committed root.

use crate::contract::PAGE_SIZE;

const LEAF_ENTRY_BYTES: usize = 4;
const BRANCH_ENTRY_BYTES: usize = 8;
const LEAF_CAPACITY: usize = PAGE_SIZE / LEAF_ENTRY_BYTES;
const BRANCH_CAPACITY: usize = PAGE_SIZE / BRANCH_ENTRY_BYTES;
const MAX_BRANCH_DEPTH: usize = 3;
const NO_PAGE: u32 = u32::MAX;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum PageNumberIndexPageKind {
    Empty,
    Leaf,
    Branch,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct PageNumberIndexPage {
    bytes: [u8; PAGE_SIZE],
    kind: PageNumberIndexPageKind,
    count: u16,
}

impl PageNumberIndexPage {
    pub(crate) const fn empty() -> Self {
        Self {
            bytes: [0; PAGE_SIZE],
            kind: PageNumberIndexPageKind::Empty,
            count: 0,
        }
    }
}

pub(crate) struct PageNumberIndexWorkspace<'storage> {
    pages: &'storage mut [PageNumberIndexPage],
}

impl<'storage> PageNumberIndexWorkspace<'storage> {
    pub(crate) fn new(pages: &'storage mut [PageNumberIndexPage]) -> Self {
        Self { pages }
    }

    fn is_clean(&self) -> bool {
        self.pages
            .iter()
            .all(|page| page.kind == PageNumberIndexPageKind::Empty && page.count == 0)
    }

    fn reset(&mut self) {
        self.pages.fill(PageNumberIndexPage::empty());
    }

    pub(crate) fn discard_after_abort(&mut self) {
        self.reset();
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum PageNumberIndexError {
    WorkspaceBusy,
    WorkspacePageLimit,
    PageBudget { required: usize, actual: usize },
    InvalidPageReference,
    InvalidPageEncoding,
    TreeTooDeep,
    Failed,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum PageNumberIndexVisitError<E> {
    Index(PageNumberIndexError),
    Visitor(E),
}

#[derive(Clone, Copy, Debug)]
struct PathFrame {
    page: u32,
    child_index: usize,
}

impl PathFrame {
    const EMPTY: Self = Self {
        page: NO_PAGE,
        child_index: 0,
    };
}

#[derive(Clone, Copy, Debug)]
struct BranchEntry {
    maximum: u32,
    child: u32,
}

/// A fixed-page B+ tree with sorted `u32` leaves.
///
/// All split capacity is calculated before the first node write, so an
/// insufficient workspace leaves the existing private index unchanged.
pub(crate) struct PageNumberIndex<'workspace, 'storage> {
    workspace: &'workspace mut PageNumberIndexWorkspace<'storage>,
    root: u32,
    pages: usize,
    values: u64,
    failed: bool,
    leaf_scratch: [u32; LEAF_CAPACITY + 1],
    branch_scratch: [BranchEntry; BRANCH_CAPACITY + 1],
}

impl<'workspace, 'storage> PageNumberIndex<'workspace, 'storage> {
    pub(crate) fn new(
        workspace: &'workspace mut PageNumberIndexWorkspace<'storage>,
    ) -> Result<Self, PageNumberIndexError> {
        if !workspace.is_clean() {
            return Err(PageNumberIndexError::WorkspaceBusy);
        }
        if workspace.pages.len() > u32::MAX as usize {
            return Err(PageNumberIndexError::WorkspacePageLimit);
        }
        Ok(Self {
            workspace,
            root: NO_PAGE,
            pages: 0,
            values: 0,
            failed: false,
            leaf_scratch: [0; LEAF_CAPACITY + 1],
            branch_scratch: [BranchEntry {
                maximum: 0,
                child: 0,
            }; BRANCH_CAPACITY + 1],
        })
    }

    pub(crate) const fn len(&self) -> u64 {
        self.values
    }

    pub(crate) const fn logical_page_count(&self) -> usize {
        self.pages
    }

    pub(crate) fn discard_after_abort(&mut self) {
        self.workspace.discard_after_abort();
        self.root = NO_PAGE;
        self.pages = 0;
        self.values = 0;
        self.failed = false;
    }

    fn fail(&mut self, error: PageNumberIndexError) -> PageNumberIndexError {
        self.failed = true;
        error
    }

    fn page(&self, reference: u32) -> Result<&PageNumberIndexPage, PageNumberIndexError> {
        let index =
            usize::try_from(reference).map_err(|_| PageNumberIndexError::InvalidPageReference)?;
        if index >= self.pages {
            return Err(PageNumberIndexError::InvalidPageReference);
        }
        Ok(&self.workspace.pages[index])
    }

    fn page_mut(
        &mut self,
        reference: u32,
    ) -> Result<&mut PageNumberIndexPage, PageNumberIndexError> {
        let index =
            usize::try_from(reference).map_err(|_| PageNumberIndexError::InvalidPageReference)?;
        if index >= self.pages {
            return Err(PageNumberIndexError::InvalidPageReference);
        }
        Ok(&mut self.workspace.pages[index])
    }

    fn leaf_value(page: &PageNumberIndexPage, index: usize) -> u32 {
        let offset = index * LEAF_ENTRY_BYTES;
        u32::from_le_bytes(
            page.bytes[offset..offset + LEAF_ENTRY_BYTES]
                .try_into()
                .unwrap(),
        )
    }

    fn set_leaf_value(page: &mut PageNumberIndexPage, index: usize, value: u32) {
        let offset = index * LEAF_ENTRY_BYTES;
        page.bytes[offset..offset + LEAF_ENTRY_BYTES].copy_from_slice(&value.to_le_bytes());
    }

    fn branch_entry(page: &PageNumberIndexPage, index: usize) -> BranchEntry {
        let offset = index * BRANCH_ENTRY_BYTES;
        BranchEntry {
            maximum: u32::from_le_bytes(page.bytes[offset..offset + 4].try_into().unwrap()),
            child: u32::from_le_bytes(page.bytes[offset + 4..offset + 8].try_into().unwrap()),
        }
    }

    fn set_branch_entry(page: &mut PageNumberIndexPage, index: usize, entry: BranchEntry) {
        let offset = index * BRANCH_ENTRY_BYTES;
        page.bytes[offset..offset + 4].copy_from_slice(&entry.maximum.to_le_bytes());
        page.bytes[offset + 4..offset + 8].copy_from_slice(&entry.child.to_le_bytes());
    }

    fn validate_leaf(&self, reference: u32) -> Result<(), PageNumberIndexError> {
        let page = self.page(reference)?;
        if page.kind != PageNumberIndexPageKind::Leaf
            || page.count == 0
            || usize::from(page.count) > LEAF_CAPACITY
        {
            return Err(PageNumberIndexError::InvalidPageEncoding);
        }
        Ok(())
    }

    fn validate_branch(&self, reference: u32) -> Result<(), PageNumberIndexError> {
        let page = self.page(reference)?;
        if page.kind != PageNumberIndexPageKind::Branch
            || page.count == 0
            || usize::from(page.count) > BRANCH_CAPACITY
        {
            return Err(PageNumberIndexError::InvalidPageEncoding);
        }
        Ok(())
    }

    fn leaf_search(page: &PageNumberIndexPage, value: u32) -> usize {
        let mut low = 0;
        let mut high = usize::from(page.count);
        while low < high {
            let middle = low + (high - low) / 2;
            if Self::leaf_value(page, middle) < value {
                low = middle + 1;
            } else {
                high = middle;
            }
        }
        low
    }

    fn branch_search(page: &PageNumberIndexPage, value: u32) -> usize {
        let mut low = 0;
        let mut high = usize::from(page.count);
        while low < high {
            let middle = low + (high - low) / 2;
            if Self::branch_entry(page, middle).maximum < value {
                low = middle + 1;
            } else {
                high = middle;
            }
        }
        if low == usize::from(page.count) {
            low - 1
        } else {
            low
        }
    }

    fn required_insert_pages(
        &self,
        path: &[PathFrame],
        leaf: u32,
    ) -> Result<usize, PageNumberIndexError> {
        if usize::from(self.page(leaf)?.count) < LEAF_CAPACITY {
            return Ok(0);
        }
        let mut required = 1usize;
        let mut carry = true;
        for frame in path.iter().rev() {
            if !carry {
                break;
            }
            if usize::from(self.page(frame.page)?.count) == BRANCH_CAPACITY {
                required = required
                    .checked_add(1)
                    .ok_or(PageNumberIndexError::PageBudget {
                        required: usize::MAX,
                        actual: self.workspace.pages.len().saturating_sub(self.pages),
                    })?;
            } else {
                carry = false;
            }
        }
        if carry {
            required = required
                .checked_add(1)
                .ok_or(PageNumberIndexError::PageBudget {
                    required: usize::MAX,
                    actual: self.workspace.pages.len().saturating_sub(self.pages),
                })?;
        }
        Ok(required)
    }

    fn reserve_new_pages(&self, required: usize) -> Result<(), PageNumberIndexError> {
        let actual = self.workspace.pages.len().saturating_sub(self.pages);
        if required > actual {
            return Err(PageNumberIndexError::PageBudget { required, actual });
        }
        Ok(())
    }

    fn allocate_page(&mut self, kind: PageNumberIndexPageKind) -> u32 {
        debug_assert!(self.pages < self.workspace.pages.len());
        let index = self.pages;
        self.pages += 1;
        self.workspace.pages[index] = PageNumberIndexPage {
            bytes: [0; PAGE_SIZE],
            kind,
            count: 0,
        };
        u32::try_from(index).unwrap()
    }

    fn write_leaf(page: &mut PageNumberIndexPage, values: &[u32]) {
        debug_assert!(!values.is_empty() && values.len() <= LEAF_CAPACITY);
        *page = PageNumberIndexPage {
            bytes: [0; PAGE_SIZE],
            kind: PageNumberIndexPageKind::Leaf,
            count: u16::try_from(values.len()).unwrap(),
        };
        for (index, value) in values.iter().copied().enumerate() {
            Self::set_leaf_value(page, index, value);
        }
    }

    fn write_branch(page: &mut PageNumberIndexPage, entries: &[BranchEntry]) {
        debug_assert!(!entries.is_empty() && entries.len() <= BRANCH_CAPACITY);
        *page = PageNumberIndexPage {
            bytes: [0; PAGE_SIZE],
            kind: PageNumberIndexPageKind::Branch,
            count: u16::try_from(entries.len()).unwrap(),
        };
        for (index, entry) in entries.iter().copied().enumerate() {
            Self::set_branch_entry(page, index, entry);
        }
    }

    fn page_maximum(page: &PageNumberIndexPage) -> u32 {
        match page.kind {
            PageNumberIndexPageKind::Leaf => Self::leaf_value(page, usize::from(page.count) - 1),
            PageNumberIndexPageKind::Branch => {
                Self::branch_entry(page, usize::from(page.count) - 1).maximum
            }
            PageNumberIndexPageKind::Empty => unreachable!(),
        }
    }

    fn update_ancestor_maximums(&mut self, path: &[PathFrame], last_parent: usize) {
        let mut maximum =
            Self::page_maximum(&self.workspace.pages[path[last_parent].page as usize]);
        for index in (0..last_parent).rev() {
            let parent = &mut self.workspace.pages[path[index].page as usize];
            let child_index = path[index].child_index;
            let mut entry = Self::branch_entry(parent, child_index);
            entry.maximum = maximum;
            Self::set_branch_entry(parent, child_index, entry);
            if child_index != usize::from(parent.count) - 1 {
                return;
            }
            maximum = Self::page_maximum(parent);
        }
    }

    fn update_leaf_maximum(&mut self, path: &[PathFrame], leaf: u32) {
        let mut maximum = Self::page_maximum(&self.workspace.pages[leaf as usize]);
        for index in (0..path.len()).rev() {
            let parent = &mut self.workspace.pages[path[index].page as usize];
            let child_index = path[index].child_index;
            let mut entry = Self::branch_entry(parent, child_index);
            entry.maximum = maximum;
            Self::set_branch_entry(parent, child_index, entry);
            if child_index != usize::from(parent.count) - 1 {
                return;
            }
            maximum = Self::page_maximum(parent);
        }
    }

    pub(crate) fn insert(&mut self, value: u32) -> Result<bool, PageNumberIndexError> {
        if self.failed {
            return Err(PageNumberIndexError::Failed);
        }
        if self.root == NO_PAGE {
            self.reserve_new_pages(1)?;
            let root = self.allocate_page(PageNumberIndexPageKind::Leaf);
            Self::write_leaf(&mut self.workspace.pages[root as usize], &[value]);
            self.root = root;
            self.values = 1;
            return Ok(true);
        }

        let mut path = [PathFrame::EMPTY; MAX_BRANCH_DEPTH];
        let mut path_len = 0usize;
        let mut current = self.root;
        loop {
            let kind = match self.page(current) {
                Ok(page) => page.kind,
                Err(error) => return Err(self.fail(error)),
            };
            match kind {
                PageNumberIndexPageKind::Leaf => {
                    if let Err(error) = self.validate_leaf(current) {
                        return Err(self.fail(error));
                    }
                    break;
                }
                PageNumberIndexPageKind::Branch => {
                    if let Err(error) = self.validate_branch(current) {
                        return Err(self.fail(error));
                    }
                    if path_len == path.len() {
                        return Err(self.fail(PageNumberIndexError::TreeTooDeep));
                    }
                    let page = self.page(current).unwrap();
                    let child_index = Self::branch_search(page, value);
                    path[path_len] = PathFrame {
                        page: current,
                        child_index,
                    };
                    path_len += 1;
                    current = Self::branch_entry(page, child_index).child;
                }
                PageNumberIndexPageKind::Empty => {
                    return Err(self.fail(PageNumberIndexError::InvalidPageEncoding));
                }
            }
        }

        let insert_at = Self::leaf_search(self.page(current).unwrap(), value);
        let leaf_count = usize::from(self.page(current).unwrap().count);
        if insert_at != leaf_count
            && Self::leaf_value(self.page(current).unwrap(), insert_at) == value
        {
            return Ok(false);
        }
        let required = match self.required_insert_pages(&path[..path_len], current) {
            Ok(required) => required,
            Err(error) => return Err(self.fail(error)),
        };
        self.reserve_new_pages(required)?;

        if leaf_count < LEAF_CAPACITY {
            {
                let leaf = &mut self.workspace.pages[current as usize];
                leaf.bytes.copy_within(
                    insert_at * LEAF_ENTRY_BYTES..leaf_count * LEAF_ENTRY_BYTES,
                    (insert_at + 1) * LEAF_ENTRY_BYTES,
                );
                Self::set_leaf_value(leaf, insert_at, value);
                leaf.count += 1;
            }
            if path_len != 0 && insert_at == leaf_count {
                self.update_leaf_maximum(&path[..path_len], current);
            }
            self.values += 1;
            return Ok(true);
        }

        for index in 0..leaf_count {
            self.leaf_scratch[index] = Self::leaf_value(self.page(current).unwrap(), index);
        }
        self.leaf_scratch
            .copy_within(insert_at..leaf_count, insert_at + 1);
        self.leaf_scratch[insert_at] = value;
        let left_count = LEAF_CAPACITY.div_ceil(2);
        let right_count = LEAF_CAPACITY + 1 - left_count;
        let leaf_scratch = self.leaf_scratch;
        let right_ref = self.allocate_page(PageNumberIndexPageKind::Leaf);
        Self::write_leaf(
            &mut self.workspace.pages[current as usize],
            &leaf_scratch[..left_count],
        );
        Self::write_leaf(
            &mut self.workspace.pages[right_ref as usize],
            &leaf_scratch[left_count..left_count + right_count],
        );
        let mut left_ref = current;
        let mut right_ref = right_ref;
        let mut left_maximum = Self::page_maximum(&self.workspace.pages[left_ref as usize]);
        let mut right_maximum = Self::page_maximum(&self.workspace.pages[right_ref as usize]);

        for path_index in (0..path_len).rev() {
            let parent_ref = path[path_index].page;
            let child_index = path[path_index].child_index;
            debug_assert_eq!(
                Self::branch_entry(&self.workspace.pages[parent_ref as usize], child_index).child,
                left_ref
            );
            let parent_count = usize::from(self.workspace.pages[parent_ref as usize].count);
            if parent_count < BRANCH_CAPACITY {
                let parent = &mut self.workspace.pages[parent_ref as usize];
                parent.bytes.copy_within(
                    (child_index + 1) * BRANCH_ENTRY_BYTES..parent_count * BRANCH_ENTRY_BYTES,
                    (child_index + 2) * BRANCH_ENTRY_BYTES,
                );
                Self::set_branch_entry(
                    parent,
                    child_index,
                    BranchEntry {
                        maximum: left_maximum,
                        child: left_ref,
                    },
                );
                Self::set_branch_entry(
                    parent,
                    child_index + 1,
                    BranchEntry {
                        maximum: right_maximum,
                        child: right_ref,
                    },
                );
                parent.count += 1;
                self.update_ancestor_maximums(&path[..path_len], path_index);
                self.values += 1;
                return Ok(true);
            }

            for index in 0..parent_count {
                self.branch_scratch[index] =
                    Self::branch_entry(&self.workspace.pages[parent_ref as usize], index);
            }
            self.branch_scratch
                .copy_within(child_index + 1..parent_count, child_index + 2);
            self.branch_scratch[child_index] = BranchEntry {
                maximum: left_maximum,
                child: left_ref,
            };
            self.branch_scratch[child_index + 1] = BranchEntry {
                maximum: right_maximum,
                child: right_ref,
            };
            let left_count = BRANCH_CAPACITY.div_ceil(2);
            let right_count = BRANCH_CAPACITY + 1 - left_count;
            let branch_scratch = self.branch_scratch;
            right_ref = self.allocate_page(PageNumberIndexPageKind::Branch);
            Self::write_branch(
                &mut self.workspace.pages[parent_ref as usize],
                &branch_scratch[..left_count],
            );
            Self::write_branch(
                &mut self.workspace.pages[right_ref as usize],
                &branch_scratch[left_count..left_count + right_count],
            );
            left_ref = parent_ref;
            left_maximum = Self::page_maximum(&self.workspace.pages[left_ref as usize]);
            right_maximum = Self::page_maximum(&self.workspace.pages[right_ref as usize]);
        }

        let root = self.allocate_page(PageNumberIndexPageKind::Branch);
        Self::write_branch(
            &mut self.workspace.pages[root as usize],
            &[
                BranchEntry {
                    maximum: left_maximum,
                    child: left_ref,
                },
                BranchEntry {
                    maximum: right_maximum,
                    child: right_ref,
                },
            ],
        );
        self.root = root;
        self.values += 1;
        Ok(true)
    }

    pub(crate) fn visit_ascending<E>(
        &mut self,
        mut visitor: impl FnMut(u32) -> Result<(), E>,
    ) -> Result<(), PageNumberIndexVisitError<E>> {
        if self.failed {
            return Err(PageNumberIndexVisitError::Index(
                PageNumberIndexError::Failed,
            ));
        }
        if self.root == NO_PAGE {
            return Ok(());
        }
        self.visit_node(self.root, 0, &mut visitor)
    }

    fn visit_node<E>(
        &mut self,
        reference: u32,
        depth: usize,
        visitor: &mut impl FnMut(u32) -> Result<(), E>,
    ) -> Result<(), PageNumberIndexVisitError<E>> {
        let kind = match self.page(reference) {
            Ok(page) => page.kind,
            Err(error) => return Err(PageNumberIndexVisitError::Index(self.fail(error))),
        };
        match kind {
            PageNumberIndexPageKind::Leaf => {
                if let Err(error) = self.validate_leaf(reference) {
                    return Err(PageNumberIndexVisitError::Index(self.fail(error)));
                }
                let count = usize::from(self.page(reference).unwrap().count);
                for index in 0..count {
                    let value = Self::leaf_value(self.page(reference).unwrap(), index);
                    visitor(value).map_err(PageNumberIndexVisitError::Visitor)?;
                }
                Ok(())
            }
            PageNumberIndexPageKind::Branch => {
                if depth == MAX_BRANCH_DEPTH {
                    return Err(PageNumberIndexVisitError::Index(
                        self.fail(PageNumberIndexError::TreeTooDeep),
                    ));
                }
                if let Err(error) = self.validate_branch(reference) {
                    return Err(PageNumberIndexVisitError::Index(self.fail(error)));
                }
                let count = usize::from(self.page(reference).unwrap().count);
                for index in 0..count {
                    let child = Self::branch_entry(self.page(reference).unwrap(), index).child;
                    self.visit_node(child, depth + 1, visitor)?;
                }
                Ok(())
            }
            PageNumberIndexPageKind::Empty => Err(PageNumberIndexVisitError::Index(
                self.fail(PageNumberIndexError::InvalidPageEncoding),
            )),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_alloc::count_thread_allocations;
    use std::vec;
    use std::vec::Vec;

    fn collect(index: &mut PageNumberIndex<'_, '_>) -> Vec<u32> {
        let mut values = Vec::new();
        index
            .visit_ascending(|value| {
                values.push(value);
                Ok::<(), ()>(())
            })
            .unwrap();
        values
    }

    #[test]
    fn orders_and_deduplicates() {
        let mut pages = [PageNumberIndexPage::empty(); 8];
        let mut workspace = PageNumberIndexWorkspace::new(&mut pages);
        let mut index = PageNumberIndex::new(&mut workspace).unwrap();
        let values = [429, 2, 55, 4_000_000_000, 55, 3, 2, 1_024, 0, u32::MAX];
        let inserted = [true, true, true, true, false, true, false, true, true, true];
        for (value, expected) in values.into_iter().zip(inserted) {
            assert_eq!(index.insert(value), Ok(expected));
        }
        assert_eq!(
            collect(&mut index),
            vec![0, 2, 3, 55, 429, 1_024, 4_000_000_000, u32::MAX]
        );
        assert_eq!(index.len(), 8);
        assert_eq!(index.logical_page_count(), 1);
    }

    #[test]
    fn splits_dense_reverse_input() {
        let mut pages = [PageNumberIndexPage::empty(); 16];
        let mut workspace = PageNumberIndexWorkspace::new(&mut pages);
        let mut index = PageNumberIndex::new(&mut workspace).unwrap();
        for value in (0..4_096u32).rev() {
            assert_eq!(index.insert(value), Ok(true));
        }
        assert!(index.logical_page_count() > 2);
        assert_eq!(index.len(), 4_096);
        let mut expected = 0u32;
        index
            .visit_ascending(|value| {
                assert_eq!(value, expected);
                expected += 1;
                Ok::<(), ()>(())
            })
            .unwrap();
        assert_eq!(expected, 4_096);
    }

    #[test]
    fn splits_full_branch_root() {
        const COUNT: u32 = 270_000;
        let mut pages = vec![PageNumberIndexPage::empty(); 540];
        let mut workspace = PageNumberIndexWorkspace::new(&mut pages);
        let mut index = PageNumberIndex::new(&mut workspace).unwrap();
        for value in 0..COUNT {
            assert_eq!(index.insert(value), Ok(true));
        }
        let root = &index.workspace.pages[index.root as usize];
        assert_eq!(root.kind, PageNumberIndexPageKind::Branch);
        assert_eq!(
            index.workspace.pages[PageNumberIndex::branch_entry(root, 0).child as usize].kind,
            PageNumberIndexPageKind::Branch
        );
        let mut expected = 0u32;
        index
            .visit_ascending(|value| {
                assert_eq!(value, expected);
                expected += 1;
                Ok::<(), ()>(())
            })
            .unwrap();
        assert_eq!(expected, COUNT);
        assert_eq!(index.len(), u64::from(COUNT));
    }

    #[test]
    fn capacity_rejection_is_pre_mutation() {
        let mut pages = [PageNumberIndexPage::empty(); 1];
        let mut workspace = PageNumberIndexWorkspace::new(&mut pages);
        let mut index = PageNumberIndex::new(&mut workspace).unwrap();
        for value in 0..LEAF_CAPACITY as u32 {
            assert_eq!(index.insert(value), Ok(true));
        }
        let before_pages = index.workspace.pages.to_vec();
        let before_len = index.len();
        let before_count = index.logical_page_count();
        assert_eq!(
            index.insert(LEAF_CAPACITY as u32),
            Err(PageNumberIndexError::PageBudget {
                required: 2,
                actual: 0,
            })
        );
        assert_eq!(index.workspace.pages, before_pages.as_slice());
        assert_eq!(index.len(), before_len);
        assert_eq!(index.logical_page_count(), before_count);
        assert_eq!(index.insert(17), Ok(false));
    }

    #[test]
    fn rejects_stale_workspace_and_scrubs_on_abort() {
        let mut pages = [PageNumberIndexPage::empty(); 2];
        pages[0].kind = PageNumberIndexPageKind::Leaf;
        let mut workspace = PageNumberIndexWorkspace::new(&mut pages);
        assert_eq!(
            PageNumberIndex::new(&mut workspace).err(),
            Some(PageNumberIndexError::WorkspaceBusy)
        );
        workspace.reset();
        let mut index = PageNumberIndex::new(&mut workspace).unwrap();
        assert_eq!(index.insert(99), Ok(true));
        index.discard_after_abort();
        assert!(index.workspace.is_clean());
        assert_eq!(index.len(), 0);
        assert_eq!(index.logical_page_count(), 0);
    }

    fn fill_no_alloc(index: &mut PageNumberIndex<'_, '_>) -> bool {
        index.discard_after_abort();
        for value in 0..2_048u32 {
            if index.insert(value) != Ok(true) {
                return false;
            }
        }
        index.len() == 2_048
    }

    #[test]
    fn uses_no_heap_after_workspace_setup() {
        let mut pages = [PageNumberIndexPage::empty(); 8];
        let mut workspace = PageNumberIndexWorkspace::new(&mut pages);
        let mut index = PageNumberIndex::new(&mut workspace).unwrap();
        assert!(fill_no_alloc(&mut index));
        let (ok, allocations) = count_thread_allocations(|| fill_no_alloc(&mut index));
        assert!(ok);
        assert_eq!(allocations, 0);
    }
}
