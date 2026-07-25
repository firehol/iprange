//! Fixed-tree deletion without occupancy rebalancing.

use crate::contract::PAGE_SIZE;
use crate::error::{Error, Result};

use super::insert::{propagate_first, propagate_first_from};
use super::page::{branch_child, build_remove, key_at, lower_bound, parse};
use super::{predecessor, private_path};
use super::{Codec, Path, RetiredPages, Store};

struct Target {
    path: Path,
    page_number: u32,
    page: [u8; PAGE_SIZE],
    header: crate::slotted_page::Header,
    index: usize,
}

pub(crate) fn delete<C: Codec, S: Store>(
    store: &mut S,
    root: &mut u32,
    key: C::Key,
    retired: &mut RetiredPages,
) -> Result<bool> {
    if !contains::<C, S>(store, *root, key)? {
        return Ok(false);
    }
    let target = locate::<C, S>(store, root, key, retired)?;
    if target.header.item_count > 1 {
        remove_leaf_record::<C, S>(store, root, target)?;
        return Ok(true);
    }
    store.discard_private(target.page_number)?;
    remove_empty_child::<C, S>(store, root, &target.path)?;
    Ok(true)
}

fn contains<C: Codec, S: Store>(store: &S, root: u32, key: C::Key) -> Result<bool> {
    let Some(found) = predecessor::<C, S>(store, root, key)? else {
        return Ok(false);
    };
    Ok(C::read_key(found.as_slice(), 0)? == key)
}

fn locate<C: Codec, S: Store>(
    store: &mut S,
    root: &mut u32,
    key: C::Key,
    retired: &mut RetiredPages,
) -> Result<Target> {
    let (path, leaf_page) = private_path::<C, S>(store, root, key, retired)?;
    let mut page = [0; PAGE_SIZE];
    store.read(leaf_page, &mut page)?;
    let header = parse::<C>(&page, store.target_txn(), Some(0))?;
    let (index, exists) = lower_bound::<C>(&page, &header, key, true)?;
    if !exists {
        return Err(Error::Corrupt("B+tree key disappeared during deletion"));
    }
    Ok(Target {
        path,
        page_number: leaf_page,
        page,
        header,
        index,
    })
}

fn remove_leaf_record<C: Codec, S: Store>(
    store: &mut S,
    root: &mut u32,
    target: Target,
) -> Result<()> {
    let mut output = [0; PAGE_SIZE];
    build_remove::<C>(&target.page, &target.header, target.index, &mut output)?;
    store.write(target.page_number, &output)?;
    if target.index != 0 {
        return Ok(());
    }
    let header = parse::<C>(&output, store.target_txn(), Some(0))?;
    let first = key_at::<C>(&output, &header, 0)?;
    propagate_first::<C, S>(store, root, &target.path, first)?;
    Ok(())
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
            if remove_only_child(store, root, frame.page_number, depth)? {
                return Ok(());
            }
            continue;
        }
        return remove_parent_entry::<C, S>(store, root, path, depth, frame, &parent, &header);
    }
    Ok(())
}

fn remove_only_child<S: Store>(
    store: &mut S,
    root: &mut u32,
    page_number: u32,
    depth: usize,
) -> Result<bool> {
    store.discard_private(page_number)?;
    if depth == 0 {
        *root = 0;
        return Ok(true);
    }
    Ok(false)
}

fn remove_parent_entry<C: Codec, S: Store>(
    store: &mut S,
    root: &mut u32,
    path: &Path,
    depth: usize,
    frame: super::Frame,
    parent: &[u8; PAGE_SIZE],
    header: &crate::slotted_page::Header,
) -> Result<()> {
    let mut output = [0; PAGE_SIZE];
    build_remove::<C>(parent, header, frame.index, &mut output)?;
    let output_header = parse::<C>(&output, store.target_txn(), Some(header.level))?;
    if collapse_root::<C, S>(
        store,
        root,
        depth,
        frame.page_number,
        &output,
        &output_header,
    )? {
        return Ok(());
    }
    store.write(frame.page_number, &output)?;
    update_first_key::<C, S>(
        store,
        root,
        path,
        depth,
        frame.index,
        &output,
        &output_header,
    )
}

fn collapse_root<C: Codec, S: Store>(
    store: &mut S,
    root: &mut u32,
    depth: usize,
    page_number: u32,
    page: &[u8; PAGE_SIZE],
    header: &crate::slotted_page::Header,
) -> Result<bool> {
    if depth != 0 || header.item_count != 1 {
        return Ok(false);
    }
    *root = branch_child::<C>(page, header, 0, store.page_limit())?;
    store.discard_private(page_number)?;
    Ok(true)
}

fn update_first_key<C: Codec, S: Store>(
    store: &mut S,
    root: &mut u32,
    path: &Path,
    depth: usize,
    index: usize,
    page: &[u8; PAGE_SIZE],
    header: &crate::slotted_page::Header,
) -> Result<()> {
    if index != 0 {
        return Ok(());
    }
    let first = key_at::<C>(page, header, 0)?;
    propagate_first_from::<C, S>(store, root, path, depth, first)
}
