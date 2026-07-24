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
struct CursorFrame {
    page: u32,
    next_child: usize,
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

    fn validate_state(&self) -> Result<(), PageNumberIndexError> {
        if self.failed {
            return Err(PageNumberIndexError::Failed);
        }
        if self.pages > self.workspace.pages.len() {
            return Err(PageNumberIndexError::InvalidPageReference);
        }
        if self.root == NO_PAGE {
            if self.pages != 0 || self.values != 0 {
                return Err(PageNumberIndexError::InvalidPageEncoding);
            }
            return Ok(());
        }
        let root =
            usize::try_from(self.root).map_err(|_| PageNumberIndexError::InvalidPageReference)?;
        if self.pages == 0 || self.values == 0 || root >= self.pages {
            return Err(PageNumberIndexError::InvalidPageEncoding);
        }
        Ok(())
    }

    fn is_empty_and_clean(&self) -> bool {
        !self.failed
            && self.root == NO_PAGE
            && self.pages == 0
            && self.values == 0
            && self.workspace.is_clean()
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

/// Iterative ordered traversal of private index pages. The fixed frame array
/// is bounded by the tree depth, so cloning and equality never allocate a
/// traversal stack proportional to the listed page count.
struct PageNumberIndexCursor<'index, 'workspace, 'storage> {
    index: &'index mut PageNumberIndex<'workspace, 'storage>,
    frames: [CursorFrame; MAX_BRANCH_DEPTH],
    frame_len: usize,
    leaf: u32,
    leaf_next: usize,
    leaf_count: usize,
    initialized: bool,
    done: bool,
    emitted: u64,
    previous: u32,
    has_previous: bool,
}

impl<'index, 'workspace, 'storage> PageNumberIndexCursor<'index, 'workspace, 'storage> {
    fn new(
        index: &'index mut PageNumberIndex<'workspace, 'storage>,
    ) -> Result<Self, PageNumberIndexError> {
        if let Err(error) = index.validate_state() {
            return Err(index.fail(error));
        }
        Ok(Self {
            index,
            frames: [CursorFrame {
                page: NO_PAGE,
                next_child: 0,
            }; MAX_BRANCH_DEPTH],
            frame_len: 0,
            leaf: NO_PAGE,
            leaf_next: 0,
            leaf_count: 0,
            initialized: false,
            done: false,
            emitted: 0,
            previous: 0,
            has_previous: false,
        })
    }

    fn fail(&mut self, error: PageNumberIndexError) -> PageNumberIndexError {
        self.done = true;
        self.index.fail(error)
    }

    fn descend_leftmost(
        &mut self,
        mut reference: u32,
        mut depth: usize,
    ) -> Result<(), PageNumberIndexError> {
        loop {
            let kind = match self.index.page(reference) {
                Ok(page) => page.kind,
                Err(error) => return Err(self.fail(error)),
            };
            match kind {
                PageNumberIndexPageKind::Leaf => {
                    if let Err(error) = self.index.validate_leaf(reference) {
                        return Err(self.fail(error));
                    }
                    let count = match self.index.page(reference) {
                        Ok(page) => usize::from(page.count),
                        Err(error) => return Err(self.fail(error)),
                    };
                    self.leaf = reference;
                    self.leaf_next = 0;
                    self.leaf_count = count;
                    return Ok(());
                }
                PageNumberIndexPageKind::Branch => {
                    if depth == MAX_BRANCH_DEPTH || self.frame_len == self.frames.len() {
                        return Err(self.fail(PageNumberIndexError::TreeTooDeep));
                    }
                    if let Err(error) = self.index.validate_branch(reference) {
                        return Err(self.fail(error));
                    }
                    let child = match self.index.page(reference) {
                        Ok(page) => PageNumberIndex::branch_entry(page, 0).child,
                        Err(error) => return Err(self.fail(error)),
                    };
                    self.frames[self.frame_len] = CursorFrame {
                        page: reference,
                        next_child: 1,
                    };
                    self.frame_len += 1;
                    reference = child;
                    depth += 1;
                }
                PageNumberIndexPageKind::Empty => {
                    return Err(self.fail(PageNumberIndexError::InvalidPageEncoding));
                }
            }
        }
    }

