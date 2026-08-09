//! One mapped root-to-leaf query for every healthy fixed tree.

use crate::contract::MAX_TREE_LEVEL;
use crate::error::{Error, Result};
use crate::mapping::{ByteRange, ByteSource};
use crate::slotted_page::Header;

use super::page::{branch_child, lower_bound, lower_bound_by, parse};
use super::{Codec, PageSource};

pub(crate) trait LeafQuery<C: Codec> {
    type Output;

    fn read_key<S: ByteSource>(&mut self, cell: ByteRange<S>) -> Result<C::Key> {
        C::read_key(cell, 0)
    }

    fn inspect<S: ByteSource>(
        &mut self,
        page: S,
        header: &Header,
        page_number: u32,
        position: usize,
        exact: bool,
    ) -> Result<Option<Self::Output>>;
}

enum Step<T> {
    Found(Option<T>),
    Descend { child: u32, expected_level: u16 },
}

#[inline]
pub(crate) fn query<C, S, Q>(
    source: &S,
    root: u32,
    key: C::Key,
    leaf: &mut Q,
) -> Result<Option<Q::Output>>
where
    C: Codec,
    S: PageSource,
    Q: LeafQuery<C>,
{
    crate::work::tree_lookup(1);
    if root == 0 {
        return Ok(None);
    }

    let mut page_number = root;
    let mut expected_level = None;
    for _ in 0..=MAX_TREE_LEVEL {
        let step = source.view_page(page_number, |page| {
            let header = parse::<C, _>(page, source.selected_txn(), expected_level)?;
            if header.level == 0 {
                let (position, exact) = lower_bound_by::<C, _>(&header, key, true, |index| {
                    leaf.read_key(C::leaf_cell(page, &header, index)?)
                })?;
                return leaf
                    .inspect(page, &header, page_number, position, exact)
                    .map(Step::Found);
            }
            let (position, _) = lower_bound::<C, _>(page, &header, key, false)?;
            let child =
                branch_child::<C, _>(page, &header, position, source.selected_page_limit())?;
            Ok(Step::Descend {
                child,
                expected_level: header.level - 1,
            })
        })?;
        match step {
            Step::Found(result) => return Ok(result),
            Step::Descend {
                child,
                expected_level: level,
            } => {
                page_number = child;
                expected_level = Some(level);
                crate::work::tree_descent(1);
            }
        }
    }
    Err(Error::Corrupt("B+tree exceeds its maximum height"))
}
