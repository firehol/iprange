//! Fixed-tree deletion without occupancy rebalancing.

use crate::contract::PAGE_SIZE;
use crate::error::{Error, Result};

use super::page::{branch_child, build_remove, key_at, lower_bound, parse};
use super::{predecessor, private_path, propagate_first, propagate_first_from};
use super::{Codec, Path, RetiredPages, Store};

pub(crate) fn delete<C: Codec, S: Store>(
    store: &mut S,
    root: &mut u32,
    key: C::Key,
    retired: &mut RetiredPages,
) -> Result<bool> {
    let Some(found) = predecessor::<C, S>(store, *root, key)? else {
        return Ok(false);
    };
    if C::read_key(found.as_slice()) != key {
        return Ok(false);
    }

    let (path, leaf_page) = private_path::<C, S>(store, root, key, retired)?;
    let mut leaf = [0; PAGE_SIZE];
    store.read(leaf_page, &mut leaf)?;
    let header = parse::<C>(&leaf, store.target_txn(), Some(0))?;
    let (index, exists) = lower_bound::<C>(&leaf, &header, key, true)?;
    if !exists {
        return Err(Error::Corrupt("B+tree key disappeared during deletion"));
    }
    if header.item_count > 1 {
        let mut output = [0; PAGE_SIZE];
        build_remove::<C>(&leaf, &header, index, &mut output)?;
        store.write(leaf_page, &output)?;
        if index == 0 {
            let output_header = parse::<C>(&output, store.target_txn(), Some(0))?;
            propagate_first::<C, S>(store, &path, key_at::<C>(&output, &output_header, 0)?)?;
        }
        return Ok(true);
    }

    store.discard_private(leaf_page)?;
    remove_empty_child::<C, S>(store, root, &path)?;
    Ok(true)
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
        let mut parent = [0; PAGE_SIZE];
        store.read(frame.page_number, &mut parent)?;
        let header = parse::<C>(&parent, store.target_txn(), None)?;
        if header.item_count == 1 {
            store.discard_private(frame.page_number)?;
            if depth == 0 {
                *root = 0;
                return Ok(());
            }
            continue;
        }

        let mut output = [0; PAGE_SIZE];
        build_remove::<C>(&parent, &header, frame.index, &mut output)?;
        let output_header = parse::<C>(&output, store.target_txn(), Some(header.level))?;
        if depth == 0 && output_header.item_count == 1 {
            *root = branch_child::<C>(&output, &output_header, 0, store.page_limit())?;
            store.discard_private(frame.page_number)?;
            return Ok(());
        }
        store.write(frame.page_number, &output)?;
        if frame.index == 0 {
            let first = key_at::<C>(&output, &output_header, 0)?;
            propagate_first_from::<C, S>(store, path, depth, first)?;
        }
        return Ok(());
    }
    Ok(())
}