    /// Returns one ordered value, or `None` once the declared stream ends.
    fn next(&mut self) -> Result<Option<u32>, PageNumberIndexError> {
        if self.done {
            return Ok(None);
        }
        if !self.initialized {
            self.initialized = true;
            if self.index.root == NO_PAGE {
                self.done = true;
                return Ok(None);
            }
            self.descend_leftmost(self.index.root, 0)?;
        }

        loop {
            if self.leaf_next < self.leaf_count {
                if self.emitted >= self.index.values {
                    return Err(self.fail(PageNumberIndexError::InvalidPageEncoding));
                }
                let value = match self.index.page(self.leaf) {
                    Ok(page) => PageNumberIndex::leaf_value(page, self.leaf_next),
                    Err(error) => return Err(self.fail(error)),
                };
                self.leaf_next += 1;
                if self.has_previous && value <= self.previous {
                    return Err(self.fail(PageNumberIndexError::InvalidPageEncoding));
                }
                self.previous = value;
                self.has_previous = true;
                self.emitted += 1;
                return Ok(Some(value));
            }

            let mut advanced = false;
            while self.frame_len > 0 {
                let frame_index = self.frame_len - 1;
                let page_ref = self.frames[frame_index].page;
                let page = match self.index.page(page_ref) {
                    Ok(page) => page,
                    Err(error) => return Err(self.fail(error)),
                };
                if let Err(error) = self.index.validate_branch(page_ref) {
                    return Err(self.fail(error));
                }
                if self.frames[frame_index].next_child < usize::from(page.count) {
                    let child =
                        PageNumberIndex::branch_entry(page, self.frames[frame_index].next_child)
                            .child;
                    self.frames[frame_index].next_child += 1;
                    self.descend_leftmost(child, self.frame_len)?;
                    advanced = true;
                    break;
                }
                self.frame_len -= 1;
            }
            if advanced {
                continue;
            }
            self.done = true;
            if self.emitted != self.index.values {
                return Err(self.fail(PageNumberIndexError::InvalidPageEncoding));
            }
            return Ok(None);
        }
    }
}

fn page_number_index_ceil_div(value: u64, divisor: u64) -> u64 {
    let quotient = value / divisor;
    if value % divisor == 0 {
        quotient
    } else {
        quotient + 1
    }
}

fn dense_clone_page_count(value_count: u64) -> Result<usize, PageNumberIndexError> {
    if value_count == 0 {
        return Ok(0);
    }
    let mut children = page_number_index_ceil_div(value_count, LEAF_CAPACITY as u64);
    let mut total = children;
    let mut branch_depth = 0usize;
    while children > 1 {
        if branch_depth == MAX_BRANCH_DEPTH {
            return Err(PageNumberIndexError::TreeTooDeep);
        }
        children = page_number_index_ceil_div(children, BRANCH_CAPACITY as u64);
        total = total
            .checked_add(children)
            .ok_or(PageNumberIndexError::InvalidPageEncoding)?;
        branch_depth += 1;
    }
    usize::try_from(total).map_err(|_| PageNumberIndexError::TreeTooDeep)
}

/// Compares ordered values, rather than logical private-page layout.
pub(crate) fn page_number_indexes_equal(
    left: &mut PageNumberIndex<'_, '_>,
    right: &mut PageNumberIndex<'_, '_>,
) -> Result<bool, PageNumberIndexError> {
    let mut left_cursor = PageNumberIndexCursor::new(left)?;
    let mut right_cursor = PageNumberIndexCursor::new(right)?;
    let mut equal = true;
    loop {
        let left_value = left_cursor.next()?;
        let right_value = right_cursor.next()?;
        if left_value != right_value {
            equal = false;
        }
        if left_value.is_none() && right_value.is_none() {
            return Ok(equal);
        }
    }
}

fn clone_abort(
    destination: &mut PageNumberIndex<'_, '_>,
    error: PageNumberIndexError,
) -> Result<(), PageNumberIndexError> {
    destination.discard_after_abort();
    Err(error)
}

/// Makes a dense copy in distinct caller-owned private workspace.
///
/// The source is streamed exactly once after output capacity is preflighted.
/// A malformed source discovered during that stream scrubs every destination
/// page before the error is returned.
pub(crate) fn clone_page_number_index_into(
    destination: &mut PageNumberIndex<'_, '_>,
    source: &mut PageNumberIndex<'_, '_>,
) -> Result<(), PageNumberIndexError> {
    if !destination.is_empty_and_clean() {
        return Err(PageNumberIndexError::WorkspaceBusy);
    }
    if let Err(error) = source.validate_state() {
        return Err(source.fail(error));
    }

    let value_count = source.values;
    let required = match dense_clone_page_count(value_count) {
        Ok(required) => required,
        Err(error) => return Err(source.fail(error)),
    };
    if required > destination.workspace.pages.len() {
        return Err(PageNumberIndexError::PageBudget {
            required,
            actual: destination.workspace.pages.len(),
        });
    }
    if value_count == 0 {
        return Ok(());
    }

    let leaf_pages = page_number_index_ceil_div(value_count, LEAF_CAPACITY as u64) as usize;
    let mut remaining = value_count;
    let mut cursor = PageNumberIndexCursor::new(source)?;
    for _ in 0..leaf_pages {
        let entry_count = usize::try_from(remaining.min(LEAF_CAPACITY as u64)).unwrap();
        let leaf_ref = destination.allocate_page(PageNumberIndexPageKind::Leaf);
        destination.workspace.pages[leaf_ref as usize].count = u16::try_from(entry_count).unwrap();
        for entry_index in 0..entry_count {
            let value = match cursor.next() {
                Ok(Some(value)) => value,
                Ok(None) => {
                    let error = cursor.fail(PageNumberIndexError::InvalidPageEncoding);
                    return clone_abort(destination, error);
                }
                Err(error) => return clone_abort(destination, error),
            };
            PageNumberIndex::set_leaf_value(
                &mut destination.workspace.pages[leaf_ref as usize],
                entry_index,
                value,
            );
        }
        remaining -= entry_count as u64;
    }
    match cursor.next() {
        Ok(None) => {}
        Ok(Some(_)) => {
            let error = cursor.fail(PageNumberIndexError::InvalidPageEncoding);
            return clone_abort(destination, error);
        }
        Err(error) => return clone_abort(destination, error),
    }
    drop(cursor);

    let mut child_start = 0usize;
    let mut child_count = leaf_pages;
    while child_count > 1 {
        let parent_start = destination.pages;
        let parent_count = child_count.div_ceil(BRANCH_CAPACITY);
        for parent_index in 0..parent_count {
            let first_child = parent_index * BRANCH_CAPACITY;
            let entry_count = (child_count - first_child).min(BRANCH_CAPACITY);
            for entry_index in 0..entry_count {
                let child = child_start + first_child + entry_index;
                let child_ref = u32::try_from(child).unwrap();
                destination.branch_scratch[entry_index] = BranchEntry {
                    maximum: PageNumberIndex::page_maximum(
                        &destination.workspace.pages[child_ref as usize],
                    ),
                    child: child_ref,
                };
            }
            let entries = destination.branch_scratch;
            let branch_ref = destination.allocate_page(PageNumberIndexPageKind::Branch);
            PageNumberIndex::write_branch(
                &mut destination.workspace.pages[branch_ref as usize],
                &entries[..entry_count],
            );
        }
        child_start = parent_start;
        child_count = parent_count;
    }
    destination.root = u32::try_from(child_start).unwrap();
    destination.values = value_count;
    Ok(())
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

        // Insertion splits leave the source with a second branch level. A
        // dense clone has one branch level, so equality must compare values
        // rather than private page layout.
        let mut dense_pages = vec![PageNumberIndexPage::empty(); 265];
        let mut dense_workspace = PageNumberIndexWorkspace::new(&mut dense_pages);
        let mut dense = PageNumberIndex::new(&mut dense_workspace).unwrap();
        clone_page_number_index_into(&mut dense, &mut index).unwrap();
        assert_eq!(dense.logical_page_count(), 265);
        let dense_root = &dense.workspace.pages[dense.root as usize];
        assert_eq!(dense_root.kind, PageNumberIndexPageKind::Branch);
        assert_eq!(
            dense.workspace.pages[PageNumberIndex::branch_entry(dense_root, 0).child as usize].kind,
            PageNumberIndexPageKind::Leaf
        );
        assert_eq!(page_number_indexes_equal(&mut index, &mut dense), Ok(true));
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

    #[test]
    fn equality_detects_mismatched_values() {
        let mut left_pages = [PageNumberIndexPage::empty(); 8];
        let mut right_pages = [PageNumberIndexPage::empty(); 8];
        let mut left_workspace = PageNumberIndexWorkspace::new(&mut left_pages);
        let mut right_workspace = PageNumberIndexWorkspace::new(&mut right_pages);
        let mut left = PageNumberIndex::new(&mut left_workspace).unwrap();
        let mut right = PageNumberIndex::new(&mut right_workspace).unwrap();
        for value in 0..2_048u32 {
            assert_eq!(left.insert(value), Ok(true));
            let other = if value == 1_337 { u32::MAX - 1 } else { value };
            assert_eq!(right.insert(other), Ok(true));
        }
        assert_eq!(page_number_indexes_equal(&mut left, &mut right), Ok(false));
    }

    #[test]
    fn equality_does_not_mask_later_source_failure() {
        let mut left_pages = [PageNumberIndexPage::empty(); 8];
        let mut right_pages = [PageNumberIndexPage::empty(); 8];
        let mut left_workspace = PageNumberIndexWorkspace::new(&mut left_pages);
        let mut right_workspace = PageNumberIndexWorkspace::new(&mut right_pages);
        let mut left = PageNumberIndex::new(&mut left_workspace).unwrap();
        let mut right = PageNumberIndex::new(&mut right_workspace).unwrap();
        for value in 0..2_048u32 {
            assert_eq!(left.insert(value), Ok(true));
            let other = if value == 1 { u32::MAX - 1 } else { value };
            assert_eq!(right.insert(other), Ok(true));
        }
        let second_leaf = {
            let root = &right.workspace.pages[right.root as usize];
            assert_eq!(root.kind, PageNumberIndexPageKind::Branch);
            assert!(root.count >= 2);
            PageNumberIndex::branch_entry(root, 1).child
        };
        right.workspace.pages[second_leaf as usize].kind = PageNumberIndexPageKind::Empty;

        assert!(page_number_indexes_equal(&mut left, &mut right).is_err());
    }

    #[test]
    fn clone_capacity_rejection_is_pre_mutation() {
        let mut source_pages = [PageNumberIndexPage::empty(); 16];
        let mut source_workspace = PageNumberIndexWorkspace::new(&mut source_pages);
        let mut source = PageNumberIndex::new(&mut source_workspace).unwrap();
        for value in 0..4_096u32 {
            assert_eq!(source.insert(value), Ok(true));
        }

        let mut destination_pages = [PageNumberIndexPage::empty(); 4];
        let mut destination_workspace = PageNumberIndexWorkspace::new(&mut destination_pages);
        let mut destination = PageNumberIndex::new(&mut destination_workspace).unwrap();
        let before = destination.workspace.pages.to_vec();
        assert_eq!(
            clone_page_number_index_into(&mut destination, &mut source),
            Err(PageNumberIndexError::PageBudget {
                required: 5,
                actual: 4,
            })
        );
        assert_eq!(destination.workspace.pages, before.as_slice());
        assert!(destination.workspace.is_clean());
        assert_eq!(destination.len(), 0);
        assert_eq!(destination.logical_page_count(), 0);
    }

    #[test]
    fn clone_scrubs_destination_on_source_failure() {
        let mut source_pages = [PageNumberIndexPage::empty(); 8];
        let mut source_workspace = PageNumberIndexWorkspace::new(&mut source_pages);
        let mut source = PageNumberIndex::new(&mut source_workspace).unwrap();
        for value in 0..2_048u32 {
            assert_eq!(source.insert(value), Ok(true));
        }
        let second_leaf = {
            let root = &source.workspace.pages[source.root as usize];
            assert_eq!(root.kind, PageNumberIndexPageKind::Branch);
            assert!(root.count >= 2);
            PageNumberIndex::branch_entry(root, 1).child
        };
        source.workspace.pages[second_leaf as usize].kind = PageNumberIndexPageKind::Empty;

        let mut destination_pages = [PageNumberIndexPage::empty(); 4];
        let mut destination_workspace = PageNumberIndexWorkspace::new(&mut destination_pages);
        let mut destination = PageNumberIndex::new(&mut destination_workspace).unwrap();
        assert!(clone_page_number_index_into(&mut destination, &mut source).is_err());
        assert!(destination.workspace.is_clean());
        assert_eq!(destination.len(), 0);
        assert_eq!(destination.logical_page_count(), 0);
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

    fn clone_and_compare_no_alloc(
        source: &mut PageNumberIndex<'_, '_>,
        destination: &mut PageNumberIndex<'_, '_>,
    ) -> bool {
        destination.discard_after_abort();
        if clone_page_number_index_into(destination, source).is_err() {
            return false;
        }
        let equal = page_number_indexes_equal(source, destination) == Ok(true);
        destination.discard_after_abort();
        equal
    }

    #[test]
    fn clone_and_equality_use_no_heap_after_workspace_setup() {
        let mut source_pages = [PageNumberIndexPage::empty(); 8];
        let mut destination_pages = [PageNumberIndexPage::empty(); 8];
        let mut source_workspace = PageNumberIndexWorkspace::new(&mut source_pages);
        let mut destination_workspace = PageNumberIndexWorkspace::new(&mut destination_pages);
        let mut source = PageNumberIndex::new(&mut source_workspace).unwrap();
        let mut destination = PageNumberIndex::new(&mut destination_workspace).unwrap();
        assert!(fill_no_alloc(&mut source));
        assert!(clone_and_compare_no_alloc(&mut source, &mut destination));
        let (ok, allocations) =
            count_thread_allocations(|| clone_and_compare_no_alloc(&mut source, &mut destination));
        assert!(ok);
        assert_eq!(allocations, 0);
    }
}
