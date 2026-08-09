//! Ordered range traversal through a mutable mapped-page store.

use crate::contract::MetaV4;
use crate::error::{Error, Result};
use crate::fixed_tree::{self, CursorDirection, PageSource, Store};
use crate::key::IpKey;
use crate::range_cursor::{DirectRange, RangeItem};
use crate::range_tree::RangeCodec;

pub(crate) struct Cursor<K> {
    meta: MetaV4,
    release_private: bool,
    inner: fixed_tree::Cursor<RangeCodec<K>>,
}

impl<K: IpKey> Cursor<K> {
    pub(crate) fn new<S: Store>(
        store: &mut S,
        meta: &MetaV4,
        release_private: bool,
    ) -> Result<Self> {
        if meta.address_family != K::FAMILY {
            return Err(Error::WrongAddressFamily(
                "stored range cursor has the wrong address family",
            ));
        }
        if release_private
            && (meta.txn_id != store.target_txn() || meta.page_count != store.page_limit())
        {
            return Err(Error::Corrupt(
                "consumed range tree is outside the draft generation",
            ));
        }
        let inner = if release_private {
            fixed_tree::Cursor::new_consuming(store, meta.range_root, CursorDirection::Forward)?
        } else {
            fixed_tree::Cursor::new(
                &SelectedStore::new(&*store, *meta),
                meta.range_root,
                CursorDirection::Forward,
            )?
        };
        Ok(Self {
            meta: *meta,
            release_private,
            inner,
        })
    }

    pub(crate) fn next<S: Store>(&mut self, store: &mut S) -> Result<Option<DirectRange<K>>> {
        let next = if self.release_private {
            self.inner.next_consuming(store, &mut RangeItem)?
        } else {
            self.inner
                .next(&SelectedStore::new(&*store, self.meta), &mut RangeItem)?
        };
        crate::work::range_consumed(u64::from(next.is_some()));
        Ok(next)
    }
}

struct SelectedStore<'a, S> {
    store: &'a S,
    meta: MetaV4,
}

impl<'a, S> SelectedStore<'a, S> {
    const fn new(store: &'a S, meta: MetaV4) -> Self {
        Self { store, meta }
    }
}

impl<S: Store> PageSource for SelectedStore<'_, S> {
    type Page<'a>
        = S::ReadPage<'a>
    where
        Self: 'a;

    fn selected_txn(&self) -> u64 {
        self.meta.txn_id
    }

    fn selected_page_limit(&self) -> u64 {
        self.meta.page_count
    }

    fn view_page<'a, T, F>(&'a self, page_number: u32, inspect: F) -> Result<T>
    where
        F: FnOnce(Self::Page<'a>) -> Result<T>,
    {
        if page_number < 2 || u64::from(page_number) >= self.meta.page_count {
            return Err(Error::Corrupt("stored range page is outside its source"));
        }
        self.store.inspect_page(page_number, inspect)
    }
}
