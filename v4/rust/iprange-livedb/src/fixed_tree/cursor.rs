//! One allocation-free ordered cursor for healthy fixed trees.

use std::marker::PhantomData;

use crate::contract::MAX_TREE_LEVEL;
use crate::error::{Error, Result};
use crate::mapping::{ByteSource, Mapping, PageView};
use crate::page_header;
use crate::slotted_page::Header;

use super::page::{branch_child, lower_bound, parse};
use super::{Codec, PageSource, Store};

const MAX_DEPTH: usize = MAX_TREE_LEVEL as usize;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Direction {
    Forward,
    Backward,
}

pub(crate) trait Item<C: Codec> {
    type Output;

    fn read<S: ByteSource>(
        &mut self,
        page: S,
        header: &Header,
        page_number: u32,
        index: usize,
    ) -> Result<Self::Output>;
}

pub(crate) enum SeekPosition {
    Index(usize),
    NextLeaf,
    Finished,
}

pub(crate) trait Seek<C: Codec> {
    fn select<S: ByteSource>(
        &mut self,
        page: S,
        header: &Header,
        position: usize,
        exact: bool,
        direction: Direction,
    ) -> Result<SeekPosition>;
}

#[derive(Clone, Copy)]
struct Frame {
    page_number: u32,
    index: usize,
    item_count: usize,
    level: u16,
}

const EMPTY_FRAME: Frame = Frame {
    page_number: 0,
    index: 0,
    item_count: 0,
    level: 0,
};

#[derive(Clone, Copy)]
struct Leaf {
    page_number: u32,
    header: Header,
}

pub(crate) struct Cursor<C> {
    selected_txn: u64,
    page_limit: u64,
    root: u32,
    direction: Direction,
    path: [Frame; MAX_DEPTH],
    depth: usize,
    leaf: Option<Leaf>,
    index: usize,
    needs_advance: bool,
    finished: bool,
    codec: PhantomData<C>,
}

impl<C: Codec> Cursor<C> {
    pub(crate) fn new<S: PageSource>(source: &S, root: u32, direction: Direction) -> Result<Self> {
        Self::open(&mut Shared(source), root, direction)
    }

    pub(crate) fn new_consuming<S: Store>(
        store: &mut S,
        root: u32,
        direction: Direction,
    ) -> Result<Self> {
        let selected_txn = store.target_txn();
        let page_limit = store.page_limit();
        Self::open(
            &mut Consuming::new(store, selected_txn, page_limit),
            root,
            direction,
        )
    }

    pub(crate) fn lookup<S, Q, I>(
        source: &S,
        root: u32,
        direction: Direction,
        key: C::Key,
        policy: &mut Q,
        item: &mut I,
    ) -> Result<Option<I::Output>>
    where
        S: PageSource,
        Q: Seek<C>,
        I: Item<C>,
    {
        let mut access = Shared(source);
        let mut cursor = Self::unpositioned(&access, root, direction)?;
        let result = cursor.seek_read_inner(&mut access, key, policy, item);
        if result.is_err() {
            cursor.finished = true;
        }
        result
    }

    fn open<A: Access>(access: &mut A, root: u32, direction: Direction) -> Result<Self> {
        let mut cursor = Self::unpositioned(access, root, direction)?;
        if !cursor.finished {
            cursor.descend_edge(access, root, None)?;
        }
        crate::work::source_pass(1);
        Ok(cursor)
    }

    fn unpositioned<A: Access>(access: &A, root: u32, direction: Direction) -> Result<Self> {
        if root != 0 && (root < 2 || u64::from(root) >= access.page_limit()) {
            return Err(Error::Corrupt("B+tree root is outside page bounds"));
        }
        Ok(Self {
            selected_txn: access.selected_txn(),
            page_limit: access.page_limit(),
            root,
            direction,
            path: [EMPTY_FRAME; MAX_DEPTH],
            depth: 0,
            leaf: None,
            index: 0,
            needs_advance: false,
            finished: root == 0,
            codec: PhantomData,
        })
    }

    pub(crate) fn seek<S, Q>(&mut self, source: &S, key: C::Key, policy: &mut Q) -> Result<()>
    where
        S: PageSource,
        Q: Seek<C>,
    {
        let result = self.seek_inner(&mut Shared(source), key, policy);
        if result.is_err() {
            self.finished = true;
        }
        result
    }

    pub(crate) fn next<S, Q>(&mut self, source: &S, item: &mut Q) -> Result<Option<Q::Output>>
    where
        S: PageSource,
        Q: Item<C>,
    {
        let result = self.next_inner(&mut Shared(source), item, false);
        if result.is_err() {
            self.finished = true;
        }
        result
    }

