//! Fixed-tree deletion without occupancy rebalancing.

use crate::error::{Error, Result};
use crate::mapping::ByteSource;
use crate::page_io::PageEdit;
use crate::slotted_page;

use super::insert::{propagate_first, propagate_first_from};
use super::page::{branch_child, codec_cell, parse};
use super::read::{adjacent_leaf, Adjacent};
use super::{first_key, private_path, Codec, Path, RetiredPages, Store};

pub(crate) struct Following<K, L> {
    pub(crate) key: K,
    pub(crate) leaf: L,
}

pub(crate) struct RemovedRun<K, L> {
    pub(crate) removed: u64,
    pub(crate) following: Option<Following<K, L>>,
}

pub(super) struct Target {
    pub(super) path: Path,
    pub(super) page_number: u32,
    pub(super) header: crate::slotted_page::Header,
    pub(super) index: usize,
}

#[cfg(test)]
pub(crate) fn delete<C: Codec, S: Store>(
    store: &mut S,
    root: &mut u32,
    key: C::Key,
    retired: &mut RetiredPages,
) -> Result<bool> {
    if !super::read::contains::<C, S>(store, *root, key)? {
        return Ok(false);
    }
    delete_existing::<C, S>(store, root, key, retired)?;
    Ok(true)
}

pub(crate) fn delete_existing<C: Codec, S: Store>(
    store: &mut S,
    root: &mut u32,
    key: C::Key,
    retired: &mut RetiredPages,
) -> Result<()> {
    let target = locate::<C, S>(store, root, key, retired)?;
    delete_target::<C, S>(store, root, target)
}

pub(crate) fn remove_leaf_run<C, S, F>(
    store: &mut S,
    root: &mut u32,
    key: C::Key,
    mut include: F,
) -> Result<RemovedRun<C::Key, C::Leaf>>
where
    C: Codec,
    S: Store,
    F: FnMut(C::Leaf) -> Result<bool>,
{
    let mut retired = RetiredPages::new();
    let leaf = private_path::<C, S>(store, root, key, &mut retired)?;
    if !retired.as_slice().is_empty() {
        return Err(Error::Corrupt("private B+tree run retired a page"));
    }
    let (index, exists) = leaf.selection;
    if !exists {
        return Err(Error::Corrupt("B+tree run start key is missing"));
    }
    let (end, following) = store.inspect_page(leaf.page_number, |page| {
        let mut end = index;
        while end < leaf.header.item_count {
            let cell = codec_cell::<C, _>(page, &leaf.header, end)?;
            let item = C::read_leaf(cell)?;
            if !include(item)? {
                return Ok((
                    end,
                    Some(Following {
                        key: C::read_key(cell, 0)?,
                        leaf: item,
                    }),
                ));
            }
            end += 1;
        }
        Ok((end, None))
    })?;
    if end == index {
        return Ok(RemovedRun {
            removed: 0,
            following,
        });
    }
    let following = match following {
        Some(following) => Some(following),
        None => adjacent_leaf::<C, S>(store, &leaf.path, Adjacent::After)?
            .map(|(key, leaf)| Following { key, leaf }),
    };
    let removed =
        u64::try_from(end - index).map_err(|_| Error::arithmetic_overflow("B+tree removed run"))?;
    if end - index == leaf.header.item_count {
        store.discard_private(leaf.page_number)?;
        remove_empty_child::<C, S>(store, root, &leaf.path)?;
    } else {
        let cell_len = C::fixed_cell_size(0).ok_or(Error::Unsupported(
            "B+tree run removal requires fixed leaf cells",
        ))?;
        store.update_page(leaf.page_number, |page| {
            slotted_page::remove_fixed_range(page, &leaf.header, index, end - index, cell_len)
                .map(drop)
        })?;
        if index == 0 {
            let first = first_key::<C, S>(store, leaf.page_number, 0)?;
            propagate_first::<C, S>(store, root, &leaf.path, first)?;
        }
    }
    Ok(RemovedRun { removed, following })
}

pub(super) fn delete_target<C: Codec, S: Store>(
    store: &mut S,
    root: &mut u32,
    target: Target,
) -> Result<()> {
    if target.header.item_count > 1 {
        remove_leaf_record::<C, S>(store, root, target)?;
        return Ok(());
    }
    store.discard_private(target.page_number)?;
    remove_empty_child::<C, S>(store, root, &target.path)
}

fn locate<C: Codec, S: Store>(
    store: &mut S,
    root: &mut u32,
    key: C::Key,
    retired: &mut RetiredPages,
) -> Result<Target> {
    let leaf = private_path::<C, S>(store, root, key, retired)?;
    let (index, exists) = leaf.selection;
    if !exists {
        return Err(Error::Corrupt("B+tree key disappeared during deletion"));
    }
    Ok(Target {
        path: leaf.path,
        page_number: leaf.page_number,
        header: leaf.header,
        index,
    })
}

fn remove_leaf_record<C: Codec, S: Store>(
    store: &mut S,
    root: &mut u32,
    target: Target,
) -> Result<()> {
    store.update_page(target.page_number, |page| {
        remove_at::<C, _>(page, &target.header, target.index)
    })?;
    if target.index != 0 {
        return Ok(());
    }
    let first = first_key::<C, S>(store, target.page_number, 0)?;
    propagate_first::<C, S>(store, root, &target.path, first)
}

fn remove_empty_child<C: Codec, S: Store>(
    store: &mut S,
    root: &mut u32,
    path: &Path,
) -> Result<()> {
    let mut depth = path.depth;
    if depth == 0 {
        *root = 0;
        return Ok(());
    }

    while depth > 0 {
        depth -= 1;
        let frame = path.frame(depth);
        let target_txn = store.target_txn();
        let header = store.inspect_page(frame.page_number, |page| {
            parse::<C, _>(page, target_txn, None)
        })?;
        if header.item_count == 1 {
            store.discard_private(frame.page_number)?;
            if depth == 0 {
                *root = 0;
                return Ok(());
            }
            continue;
        }

        store.update_page(frame.page_number, |page| {
            remove_at::<C, _>(page, &header, frame.index)
        })?;
        let output_count = header.item_count - 1;
        if depth == 0 && output_count == 1 {
            let child = store.inspect_page(frame.page_number, |page| {
                let output = parse::<C, _>(page, target_txn, Some(header.level))?;
                branch_child::<C, _>(page, &output, 0, store.page_limit())
            })?;
            *root = child;
            store.discard_private(frame.page_number)?;
            return Ok(());
        }
        if frame.index == 0 {
            let first = first_key::<C, S>(store, frame.page_number, header.level)?;
            propagate_first_from::<C, S>(store, root, path, depth, first)?;
        }
        return Ok(());
    }
    Ok(())
}

fn remove_at<C: Codec, D: PageEdit>(
    page: &mut D,
    header: &crate::slotted_page::Header,
    index: usize,
) -> Result<()> {
    let old_len = super::page::codec_cell::<C, _>(page.view(), header, index)?.len();
    slotted_page::remove(page, header, index, old_len)
}
