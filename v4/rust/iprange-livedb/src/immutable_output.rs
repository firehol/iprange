//! Shared append-only construction of one private immutable v4 file.

use std::fs::File;

use crate::contract::{AddressFamily, MetaV4, ValueKind, ValueTag, MAX_PAGE_COUNT, PAGE_SIZE};
use crate::error::{Error, Result};
use crate::feed::{FeedEntry, FeedName};
use crate::feed_catalog;
use crate::fixed_tree::{RetiredPages, RetiringStore, Store};
use crate::heap::Heap;
use crate::key::{Ipv4Key, Ipv6Key};
use crate::mapping::{Mapping, PageMut, PageView};
use crate::membership_delta::Delta;
use crate::membership_dictionary::{self, State};
use crate::metadata;
use crate::page_checksum;
use crate::page_header;
use crate::used_bitmap::{self, Kind};

mod membership;
mod ranges;
mod reference_batch;
mod setup;
pub(crate) mod unordered;

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

impl OutputSpec {
    pub(crate) fn fresh(
        address_family: AddressFamily,
        value_kind: ValueKind,
        value_tag: ValueTag,
        feed_index_limit: u64,
    ) -> Result<Self> {
        Ok(Self {
            address_family,
            value_kind,
            value_tag,
            database_id: crate::random::nonzero_128()?,
            transaction_id: 1,
            commit_nonce: crate::random::nonzero_128()?,
            feed_index_limit,
        })
    }
}

#[derive(Clone, Copy, Debug)]
pub(crate) struct OutputBudget {
    pub(crate) max_output_pages: u64,
}

#[derive(Debug)]
pub(crate) struct Finished {
    pub(crate) file: File,
    pub(crate) mapping: Mapping,
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

#[derive(Debug)]
pub(crate) struct Builder {
    mapping: Mapping,
    meta: MetaV4,
    budget: OutputBudget,
    ranges: ranges::Ranges,
    fault_protection: bool,
    membership_references: reference_batch::ReferenceBatch,
    metadata_staged: bool,
    failed: bool,
}

impl Builder {
    #[cfg(test)]
    #[allow(clippy::result_large_err)]
    pub(crate) fn new_owned(
        file: File,
        spec: OutputSpec,
        budget: OutputBudget,
    ) -> std::result::Result<Self, NewFailure> {
        let mut heap = Heap::new(0);
        Self::new_owned_with_heap(file, spec, budget, &mut heap)
    }

    #[allow(clippy::result_large_err)]
    pub(crate) fn new_owned_with_heap(
        file: File,
        spec: OutputSpec,
        budget: OutputBudget,
        heap: &mut Heap,
    ) -> std::result::Result<Self, NewFailure> {
        Self::new_owned_with_extent(file, spec, budget, budget.max_output_pages, heap)
    }