    pub(crate) fn next_leaf_mapped(&mut self, mapping: &Mapping) -> Result<Option<C::Leaf>> {
        let result = self.next_leaf_mapped_inner(mapping);
        if result.is_err() {
            self.finished = true;
        }
        result
    }

    pub(crate) fn next_consuming<S, Q>(
        &mut self,
        store: &mut S,
        item: &mut Q,
    ) -> Result<Option<Q::Output>>
    where
        S: Store,
        Q: Item<C>,
    {
        if store.target_txn() != self.selected_txn || store.page_limit() < self.page_limit {
            self.finished = true;
            return Err(Error::Corrupt("B+tree cursor source changed"));
        }
        let result = self.next_inner(
            &mut Consuming::new(store, self.selected_txn, self.page_limit),
            item,
            true,
        );
        if result.is_err() {
            self.finished = true;
        }
        result
    }

    pub(crate) const fn finished(&self) -> bool {
        self.finished
    }

    fn seek_inner<A, Q>(&mut self, access: &mut A, key: C::Key, policy: &mut Q) -> Result<()>
    where
        A: Access,
        Q: Seek<C>,
    {
        self.seek_read_inner(access, key, policy, &mut IgnoreItem)
            .map(drop)
    }

    fn seek_read_inner<A, Q, I>(
        &mut self,
        access: &mut A,
        key: C::Key,
        policy: &mut Q,
        item: &mut I,
    ) -> Result<Option<I::Output>>
    where
        A: Access,
        Q: Seek<C>,
        I: Item<C>,
    {
        self.require_access(access)?;
        crate::work::tree_lookup(1);
        self.depth = 0;
        self.leaf = None;
        self.needs_advance = false;
        self.finished = self.root == 0;
        if self.finished {
            return Ok(None);
        }

        let mut page_number = self.root;
        let mut expected_level = None;
        loop {
            let step = access.view_page(page_number, |page| {
                let header = parse::<C, _>(page, self.selected_txn, expected_level)?;
                if header.level == 0 {
                    let (position, exact) = lower_bound::<C, _>(page, &header, key, true)?;
                    let selected = policy.select(page, &header, position, exact, self.direction)?;
                    let output = match selected {
                        SeekPosition::Index(index) => {
                            if index >= header.item_count {
                                return Err(Error::Corrupt("B+tree seek index is invalid"));
                            }
                            Some(item.read(page, &header, page_number, index)?)
                        }
                        SeekPosition::NextLeaf | SeekPosition::Finished => None,
                    };
                    return Ok(SeekStep::Leaf {
                        header,
                        selected,
                        output,
                    });
                }
                let (position, exact) = lower_bound::<C, _>(page, &header, key, true)?;
                let found = if exact {
                    Some(position)
                } else {
                    position.checked_sub(1)
                };
                let index = match (found, self.direction) {
                    (Some(index), _) => index,
                    (None, Direction::Forward) => 0,
                    (None, Direction::Backward) => return Ok(SeekStep::Finished),
                };
                let child = branch_child::<C, _>(page, &header, index, self.page_limit)?;
                Ok(SeekStep::Branch {
                    header,
                    index,
                    child,
                })
            })?;
            match step {
                SeekStep::Finished => {
                    self.finished = true;
                    return Ok(None);
                }
                SeekStep::Leaf {
                    header,
                    selected,
                    output,
                } => {
                    return match selected {
                        SeekPosition::Index(index) => {
                            self.set_leaf(page_number, header, index);
                            Ok(output)
                        }
                        SeekPosition::NextLeaf => {
                            self.set_leaf(page_number, header, header.item_count - 1);
                            self.advance_leaf(access)?;
                            self.read_current(access, item)
                        }
                        SeekPosition::Finished => {
                            self.finished = true;
                            Ok(None)
                        }
                    };
                }
                SeekStep::Branch {
                    header,
                    index,
                    child,
                } => {
                    self.push(page_number, index, &header)?;
                    page_number = child;
                    expected_level = Some(header.level - 1);
                    crate::work::tree_descent(1);
                }
            }
        }
    }

    fn read_current<A, I>(&mut self, access: &A, item: &mut I) -> Result<Option<I::Output>>
    where
        A: Access,
        I: Item<C>,
    {
        if self.finished {
            return Ok(None);
        }
        let leaf = self.leaf.expect("active B+tree cursor has a leaf");
        access
            .view_page(leaf.page_number, |page| {
                item.read(page, &leaf.header, leaf.page_number, self.index)
            })
            .map(Some)
    }

