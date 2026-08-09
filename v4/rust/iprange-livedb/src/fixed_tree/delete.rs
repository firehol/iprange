//! Fixed-tree deletion without occupancy rebalancing.

use crate::error::{Error, Result};
use crate::mapping::ByteSource;
use crate::page_io::PageEdit;
use crate::slotted_page;

use super::insert::{propagate_first, propagate_first_from};
use super::page::{branch_child, parse};
use super::{first_key, private_path, Codec, Path, RetiredPages, Store};

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
        let frame = path.frames[depth];
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
