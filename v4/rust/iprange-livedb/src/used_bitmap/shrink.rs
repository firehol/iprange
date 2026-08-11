//! Membership namespace shrink after trailing IDs disappear.

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
    shrink(store, root, old_limit, Kind::Membership)
}

pub(crate) fn structure<S: RetiringStore>(
    store: &mut S,
    root: &mut u32,
    old_limit: u64,
) -> Result<u64> {
    shrink(store, root, old_limit, Kind::Structure)
}

fn shrink<S: RetiringStore>(
    store: &mut S,
    root: &mut u32,
    old_limit: u64,
    kind: Kind,
) -> Result<u64> {
    let new_limit = greatest(store, *root, old_limit, kind)?.map_or(1, |id| u64::from(id) + 1);
    if new_limit == old_limit || *root == 0 {
        return Ok(new_limit);
    }
    let mut retired = RetiredPages::new();
    trim_root(store, root, old_limit, new_limit, kind, &mut retired)?;
    if *root != 0 {
        *root = refresh_page(
            store,
            *root,
            required_level(new_limit)?,
            0,
            new_limit,
            kind,
            &mut retired,
        )?;
    }
    store.retire_pages(retired.as_slice())?;
    Ok(new_limit)
}

fn trim_root<S: Store>(
    store: &mut S,
    root: &mut u32,
    old_limit: u64,
    new_limit: u64,
    kind: Kind,
    retired: &mut RetiredPages,
) -> Result<()> {
    let mut level = required_level(old_limit)?;
    let required = required_level(new_limit)?;
    while level > required {
        let (private, header) = touch(store, *root, kind, level, 0, old_limit, retired)?;
        *root = private;
        let page_limit = store.page_limit();
        let child = store.inspect_page(private, |page| {
            let child = branch_child(page, &header, 0, page_limit)?;
            if child == 0 {
                return Err(Error::Corrupt("used bitmap root has no retained child"));
            }
            for index in 1..BRANCH_CHILDREN {
                if branch_child(page, &header, index, page_limit)? != 0 {
                    return Err(Error::Corrupt(
                        "used bitmap root has data above its new limit",
                    ));
                }
            }
            Ok(child)
        })?;
        store.discard_private(private)?;
        *root = child;
        level -= 1;
    }
    Ok(())
}

fn refresh_page<S: Store>(
    store: &mut S,
    page_number: u32,
    level: u16,
    base: u64,
    limit: u64,
    kind: Kind,
    retired: &mut RetiredPages,
) -> Result<u32> {
    let (private, header) = touch(store, page_number, kind, level, base, limit, retired)?;
    if level == 0 {
        return Ok(private);
    }
    let span = coverage(level - 1)?;
    for index in 0..BRANCH_CHILDREN {
        refresh_child(
            store, private, &header, index, level, base, span, limit, kind, retired,
        )?;
    }
    Ok(private)
}

#[allow(clippy::too_many_arguments)]
fn refresh_child<S: Store>(
    store: &mut S,
    page_number: u32,
    header: &Header,
    index: usize,
    level: u16,
    base: u64,
    span: u64,
    limit: u64,
    kind: Kind,
    retired: &mut RetiredPages,
) -> Result<()> {
    let child_base = add_child_base(base, span, index)?;
    let page_limit = store.page_limit();
    let child = store.inspect_page(page_number, |page| {
        branch_child(page, header, index, page_limit)
    })?;
    if child_base >= limit {
        if child != 0 {
            return Err(Error::Corrupt("used bitmap has data above its new limit"));
        }
        return store.update_page(page_number, |page| {
            set_branch_child(page, header, index, 0, false)
        });
    }
    if child_base.saturating_add(span) <= limit {
        return Ok(());
    }
    if child == 0 {
        return store.update_page(page_number, |page| {
            set_branch_child(page, header, index, 0, true)
        });
    }
    let child = refresh_page(store, child, level - 1, child_base, limit, kind, retired)?;
    let candidate = subtree_has_candidate(store, child, kind, child_base, limit)?;
    store.update_page(page_number, |page| {
        set_branch_child(page, header, index, child, candidate)
    })
}
