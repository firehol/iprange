//! One-shot reads through the canonical ordered tree traversal.

use crate::error::{Error, Result};
use crate::mapping::ByteSource;
use crate::slotted_page::{self, Header};

#[cfg(test)]
use super::{query, LeafQuery};
use super::{
    Codec, Cursor, CursorDirection as Direction, CursorItem as Item, CursorSeek as Seek,
    PageSource, Path, SeekPosition, Store,
};

#[derive(Clone, Copy)]
pub(super) enum Adjacent {
    Before,
    After,
}

#[derive(Clone, Copy)]
pub(crate) struct LeafLocation {
    pub(super) page_number: u32,
    pub(super) header: slotted_page::Header,
    pub(super) index: usize,
}

pub(crate) fn predecessor<C: Codec, S: PageSource>(
    store: &S,
    root: u32,
    key: C::Key,
) -> Result<Option<C::Leaf>> {
    Cursor::<C>::lookup(
        store,
        root,
        Direction::Backward,
        key,
        &mut Previous,
        &mut ReadLeaf,
    )
}

pub(crate) fn predecessor_located<C: Codec, S: PageSource>(
    store: &S,
    root: u32,
    key: C::Key,
) -> Result<Option<(LeafLocation, C::Leaf)>> {
    Cursor::<C>::lookup(
        store,
        root,
        Direction::Backward,
        key,
        &mut Previous,
        &mut ReadLocated,
    )
}

#[cfg(test)]
pub(super) fn contains<C: Codec, S: PageSource>(store: &S, root: u32, key: C::Key) -> Result<bool> {
    query::<C, S, _>(store, root, key, &mut Exact).map(|found| found.is_some())
}

pub(crate) fn at_or_after<C: Codec, S: PageSource>(
    store: &S,
    root: u32,
    key: C::Key,
) -> Result<Option<C::Leaf>> {
    Cursor::<C>::lookup(
        store,
        root,
        Direction::Forward,
        key,
        &mut CurrentOrNext,
        &mut ReadLeaf,
    )
}

pub(crate) fn inspect_leaf<'a, C: Codec, S: PageSource, T, F>(
    store: &'a S,
    location: LeafLocation,
    inspect: F,
) -> Result<T>
where
    F: FnOnce(crate::mapping::ByteRange<S::Page<'a>>) -> Result<T>,
{
    store.view_page(location.page_number, |page| {
        let header = super::page::parse::<C, _>(page, store.selected_txn(), Some(0))?;
        if header.item_count != location.header.item_count {
            return Err(Error::Corrupt("B+tree leaf changed during inspection"));
        }
        inspect(C::leaf_cell(page, &header, location.index)?)
    })
}

pub(super) fn adjacent_leaf<C: Codec, S: Store>(
    store: &S,
    path: &Path,
    direction: Adjacent,
) -> Result<Option<(C::Key, C::Leaf)>> {
    let mut depth = path.depth;
    while depth > 0 {
        depth -= 1;
        let frame = path.frame(depth);
        let sibling = match direction {
            Adjacent::Before => frame.index.checked_sub(1),
            Adjacent::After => (frame.index + 1 < frame.item_count).then_some(frame.index + 1),
        };
        let Some(sibling) = sibling else {
            continue;
        };
        let (mut page_number, mut expected_level) =
            store.inspect_page(frame.page_number, |page| {
                let header = super::page::parse::<C, _>(page, store.target_txn(), None)?;
                if header.item_count != frame.item_count || header.level == 0 {
                    return Err(Error::Corrupt(
                        "B+tree path changed during adjacent-leaf traversal",
                    ));
                }
                let child =
                    super::page::branch_child::<C, _>(page, &header, sibling, store.page_limit())?;
                Ok((child, header.level - 1))
            })?;
        loop {
            let step = store.inspect_page(page_number, |page| {
                let header =
                    super::page::parse::<C, _>(page, store.target_txn(), Some(expected_level))?;
                let index = match direction {
                    Adjacent::Before => header.item_count - 1,
                    Adjacent::After => 0,
                };
                let cell = super::page::codec_cell::<C, _>(page, &header, index)?;
                if header.level == 0 {
                    return Ok(AdjacentStep::Leaf((
                        C::read_key(cell, 0)?,
                        C::read_leaf(cell)?,
                    )));
                }
                Ok(AdjacentStep::Branch(super::page::branch_child::<C, _>(
                    page,
                    &header,
                    index,
                    store.page_limit(),
                )?))
            })?;
            match step {
                AdjacentStep::Leaf(leaf) => return Ok(Some(leaf)),
                AdjacentStep::Branch(child) => {
                    page_number = child;
                    expected_level -= 1;
                    crate::work::tree_descent(1);
                }
            }
        }
    }
    Ok(None)
}

enum AdjacentStep<K, L> {
    Leaf((K, L)),
    Branch(u32),
}

struct Previous;

impl<C: Codec> Seek<C> for Previous {
    fn select<S: ByteSource>(
        &mut self,
        _page: S,
        _header: &Header,
        position: usize,
        exact: bool,
        _direction: Direction,
    ) -> Result<SeekPosition> {
        if exact {
            Ok(SeekPosition::Index(position))
        } else {
            Ok(position
                .checked_sub(1)
                .map_or(SeekPosition::Finished, SeekPosition::Index))
        }
    }
}

struct CurrentOrNext;

impl<C: Codec> Seek<C> for CurrentOrNext {
    fn select<S: ByteSource>(
        &mut self,
        _page: S,
        header: &Header,
        position: usize,
        _exact: bool,
        _direction: Direction,
    ) -> Result<SeekPosition> {
        Ok(if position < header.item_count {
            SeekPosition::Index(position)
        } else {
            SeekPosition::NextLeaf
        })
    }
}

#[cfg(test)]
struct Exact;

#[cfg(test)]
impl<C: Codec> LeafQuery<C> for Exact {
    type Output = ();

    fn inspect<S: ByteSource>(
        &mut self,
        _page: S,
        _header: &Header,
        _page_number: u32,
        _position: usize,
        exact: bool,
    ) -> Result<Option<Self::Output>> {
        Ok(exact.then_some(()))
    }
}

struct ReadLeaf;

impl<C: Codec> Item<C> for ReadLeaf {
    type Output = C::Leaf;

    fn read<S: ByteSource>(
        &mut self,
        page: S,
        header: &Header,
        _page_number: u32,
        index: usize,
    ) -> Result<Self::Output> {
        C::read_leaf(C::leaf_cell(page, header, index)?)
    }
}

struct ReadLocated;

impl<C: Codec> Item<C> for ReadLocated {
    type Output = (LeafLocation, C::Leaf);

    fn read<S: ByteSource>(
        &mut self,
        page: S,
        header: &Header,
        page_number: u32,
        index: usize,
    ) -> Result<Self::Output> {
        Ok((
            LeafLocation {
                page_number,
                header: *header,
                index,
            },
            C::read_leaf(C::leaf_cell(page, header, index)?)?,
        ))
    }
}