    fn next_inner<A, Q>(
        &mut self,
        access: &mut A,
        item: &mut Q,
        eager_advance: bool,
    ) -> Result<Option<Q::Output>>
    where
        A: Access,
        Q: Item<C>,
    {
        self.require_access(access)?;
        if self.finished {
            return Ok(None);
        }
        if self.needs_advance {
            self.advance(access)?;
            if self.finished {
                return Ok(None);
            }
        }
        let leaf = self.leaf.expect("active B+tree cursor has a leaf");
        let result = access.view_page(leaf.page_number, |page| {
            item.read(page, &leaf.header, leaf.page_number, self.index)
        })?;
        self.needs_advance = true;
        if eager_advance {
            self.advance(access)?;
        }
        Ok(Some(result))
    }

    fn next_leaf_mapped_inner(&mut self, mapping: &Mapping) -> Result<Option<C::Leaf>> {
        if self.finished {
            return Ok(None);
        }
        if self.needs_advance {
            self.advance(&mut Mapped::new(
                mapping,
                self.selected_txn,
                self.page_limit,
            ))?;
            if self.finished {
                return Ok(None);
            }
        }
        let leaf = self.leaf.expect("active B+tree cursor has a leaf");
        let page = mapping.page(leaf.page_number, self.page_limit)?;
        let result = C::read_leaf(C::leaf_cell(page, &leaf.header, self.index)?)?;
        self.needs_advance = true;
        Ok(Some(result))
    }

    fn advance<A: Access>(&mut self, access: &mut A) -> Result<()> {
        let leaf = self.leaf.expect("active B+tree cursor has a leaf");
        let in_leaf = match self.direction {
            Direction::Forward => self.index + 1 < leaf.header.item_count,
            Direction::Backward => self.index > 0,
        };
        if in_leaf {
            match self.direction {
                Direction::Forward => self.index += 1,
                Direction::Backward => self.index -= 1,
            }
            self.needs_advance = false;
            return Ok(());
        }
        access.discard(leaf.page_number)?;
        self.advance_leaf(access)
    }

    fn advance_leaf<A: Access>(&mut self, access: &mut A) -> Result<()> {
        self.leaf = None;
        while self.depth > 0 {
            let slot = self.depth - 1;
            let mut frame = self.path[slot];
            let has_sibling = match self.direction {
                Direction::Forward => frame.index + 1 < frame.item_count,
                Direction::Backward => frame.index > 0,
            };
            if !has_sibling {
                self.depth = slot;
                access.discard(frame.page_number)?;
                continue;
            }
            match self.direction {
                Direction::Forward => frame.index += 1,
                Direction::Backward => frame.index -= 1,
            }
            self.path[slot] = frame;
            self.depth = slot + 1;
            let child = access.view_page(frame.page_number, |page| {
                let header = parse::<C, _>(page, self.selected_txn, Some(frame.level))?;
                if header.item_count != frame.item_count {
                    return Err(Error::Corrupt("B+tree branch changed during traversal"));
                }
                branch_child::<C, _>(page, &header, frame.index, self.page_limit)
            })?;
            crate::work::tree_descent(1);
            return self.descend_edge(access, child, Some(frame.level - 1));
        }
        self.needs_advance = false;
        self.finished = true;
        Ok(())
    }

    fn descend_edge<A: Access>(
        &mut self,
        access: &mut A,
        mut page_number: u32,
        mut expected_level: Option<u16>,
    ) -> Result<()> {
        loop {
            let (header, child) = access.view_page(page_number, |page| {
                let header = parse::<C, _>(page, self.selected_txn, expected_level)?;
                if access
                    .consumed_txn()
                    .is_some_and(|txn| page_header::born_txn(page) != txn)
                {
                    return Err(Error::Corrupt("consumed B+tree contains a committed page"));
                }
                let index = match self.direction {
                    Direction::Forward => 0,
                    Direction::Backward => header.item_count - 1,
                };
                let child = if header.level == 0 {
                    None
                } else {
                    Some(branch_child::<C, _>(page, &header, index, self.page_limit)?)
                };
                Ok((header, child))
            })?;
            let Some(child) = child else {
                let index = match self.direction {
                    Direction::Forward => 0,
                    Direction::Backward => header.item_count - 1,
                };
                self.set_leaf(page_number, header, index);
                return Ok(());
            };
            let index = match self.direction {
                Direction::Forward => 0,
                Direction::Backward => header.item_count - 1,
            };
            self.push(page_number, index, &header)?;
            page_number = child;
            expected_level = Some(header.level - 1);
            crate::work::tree_descent(1);
        }
    }

    fn push(&mut self, page_number: u32, index: usize, header: &Header) -> Result<()> {
        let frame = self
            .path
            .get_mut(self.depth)
            .ok_or_else(|| Error::corrupt("B+tree exceeds its maximum height"))?;
        *frame = Frame {
            page_number,
            index,
            item_count: header.item_count,
            level: header.level,
        };
        self.depth += 1;
        Ok(())
    }

