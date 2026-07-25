//! Membership namespace shrink after trailing IDs disappear.

use crate::contract::PAGE_SIZE;
use crate::error::{Error, Result};
use crate::fixed_tree::{RetiredPages, RetiringStore, Store};

use super::page::{branch_child, set_branch_child, Header, BRANCH_CHILDREN};
use super::search::greatest;
use super::{add_child_base, coverage, required_level, subtree_has_candidate, touch, Kind};

pub(crate) fn membership<S: RetiringStore>(
    store: &mut S,
    root: &mut u32,
    old_limit: u64,
) -> Result<u64> {
    let new_limit =
        greatest(store, *root, old_limit, Kind::Membership)?.map_or(1, |id| u64::from(id) + 1);
    if new_limit == old_limit || *root == 0 {
        return Ok(new_limit);
    }
    let mut retired = RetiredPages::new();
    trim_root(store, root, old_limit, new_limit, &mut retired)?;
    refresh_limit(store, root, new_limit, &mut retired)?;
    store.retire_pages(retired.as_slice())?;
    Ok(new_limit)
}

fn trim_root<S: Store>(
    store: &mut S,
    root: &mut u32,
    old_limit: u64,
    new_limit: u64,
    retired: &mut RetiredPages,
) -> Result<()> {
    let mut level = required_level(old_limit)?;
    let required = required_level(new_limit)?;
    while level > required {
        let (private, page, header) =
            touch(store, *root, Kind::Membership, level, 0, old_limit, retired)?;
        *root = private;
        let child = branch_child(&page, &header, 0, store.page_limit())?;
        require_only_first_child(store, &page, &header, child)?;
        store.discard_private(private)?;
        *root = child;
        level -= 1;
    }
    Ok(())
}

fn require_only_first_child<S: Store>(
    store: &S,
    page: &[u8; PAGE_SIZE],
    header: &Header,
    child: u32,
) -> Result<()> {
    if child == 0 {
        return Err(Error::Corrupt("used bitmap root has no retained child"));
    }
    for index in 1..BRANCH_CHILDREN {
        if branch_child(page, header, index, store.page_limit())? != 0 {
            return Err(Error::Corrupt(
                "used bitmap root has data above its new limit",
            ));
        }
    }
    Ok(())
}

fn refresh_limit<S: Store>(
    store: &mut S,
    root: &mut u32,
    limit: u64,
    retired: &mut RetiredPages,
) -> Result<()> {
    if *root != 0 {
        *root = refresh_page(store, *root, required_level(limit)?, 0, limit, retired)?;
    }
    Ok(())
}

fn refresh_page<S: Store>(
    store: &mut S,
    page_number: u32,
    level: u16,
    base: u64,
    limit: u64,
    retired: &mut RetiredPages,
) -> Result<u32> {
    let kind = Kind::Membership;
    let (private, mut page, header) = touch(store, page_number, kind, level, base, limit, retired)?;
    if level == 0 {
        return Ok(private);
    }
    let span = coverage(level - 1)?;
    for index in 0..BRANCH_CHILDREN {
        refresh_child(
            store, &mut page, &header, index, level, base, span, limit, retired,
        )?;
    }
    store.write(private, &page)?;
    Ok(private)
}

#[allow(clippy::too_many_arguments)]
fn refresh_child<S: Store>(
    store: &mut S,
    page: &mut [u8; PAGE_SIZE],
    header: &Header,
    index: usize,
    level: u16,
    base: u64,
    span: u64,
    limit: u64,
    retired: &mut RetiredPages,
) -> Result<()> {
    let child_base = add_child_base(base, span, index)?;
    let child = branch_child(page, header, index, store.page_limit())?;
    if child_base >= limit {
        require_absent(child)?;
        return set_branch_child(page, header, index, 0, false);
    }
    if child_base.saturating_add(span) <= limit {
        return Ok(());
    }
    refresh_boundary(
        store, page, header, index, child, level, child_base, limit, retired,
    )
}

#[allow(clippy::too_many_arguments)]
fn refresh_boundary<S: Store>(
    store: &mut S,
    page: &mut [u8; PAGE_SIZE],
    header: &Header,
    index: usize,
    child: u32,
    level: u16,
    child_base: u64,
    limit: u64,
    retired: &mut RetiredPages,
) -> Result<()> {
    if child == 0 {
        return set_branch_child(page, header, index, 0, true);
    }
    let child = refresh_page(store, child, level - 1, child_base, limit, retired)?;
    let candidate = subtree_has_candidate(store, child, Kind::Membership, child_base, limit)?;
    set_branch_child(page, header, index, child, candidate)
}

fn require_absent(child: u32) -> Result<()> {
    if child == 0 {
        Ok(())
    } else {
        Err(Error::Corrupt("used bitmap has data above its new limit"))
    }
}