    #[allow(clippy::result_large_err)]
    pub(crate) fn new_owned_with_extent(
        file: File,
        spec: OutputSpec,
        budget: OutputBudget,
        physical_pages: u64,
        heap: &mut Heap,
    ) -> std::result::Result<Self, NewFailure> {
        let output_capacity = match budget
            .max_output_pages
            .checked_mul(PAGE_SIZE as u64)
            .ok_or(Error::ArithmeticOverflow("immutable output capacity"))
        {
            Ok(capacity) => capacity,
            Err(cause) => return Err(NewFailure { file, cause }),
        };
        let physical_capacity = match physical_pages
            .checked_mul(PAGE_SIZE as u64)
            .ok_or(Error::ArithmeticOverflow("immutable construction extent"))
        {
            Ok(capacity) => capacity,
            Err(cause) => return Err(NewFailure { file, cause }),
        };
        if physical_pages < budget.max_output_pages || physical_pages > MAX_PAGE_COUNT {
            return Err(NewFailure {
                file,
                cause: Error::InvalidArgument("immutable construction extent is invalid"),
            });
        }
        if let Err(cause) = setup::require_new_output(&file, spec, budget)
            .and_then(|()| file.set_len(physical_capacity).map_err(Error::from))
        {
            return Err(NewFailure { file, cause });
        }
        let membership_references = match reference_batch::ReferenceBatch::new(
            spec.value_kind == ValueKind::Membership,
            heap,
        ) {
            Ok(batch) => batch,
            Err(cause) => return Err(NewFailure { file, cause }),
        };
        let mapped_file = match file.try_clone() {
            Ok(mapped_file) => mapped_file,
            Err(error) => {
                return Err(NewFailure {
                    file,
                    cause: error.into(),
                });
            }
        };
        let mapping = match Mapping::read_write(mapped_file, output_capacity) {
            Ok(mapping) => mapping,
            Err(cause) => return Err(NewFailure { file, cause }),
        };
        drop(file);
        let meta = crate::database_file::empty_meta(crate::database_file::EmptySpec {
            address_family: spec.address_family,
            value_kind: spec.value_kind,
            value_tag: spec.value_tag,
            database_id: spec.database_id,
            transaction_id: spec.transaction_id,
            commit_nonce: spec.commit_nonce,
            feed_index_limit: spec.feed_index_limit,
        });
        Ok(Self {
            mapping,
            meta,
            budget,
            ranges: ranges::Ranges::new(meta.address_family, meta.txn_id, meta.value_kind),
            fault_protection: crate::worker::fault_protection_active(),
            membership_references,
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

    pub(crate) fn intern_membership_value<W: MembershipWords>(&mut self, words: &W) -> Result<u32> {
        self.mutate(|output| output.intern_membership(words))
    }

    pub(crate) fn push_interned_membership_v4(
        &mut self,
        from: Ipv4Key,
        to: Ipv4Key,
        value: u32,
    ) -> Result<()> {
        self.mutate(|output| output.push_interned_membership_v4_inner(from, to, value))
    }

    pub(crate) fn push_interned_membership_v6(
        &mut self,
        from: Ipv6Key,
        to: Ipv6Key,
        value: u32,
    ) -> Result<()> {
        self.mutate(|output| output.push_interned_membership_v6_inner(from, to, value))
    }

    pub(crate) fn write_metadata_with_budget(
        &mut self,
        input: &[u8],
        max_heap_bytes: u64,
    ) -> Result<()> {
        self.mutate(|output| output.write_metadata_inner(input, max_heap_bytes))
    }

    // The owner must remain available for exact cleanup without a failure-path allocation.
    #[allow(clippy::result_large_err)]
    pub(crate) fn finish_owned(mut self) -> std::result::Result<Finished, FinishFailure> {
        let result = finish(&mut self);
        match result {
            Ok(()) => match self.mapping.file().try_clone() {
                Ok(file) => Ok(Finished {
                    file,
                    mapping: self.mapping,
                    meta: self.meta,
                }),
                Err(error) => Err(FinishFailure {
                    builder: self,
                    cause: error.into(),
                }),
            },
            Err(cause) => Err(FinishFailure {
                builder: self,
                cause,
            }),
        }
    }

    pub(crate) fn into_file(self) -> File {
        self.mapping.into_file()
    }

    pub(crate) fn clone_file(&self) -> Result<File> {
        Ok(self.mapping.file().try_clone()?)
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
        self.ranges.push_v4(
            &mut self.mapping,
            &mut self.meta,
            self.budget,
            self.fault_protection,
            ranges::Record { from, to, value },
        )
    }

    fn push_direct_v6_inner(&mut self, from: Ipv6Key, to: Ipv6Key, value: u32) -> Result<()> {
        self.require_mode(ValueKind::Direct, AddressFamily::Ipv6)?;
        self.ranges.push_v6(
            &mut self.mapping,
            &mut self.meta,
            self.budget,
            self.fault_protection,
            ranges::Record { from, to, value },
        )
    }

    fn push_membership_v4_inner<W: MembershipWords>(
        &mut self,
        from: Ipv4Key,
        to: Ipv4Key,
        words: &W,
    ) -> Result<()> {
        self.require_mode(ValueKind::Membership, AddressFamily::Ipv4)?;
        let value = self.intern_membership(words)?;
        self.ranges.push_v4(
            &mut self.mapping,
            &mut self.meta,
            self.budget,
            self.fault_protection,
            ranges::Record { from, to, value },
        )?;
        self.add_membership_reference(value)
    }

    fn push_membership_v6_inner<W: MembershipWords>(
        &mut self,
        from: Ipv6Key,
        to: Ipv6Key,
        words: &W,
    ) -> Result<()> {
        self.require_mode(ValueKind::Membership, AddressFamily::Ipv6)?;
        let value = self.intern_membership(words)?;
        self.ranges.push_v6(
            &mut self.mapping,
            &mut self.meta,
            self.budget,
            self.fault_protection,
            ranges::Record { from, to, value },
        )?;
        self.add_membership_reference(value)
    }

    fn push_interned_membership_v4_inner(
        &mut self,
        from: Ipv4Key,
        to: Ipv4Key,
        value: u32,
    ) -> Result<()> {
        self.require_mode(ValueKind::Membership, AddressFamily::Ipv4)?;
        self.ranges.push_v4(
            &mut self.mapping,
            &mut self.meta,
            self.budget,
            self.fault_protection,
            ranges::Record { from, to, value },
        )?;
        self.add_membership_reference(value)
    }

    fn push_interned_membership_v6_inner(
        &mut self,
        from: Ipv6Key,
        to: Ipv6Key,
        value: u32,
    ) -> Result<()> {
        self.require_mode(ValueKind::Membership, AddressFamily::Ipv6)?;
        self.ranges.push_v6(
            &mut self.mapping,
            &mut self.meta,
            self.budget,
            self.fault_protection,
            ranges::Record { from, to, value },
        )?;
        self.add_membership_reference(value)
    }

    fn intern_membership<W: MembershipWords>(&mut self, source: &W) -> Result<u32> {
        membership::intern(self, source)
    }

    fn add_membership_reference(&mut self, value: u32) -> Result<()> {
        match self.membership_references.add(value)? {
            reference_batch::Add::Added => return Ok(()),
            reference_batch::Add::Direct => return self.apply_membership_reference(value),
            reference_batch::Add::Full => {}
        }
        self.flush_membership_references()?;
        match self.membership_references.add(value)? {
            reference_batch::Add::Added => Ok(()),
            reference_batch::Add::Full => Err(Error::Corrupt(
                "empty membership reference batch stayed full",
            )),
            reference_batch::Add::Direct => self.apply_membership_reference(value),
        }
    }

    fn apply_membership_reference(&mut self, value: u32) -> Result<()> {
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
        crate::work::membership_refcount_batch(1);
        Ok(())
    }

    fn flush_membership_references(&mut self) -> Result<()> {
        if self.membership_references.is_empty() {
            return Ok(());
        }
        let mut state = self.membership_state();
        for index in 0..self.membership_references.len() {
            if let Some(delta) = self.membership_references.take(index) {
                membership_dictionary::apply_delta(self, &mut state, delta)?;
            }
        }
        self.membership_references.finish_flush();
        self.store_membership_state(state);
        crate::work::membership_refcount_batch(1);
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
        if self.meta.value_kind != kind {
            return Err(Error::WrongValueKind(
                "immutable output operation does not match its value kind",
            ));
        }
        if self.meta.address_family != family {
            return Err(Error::WrongAddressFamily(
                "immutable output operation does not match its address family",
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

    #[inline]
    fn protected_region(&self) -> Result<Option<(*const u8, usize)>> {
        self.fault_protection
            .then(|| self.mapping.region())
            .transpose()
    }
}

fn finish(output: &mut Builder) -> Result<()> {
    output.require_active()?;
    output.flush_membership_references()?;
    let (range_root, range_record_count) = output.ranges.finish(
        &mut output.mapping,
        &mut output.meta,
        output.budget,
        output.fault_protection,
    )?;
    output.meta.range_root = range_root;
    output.meta.range_record_count = range_record_count;
    seal_pages(output)?;
    let bytes = output
        .meta
        .page_count
        .checked_mul(PAGE_SIZE as u64)
        .ok_or(Error::ArithmeticOverflow("immutable output length"))?;
    output.mapping.resize(bytes)?;
    let region = output.protected_region()?;
    with_output_protection(region, || {
        output
            .meta
            .encode_mapped(output.mapping.page_mut(0, output.meta.page_count)?)?;
        output
            .meta
            .encode_mapped(output.mapping.page_mut(1, output.meta.page_count)?)
    })?;
    output.mapping.flush_range(0, bytes)?;
    output.mapping.sync_file()
}

fn reserve_page(meta: &mut MetaV4, budget: OutputBudget) -> Result<u32> {
    if meta.page_count == MAX_PAGE_COUNT {
        return Err(Error::PageSpaceExhausted);
    }
    if meta.page_count >= budget.max_output_pages {
        return Err(Error::BudgetExceeded("immutable output pages"));
    }
    let page = u32::try_from(meta.page_count).map_err(|_| Error::PageSpaceExhausted)?;
    meta.page_count += 1;
    crate::work::page_created(1);
    Ok(page)
}

fn seal_pages(output: &mut Builder) -> Result<()> {
    let region = output.protected_region()?;
    with_output_protection(region, || {
        for page_number in 2..output.meta.page_count {
            let page_number = u32::try_from(page_number).map_err(|_| Error::PageSpaceExhausted)?;
            let mut page = output
                .mapping
                .page_mut(page_number, output.meta.page_count)?;
            let view = page.view();
            if !page_header::owned_by(view, output.meta.txn_id) {
                return Err(Error::Corrupt("immutable output page ownership is invalid"));
            }
            page_checksum::seal_mapped(&mut page)?;
        }
        Ok(())
    })
}

impl Store for Builder {
    type ReadPage<'a>
        = PageView<'a>
    where
        Self: 'a;
    type WritePage<'a>
        = PageMut<'a>
    where
        Self: 'a;

    fn target_txn(&self) -> u64 {
        self.meta.txn_id
    }

    fn page_limit(&self) -> u64 {
        self.meta.page_count
    }

    fn inspect_page<'a, T, F>(&'a self, page_number: u32, inspect: F) -> Result<T>
    where
        F: FnOnce(Self::ReadPage<'a>) -> Result<T>,
    {
        require_output_page(page_number, self.meta.page_count)?;
        let region = self.protected_region()?;
        with_output_protection(region, || {
            inspect(self.mapping.page(page_number, self.meta.page_count)?)
        })
    }

    fn allocate(&mut self) -> Result<u32> {
        reserve_page(&mut self.meta, self.budget)
    }

    fn update_page<'a, T, F>(&'a mut self, page_number: u32, update: F) -> Result<T>
    where
        F: FnOnce(&mut Self::WritePage<'a>) -> Result<T>,
    {
        require_output_page(page_number, self.meta.page_count)?;
        let region = self.protected_region()?;
        with_output_protection(region, || {
            let mut page = self.mapping.page_mut(page_number, self.meta.page_count)?;
            let result = update(&mut page)?;
            require_output_owner(page.view(), self.meta.txn_id)?;
            Ok(result)
        })
    }

    fn copy_page<'a, T, F>(&'a mut self, source: u32, destination: u32, copy: F) -> Result<T>
    where
        F: FnOnce(Self::ReadPage<'a>, &mut Self::WritePage<'a>) -> Result<T>,
    {
        require_output_page(source, self.meta.page_count)?;
        require_output_page(destination, self.meta.page_count)?;
        let region = self.protected_region()?;
        with_output_protection(region, || {
            let (source, mut destination) =
                self.mapping
                    .page_pair(source, destination, self.meta.page_count)?;
            let result = copy(source, &mut destination)?;
            require_output_owner(destination.view(), self.meta.txn_id)?;
            crate::work::page_copied(1);
            Ok(result)
        })
    }

    fn discard_private(&mut self, _page_number: u32) -> Result<()> {
        Err(Error::Corrupt(
            "immutable output attempted to discard an append-only page",
        ))
    }
}

#[inline]
fn with_output_protection<T>(
    region: Option<(*const u8, usize)>,
    operation: impl FnOnce() -> Result<T>,
) -> Result<T> {
    match region {
        Some(region) => crate::worker::probe_output_region(region, operation),
        None => operation(),
    }
}

fn require_output_page(page_number: u32, page_limit: u64) -> Result<()> {
    if page_number < 2 || u64::from(page_number) >= page_limit {
        Err(Error::Corrupt("immutable output page is outside bounds"))
    } else {
        Ok(())
    }
}

fn require_output_owner(page: PageView<'_>, txn: u64) -> Result<()> {
    if page_header::owned_by(page, txn) {
        Ok(())
    } else {
        Err(Error::Corrupt("immutable output page ownership is invalid"))
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

#[cfg(test)]
#[path = "immutable_output_tests.rs"]
mod tests;
