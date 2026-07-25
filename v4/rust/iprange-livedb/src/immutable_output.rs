//! Shared append-only construction of one private immutable v4 file.

use std::fs::File;

use crate::contract::{AddressFamily, MetaV4, ValueKind, ValueTag, MAX_PAGE_COUNT, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::feed::{FeedEntry, FeedName};
use crate::feed_catalog;
use crate::file_io;
use crate::fixed_tree::{RetiredPages, RetiringStore, Store};
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::membership_delta::Delta;
use crate::membership_dictionary::{self, State};
use crate::metadata;
use crate::range_mutation;
use crate::used_bitmap::{self, Kind};

mod membership;
mod setup;

pub(crate) use membership::MembershipWords;

#[derive(Clone, Copy, Debug)]
pub(crate) struct OutputSpec {
    pub(crate) address_family: AddressFamily,
    pub(crate) value_kind: ValueKind,
    pub(crate) value_tag: ValueTag,
    pub(crate) database_id: [u8; 16],
    pub(crate) transaction_id: u64,
    pub(crate) commit_nonce: [u8; 16],
    pub(crate) feed_index_limit: u64,
}

#[derive(Clone, Copy, Debug)]
pub(crate) struct OutputBudget {
    pub(crate) max_heap_bytes: u64,
    pub(crate) max_output_pages: u64,
}

#[derive(Debug)]
pub(crate) struct Finished {
    pub(crate) file: File,
    pub(crate) meta: MetaV4,
}

#[derive(Debug)]
pub(crate) struct FinishFailure {
    pub(crate) builder: Builder,
    pub(crate) cause: Error,
}

#[derive(Debug)]
pub(crate) struct NewFailure {
    pub(crate) file: File,
    pub(crate) cause: Error,
}

#[derive(Clone, Copy, Debug)]
struct LastRange<K> {
    to: K,
    value: u32,
}

#[derive(Debug)]
pub(crate) struct Builder {
    file: File,
    meta: MetaV4,
    budget: OutputBudget,
    last_v4: Option<LastRange<Ipv4Key>>,
    last_v6: Option<LastRange<Ipv6Key>>,
    metadata_staged: bool,
    failed: bool,
}

impl Builder {
    pub(crate) fn new(file: File, spec: OutputSpec, budget: OutputBudget) -> Result<Self> {
        Self::new_owned(file, spec, budget).map_err(|failure| failure.cause)
    }

    #[allow(clippy::result_large_err)]
    pub(crate) fn new_owned(
        file: File,
        spec: OutputSpec,
        budget: OutputBudget,
    ) -> std::result::Result<Self, NewFailure> {
        if let Err(cause) = setup::require_new_output(&file, spec, budget)
            .and_then(|()| file.set_len((2 * PAGE_SIZE) as u64).map_err(Error::from))
        {
            return Err(NewFailure { file, cause });
        }
        Ok(Self {
            file,
            meta: setup::empty_meta(spec),
            budget,
            last_v4: None,
            last_v6: None,
            metadata_staged: false,
            failed: false,
        })
    }

    pub(crate) fn push_feed(&mut self, name: FeedName, index: u32) -> Result<()> {
        self.mutate(|output| output.push_feed_inner(name, index))
    }

    pub(crate) fn push_direct_v4(&mut self, from: Ipv4Key, to: Ipv4Key, value: u32) -> Result<()> {
        self.mutate(|output| output.push_direct_v4_inner(from, to, value))
    }

    pub(crate) fn push_direct_v6(&mut self, from: Ipv6Key, to: Ipv6Key, value: u32) -> Result<()> {
        self.mutate(|output| output.push_direct_v6_inner(from, to, value))
    }

    pub(crate) fn push_membership_v4<W: MembershipWords>(
        &mut self,
        from: Ipv4Key,
        to: Ipv4Key,
        words: &W,
    ) -> Result<()> {
        self.mutate(|output| output.push_membership_v4_inner(from, to, words))
    }

    pub(crate) fn push_membership_v6<W: MembershipWords>(
        &mut self,
        from: Ipv6Key,
        to: Ipv6Key,
        words: &W,
    ) -> Result<()> {
        self.mutate(|output| output.push_membership_v6_inner(from, to, words))
    }

    pub(crate) fn write_metadata(&mut self, input: &[u8]) -> Result<()> {
        self.write_metadata_with_budget(input, self.budget.max_heap_bytes)
    }

    pub(crate) fn write_metadata_with_budget(
        &mut self,
        input: &[u8],
        max_heap_bytes: u64,
    ) -> Result<()> {
        self.mutate(|output| output.write_metadata_inner(input, max_heap_bytes))
    }

    pub(crate) fn finish(self) -> Result<Finished> {
        self.finish_owned().map_err(|failure| failure.cause)
    }

    // The owner must remain available for exact cleanup without a failure-path allocation.
    #[allow(clippy::result_large_err)]
    pub(crate) fn finish_owned(self) -> std::result::Result<Finished, FinishFailure> {
        let result = finish(&self);
        match result {
            Ok(()) => Ok(Finished {
                file: self.file,
                meta: self.meta,
            }),
            Err(cause) => Err(FinishFailure {
                builder: self,
                cause,
            }),
        }
    }

    pub(crate) fn into_file(self) -> File {
        self.file
    }

    pub(crate) fn meta(&self) -> MetaV4 {
        self.meta
    }

    fn mutate<T>(&mut self, operation: impl FnOnce(&mut Self) -> Result<T>) -> Result<T> {
        self.require_active()?;
        let result = operation(self);
        if result.is_err() {
            self.failed = true;
        }
        result
    }

    fn require_active(&self) -> Result<()> {
        if self.failed {
            Err(Error::WrongState("immutable output construction failed"))
        } else {
            Ok(())
        }
    }

    fn push_feed_inner(&mut self, name: FeedName, index: u32) -> Result<()> {
        self.require_mode(ValueKind::Membership, self.meta.address_family)?;
        if u64::from(index) >= self.meta.feed_index_limit {
            return Err(Error::InvalidArgument(
                "feed index exceeds the preserved limit",
            ));
        }
        let active = self
            .meta
            .active_feed_count
            .checked_add(1)
            .ok_or(Error::ArithmeticOverflow("active feed count"))?;
        if active > self.meta.feed_index_limit {
            return Err(Error::Corrupt("feed catalog exceeds its index limit"));
        }

        let mut name_root = self.meta.catalog_name_root;
        let mut index_root = self.meta.catalog_index_root;
        feed_catalog::insert(
            self,
            &mut name_root,
            &mut index_root,
            FeedEntry { name, index },
        )?;
        self.meta.catalog_name_root = name_root;
        self.meta.catalog_index_root = index_root;

        let mut used_root = self.meta.feed_used_root;
        let mut retired = RetiredPages::new();
        used_bitmap::set(
            self,
            &mut used_root,
            self.meta.feed_index_limit,
            Kind::Feed,
            index,
            &mut retired,
        )?;
        self.retire_pages(retired.as_slice())?;
        self.meta.feed_used_root = used_root;
        self.meta.active_feed_count = active;
        Ok(())
    }

    fn push_direct_v4_inner(&mut self, from: Ipv4Key, to: Ipv4Key, value: u32) -> Result<()> {
        self.require_mode(ValueKind::Direct, AddressFamily::Ipv4)?;
        require_order(self.last_v4, from, to, Some(value))?;
        self.assign_range(from, to, value)?;
        self.last_v4 = Some(LastRange { to, value });
        Ok(())
    }

    fn push_direct_v6_inner(&mut self, from: Ipv6Key, to: Ipv6Key, value: u32) -> Result<()> {
        self.require_mode(ValueKind::Direct, AddressFamily::Ipv6)?;
        require_order(self.last_v6, from, to, Some(value))?;
        self.assign_range(from, to, value)?;
        self.last_v6 = Some(LastRange { to, value });
        Ok(())
    }

    fn push_membership_v4_inner<W: MembershipWords>(
        &mut self,
        from: Ipv4Key,
        to: Ipv4Key,
        words: &W,
    ) -> Result<()> {
        self.require_mode(ValueKind::Membership, AddressFamily::Ipv4)?;
        require_order(self.last_v4, from, to, None)?;
        let value = self.intern_membership(words)?;
        require_adjacency(self.last_v4, from, value)?;
        self.assign_range(from, to, value)?;
        self.last_v4 = Some(LastRange { to, value });
        Ok(())
    }

    fn push_membership_v6_inner<W: MembershipWords>(
        &mut self,
        from: Ipv6Key,
        to: Ipv6Key,
        words: &W,
    ) -> Result<()> {
        self.require_mode(ValueKind::Membership, AddressFamily::Ipv6)?;
        require_order(self.last_v6, from, to, None)?;
        let value = self.intern_membership(words)?;
        require_adjacency(self.last_v6, from, value)?;
        self.assign_range(from, to, value)?;
        self.last_v6 = Some(LastRange { to, value });
        Ok(())
    }

    fn intern_membership<W: MembershipWords>(&mut self, source: &W) -> Result<u32> {
        membership::intern(self, source)
    }

    fn assign_range<K: IpKey>(&mut self, from: K, to: K, value: u32) -> Result<()> {
        let mut root = self.meta.range_root;
        let mut count = self.meta.range_record_count;
        if !range_mutation::assign(self, &mut root, &mut count, from, to, value)? {
            return Err(Error::Corrupt("immutable output range was not inserted"));
        }
        self.meta.range_root = root;
        self.meta.range_record_count = count;
        Ok(())
    }

    fn write_metadata_inner(&mut self, input: &[u8], max_heap_bytes: u64) -> Result<()> {
        if self.metadata_staged {
            return Err(Error::WrongState(
                "immutable output metadata is already set",
            ));
        }
        let compressed = metadata::compress(input, max_heap_bytes)?;
        self.meta.metadata_root = metadata::write_chain(self, &compressed)?;
        self.meta.metadata_uncompressed_len = input.len() as u64;
        self.meta.metadata_compressed_len = compressed.len() as u64;
        self.metadata_staged = true;
        Ok(())
    }

    fn require_mode(&self, kind: ValueKind, family: AddressFamily) -> Result<()> {
        if self.meta.value_kind != kind || self.meta.address_family != family {
            return Err(Error::WrongMode(
                "immutable output operation does not match its format",
            ));
        }
        Ok(())
    }

    fn membership_state(&self) -> State {
        State {
            id_root: self.meta.membership_id_root,
            hash_root: self.meta.membership_hash_root,
            used_root: self.meta.membership_used_root,
            entry_count: self.meta.membership_entry_count,
            id_limit: self.meta.membership_id_limit,
        }
    }

    fn store_membership_state(&mut self, state: State) {
        self.meta.membership_id_root = state.id_root;
        self.meta.membership_hash_root = state.hash_root;
        self.meta.membership_used_root = state.used_root;
        self.meta.membership_entry_count = state.entry_count;
        self.meta.membership_id_limit = state.id_limit;
    }
}

fn finish(output: &Builder) -> Result<()> {
    output.require_active()?;
    let bytes = output
        .meta
        .page_count
        .checked_mul(PAGE_SIZE as u64)
        .ok_or(Error::ArithmeticOverflow("immutable output length"))?;
    output.file.set_len(bytes)?;
    let mut page = [0; PAGE_SIZE];
    output.meta.encode_into(&mut page);
    file_io::write_exact_at(&output.file, &page, 0)?;
    file_io::write_exact_at(&output.file, &page, PAGE_SIZE as u64)
}

impl Store for Builder {
    fn target_txn(&self) -> u64 {
        self.meta.txn_id
    }

    fn page_limit(&self) -> u64 {
        self.meta.page_count
    }

    fn read(&self, page_number: u32, page: &mut [u8; PAGE_SIZE]) -> Result<()> {
        file_io::read_page(&self.file, page_number, self.meta.page_count, page)
    }

    fn allocate(&mut self) -> Result<u32> {
        if self.meta.page_count == MAX_PAGE_COUNT {
            return Err(Error::PageSpaceExhausted);
        }
        if self.meta.page_count >= self.budget.max_output_pages {
            return Err(Error::BudgetExceeded("immutable output pages"));
        }
        let page = u32::try_from(self.meta.page_count).map_err(|_| Error::PageSpaceExhausted)?;
        self.meta.page_count += 1;
        Ok(page)
    }

    fn write(&mut self, page_number: u32, page: &[u8; PAGE_SIZE]) -> Result<()> {
        if page_number < 2 || u64::from(page_number) >= self.meta.page_count {
            return Err(Error::Corrupt("immutable output write is outside bounds"));
        }
        let offset = u64::from(page_number)
            .checked_mul(PAGE_SIZE as u64)
            .ok_or(Error::ArithmeticOverflow("immutable output page offset"))?;
        file_io::write_exact_at(&self.file, page, offset)
    }

    fn discard_private(&mut self, _page_number: u32) -> Result<()> {
        Err(Error::Corrupt(
            "immutable output attempted to discard an append-only page",
        ))
    }
}

impl RetiringStore for Builder {
    fn retire_pages(&mut self, pages: &[u32]) -> Result<()> {
        if pages.is_empty() {
            Ok(())
        } else {
            Err(Error::Corrupt(
                "immutable output attempted to retire an existing page",
            ))
        }
    }
}

impl range_mutation::RangeStore for Builder {
    fn range_record_added(&mut self, value: u32) -> Result<()> {
        if self.meta.value_kind == ValueKind::Direct {
            return Ok(());
        }
        let mut state = self.membership_state();
        membership_dictionary::apply_delta(
            self,
            &mut state,
            Delta {
                id: value,
                change: 1,
            },
        )?;
        self.store_membership_state(state);
        Ok(())
    }

    fn range_record_removed(&mut self, _value: u32) -> Result<()> {
        Err(Error::Corrupt(
            "immutable output attempted to rewrite an ordered range",
        ))
    }
}

fn require_order<K: IpKey>(
    previous: Option<LastRange<K>>,
    from: K,
    to: K,
    value: Option<u32>,
) -> Result<()> {
    if from > to {
        return Err(Error::InvalidArgument("range start is after its end"));
    }
    let Some(previous) = previous else {
        return Ok(());
    };
    if previous.to >= from {
        return Err(Error::InvalidArgument(
            "immutable output ranges are not strictly ordered",
        ));
    }
    if value == Some(previous.value) && previous.to.checked_next() == Some(from) {
        return Err(Error::InvalidArgument(
            "adjacent equal ranges are not canonical",
        ));
    }
    Ok(())
}

fn require_adjacency<K: IpKey>(previous: Option<LastRange<K>>, from: K, value: u32) -> Result<()> {
    if previous
        .is_some_and(|previous| previous.value == value && previous.to.checked_next() == Some(from))
    {
        Err(Error::InvalidArgument(
            "adjacent equal ranges are not canonical",
        ))
    } else {
        Ok(())
    }
}

#[cfg(test)]
#[path = "immutable_output_tests.rs"]
mod tests;
