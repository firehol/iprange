//! Atomic maintenance of the two feed-catalog indexes.

use crate::error::{Error, Result};
use crate::feed::{FeedEntry, FeedName};
use crate::fixed_tree::{self, RetiringStore};

use super::codec::{Encoded, IndexCodec, NameCodec};

pub(crate) fn insert<S: RetiringStore>(
    store: &mut S,
    name_root: &mut u32,
    index_root: &mut u32,
    entry: FeedEntry,
) -> Result<()> {
    let record = Encoded::new(entry)?;
    if !fixed_tree::insert_retiring::<NameCodec, S>(store, name_root, record.as_slice())? {
        return Err(Error::Corrupt("feed name already exists"));
    }
    if !fixed_tree::insert_retiring::<IndexCodec, S>(store, index_root, record.as_slice())? {
        return Err(Error::Corrupt("feed index already exists"));
    }
    crate::work::catalog_intern(1);
    Ok(())
}

pub(crate) fn delete<S: RetiringStore>(
    store: &mut S,
    name_root: &mut u32,
    index_root: &mut u32,
    entry: FeedEntry,
) -> Result<()> {
    fixed_tree::delete_retiring::<NameCodec, S>(store, name_root, entry.name)?;
    fixed_tree::delete_retiring::<IndexCodec, S>(store, index_root, entry.index)?;
    Ok(())
}

pub(crate) fn rename<S: RetiringStore>(
    store: &mut S,
    name_root: &mut u32,
    index_root: &mut u32,
    old: FeedEntry,
    new_name: FeedName,
) -> Result<()> {
    fixed_tree::delete_retiring::<NameCodec, S>(store, name_root, old.name)?;
    let renamed = FeedEntry {
        name: new_name,
        index: old.index,
    };
    let record = Encoded::new(renamed)?;
    if !fixed_tree::insert_retiring::<NameCodec, S>(store, name_root, record.as_slice())? {
        return Err(Error::Corrupt("renamed feed name already exists"));
    }
    if fixed_tree::insert_retiring::<IndexCodec, S>(store, index_root, record.as_slice())? {
        return Err(Error::Corrupt("renamed feed index was missing"));
    }
    Ok(())
}