    fn set_leaf(&mut self, page_number: u32, header: Header, index: usize) {
        self.leaf = Some(Leaf {
            page_number,
            header,
        });
        self.index = index;
        self.needs_advance = false;
        self.finished = false;
    }

    fn require_access<A: Access>(&self, access: &A) -> Result<()> {
        if access.selected_txn() != self.selected_txn || access.page_limit() != self.page_limit {
            return Err(Error::Corrupt("B+tree cursor source changed"));
        }
        Ok(())
    }
}

struct IgnoreItem;

impl<C: Codec> Item<C> for IgnoreItem {
    type Output = ();

    fn read<S: ByteSource>(
        &mut self,
        _page: S,
        _header: &Header,
        _page_number: u32,
        _index: usize,
    ) -> Result<Self::Output> {
        Ok(())
    }
}

enum SeekStep<T> {
    Finished,
    Leaf {
        header: Header,
        selected: SeekPosition,
        output: Option<T>,
    },
    Branch {
        header: Header,
        index: usize,
        child: u32,
    },
}

trait Access {
    type Page<'a>: ByteSource
    where
        Self: 'a;

    fn selected_txn(&self) -> u64;
    fn page_limit(&self) -> u64;
    fn consumed_txn(&self) -> Option<u64>;
    fn view_page<'a, T, F>(&'a self, page_number: u32, inspect: F) -> Result<T>
    where
        F: FnOnce(Self::Page<'a>) -> Result<T>;
    fn discard(&mut self, page_number: u32) -> Result<()>;
}

struct Shared<'a, S>(&'a S);

impl<S: PageSource> Access for Shared<'_, S> {
    type Page<'a>
        = S::Page<'a>
    where
        Self: 'a;

    fn selected_txn(&self) -> u64 {
        self.0.selected_txn()
    }

    fn page_limit(&self) -> u64 {
        self.0.selected_page_limit()
    }

    fn consumed_txn(&self) -> Option<u64> {
        None
    }

    fn view_page<'a, T, F>(&'a self, page_number: u32, inspect: F) -> Result<T>
    where
        F: FnOnce(Self::Page<'a>) -> Result<T>,
    {
        self.0.view_page(page_number, inspect)
    }

    fn discard(&mut self, _page_number: u32) -> Result<()> {
        Ok(())
    }
}

struct Mapped<'a> {
    mapping: &'a Mapping,
    selected_txn: u64,
    page_limit: u64,
}

impl<'a> Mapped<'a> {
    const fn new(mapping: &'a Mapping, selected_txn: u64, page_limit: u64) -> Self {
        Self {
            mapping,
            selected_txn,
            page_limit,
        }
    }
}

impl Access for Mapped<'_> {
    type Page<'a>
        = PageView<'a>
    where
        Self: 'a;

    fn selected_txn(&self) -> u64 {
        self.selected_txn
    }

    fn page_limit(&self) -> u64 {
        self.page_limit
    }

    fn consumed_txn(&self) -> Option<u64> {
        None
    }

    fn view_page<'a, T, F>(&'a self, page_number: u32, inspect: F) -> Result<T>
    where
        F: FnOnce(Self::Page<'a>) -> Result<T>,
    {
        inspect(self.mapping.page(page_number, self.page_limit)?)
    }

    fn discard(&mut self, _page_number: u32) -> Result<()> {
        Ok(())
    }
}

struct Consuming<'a, S> {
    store: &'a mut S,
    selected_txn: u64,
    page_limit: u64,
}

impl<'a, S> Consuming<'a, S> {
    const fn new(store: &'a mut S, selected_txn: u64, page_limit: u64) -> Self {
        Self {
            store,
            selected_txn,
            page_limit,
        }
    }
}

impl<S: Store> Access for Consuming<'_, S> {
    type Page<'a>
        = S::ReadPage<'a>
    where
        Self: 'a;

    fn selected_txn(&self) -> u64 {
        self.selected_txn
    }

    fn page_limit(&self) -> u64 {
        self.page_limit
    }

    fn consumed_txn(&self) -> Option<u64> {
        Some(self.selected_txn)
    }

    fn view_page<'a, T, F>(&'a self, page_number: u32, inspect: F) -> Result<T>
    where
        F: FnOnce(Self::Page<'a>) -> Result<T>,
    {
        self.store.inspect_page(page_number, inspect)
    }

    fn discard(&mut self, page_number: u32) -> Result<()> {
        self.store.discard_private(page_number)
    }
}
