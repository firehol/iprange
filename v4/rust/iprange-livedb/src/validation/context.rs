use crate::cancellation::CancellationToken;
use crate::contract::{u32_le, MetaV4, ValueKind, PAGE_SIZE};
use crate::crc32c;
use crate::error::{Error, Result};
use crate::mapping::{Mapping, PageView};

use super::{
    membership_table::{InsertResult, Slot, Table},
    PhysicalByteInterval, ValidationAddressFence, ValidationBudget, ValidationFinding,
    ValidationObject, ValidationProgress, ValidationReason, ValidationSink, ValidationSinkControl,
};

const UNCLAIMED: u8 = 0;
const GRAPH: u8 = 1;
const ALLOCATION: u8 = 2;

pub(crate) struct Context<'a, S> {
    pub(crate) mapping: &'a Mapping,
    pub(crate) meta: MetaV4,
    claims: Claims,
    memberships: Option<Table>,
    heap_limit: u64,
    heap_used: u64,
    progress: ValidationProgress,
    cancellation: &'a CancellationToken,
    sink: &'a mut S,
}

impl<'a, S: ValidationSink> Context<'a, S> {
    pub(crate) fn new(
        mapping: &'a Mapping,
        meta: MetaV4,
        budget: &ValidationBudget,
        cancellation: &'a CancellationToken,
        sink: &'a mut S,
    ) -> Result<Self> {
        let claims = Claims::new(meta.page_count, budget.max_heap_bytes)?;
        let mut heap_used = claims.retained_bytes();
        let memberships = if meta.value_kind == ValueKind::Membership {
            let table = Table::new(
                meta.membership_entry_count,
                budget.max_heap_bytes.saturating_sub(heap_used),
            )?;
            heap_used = heap_used
                .checked_add(table.retained_bytes())
                .ok_or(Error::ArithmeticOverflow("validation retained heap"))?;
            Some(table)
        } else {
            None
        };
        Ok(Self {
            mapping,
            meta,
            claims,
            memberships,
            heap_limit: budget.max_heap_bytes,
            heap_used,
            progress: ValidationProgress::new(),
            cancellation,
            sink,
        })
    }

    pub(crate) fn finish(self) -> ValidationProgress {
        self.progress
    }

    pub(crate) fn checkpoint(&self) -> Result<()> {
        self.cancellation.check()
    }

    pub(crate) fn reserve_heap(&mut self, bytes: u64, purpose: &'static str) -> Result<()> {
        let retained = self
            .heap_used
            .checked_add(bytes)
            .ok_or(Error::ArithmeticOverflow("validation retained heap"))?;
        if retained > self.heap_limit {
            return Err(Error::BudgetExceeded(purpose));
        }
        self.heap_used = retained;
        Ok(())
    }

    pub(crate) fn release_heap(&mut self, bytes: u64) {
        self.heap_used = self.heap_used.saturating_sub(bytes);
    }

    pub(crate) fn mark_untraversable(&mut self, unbounded: bool) -> Result<()> {
        self.progress.mark_untraversable(unbounded)
    }

    pub(crate) fn count_membership_range(&mut self, id: u32) -> Result<InsertResult> {
        let cancellation = self.cancellation;
        self.memberships
            .as_mut()
            .ok_or(Error::Corrupt("direct validation has no membership table"))?
            .count_range(id, cancellation)
    }

    pub(crate) fn define_membership(
        &mut self,
        id: u32,
        refcount: u64,
        word_count: u32,
        digest: [u8; 32],
    ) -> Result<InsertResult> {
        let cancellation = self.cancellation;
        self.memberships
            .as_mut()
            .ok_or(Error::Corrupt("direct validation has no membership table"))?
            .define(id, refcount, word_count, digest, cancellation)
    }

    pub(crate) fn mark_membership_reverse(
        &mut self,
        id: u32,
        word_count: u32,
        digest: [u8; 32],
    ) -> Result<bool> {
        let cancellation = self.cancellation;
        self.memberships
            .as_mut()
            .ok_or(Error::Corrupt("direct validation has no membership table"))?
            .mark_reverse(id, word_count, digest, cancellation)
    }

    pub(crate) fn membership_slots(&self) -> Result<usize> {
        Ok(self
            .memberships
            .as_ref()
            .ok_or(Error::Corrupt("direct validation has no membership table"))?
            .len())
    }

    pub(crate) fn membership_slot(&self, index: usize) -> Result<Option<Slot>> {
        Ok(self
            .memberships
            .as_ref()
            .ok_or(Error::Corrupt("direct validation has no membership table"))?
            .slot(index))
    }

    pub(crate) fn reserve_allocator_pages(&mut self) -> Result<()> {
        for page in self.meta.allocator_reserve {
            if page == 0 {
                continue;
            }
            if page < 2 || u64::from(page) >= self.meta.page_count {
                self.emit(
                    ValidationReason::PageOutOfBounds,
                    ValidationObject::FreeBitmap,
                    Some(page),
                    None,
                    None,
                )?;
                continue;
            }
            if self.claims.add(page, ALLOCATION)? != UNCLAIMED {
                self.emit(
                    ValidationReason::AllocationPartitionInvalid,
                    ValidationObject::FreeBitmap,
                    Some(page),
                    None,
                    None,
                )?;
            }
        }
        Ok(())
    }

    pub(crate) fn read_graph_page(
        &mut self,
        page_number: u32,
        object: ValidationObject,
        path: &[u32],
    ) -> Result<Option<PageView<'a>>> {
        self.checkpoint()?;
        if !self.require_graph_bounds(page_number, object)? {
            return Ok(None);
        }
        if !self.claim_graph_page(page_number, object, path)? {
            return Ok(None);
        }
        self.load_graph_page(page_number, object)
    }

    fn require_graph_bounds(&mut self, page_number: u32, object: ValidationObject) -> Result<bool> {
        if page_number >= 2 && u64::from(page_number) < self.meta.page_count {
            return Ok(true);
        }
        self.emit(
            ValidationReason::PageOutOfBounds,
            object,
            Some(page_number),
            None,
            None,
        )?;
        self.progress
            .mark_untraversable(object_has_addresses(object))?;
        Ok(false)
    }

    fn claim_graph_page(
        &mut self,
        page_number: u32,
        object: ValidationObject,
        path: &[u32],
    ) -> Result<bool> {
        let previous = self.claims.get(page_number)?;
        if previous & GRAPH != 0 {
            self.emit(
                graph_reuse_reason(path, page_number),
                object,
                Some(page_number),
                None,
                None,
            )?;
            self.progress
                .mark_untraversable(object_has_addresses(object))?;
            return Ok(false);
        }
        self.claims.add(page_number, GRAPH)?;
        if previous & ALLOCATION != 0 {
            self.emit(
                ValidationReason::AllocationPartitionInvalid,
                object,
                Some(page_number),
                None,
                None,
            )?;
        }
        Ok(true)
    }

    fn load_graph_page(
        &mut self,
        page_number: u32,
        object: ValidationObject,
    ) -> Result<Option<PageView<'a>>> {
        self.progress.count_page(object)?;
        let mapping: &'a Mapping = self.mapping;
        let page = match mapping.page(page_number, self.meta.page_count) {
            Ok(page) => page,
            Err(_) => {
                self.emit(
                    ValidationReason::IoError,
                    object,
                    Some(page_number),
                    None,
                    None,
                )?;
                self.progress
                    .mark_untraversable(object_has_addresses(object))?;
                return Ok(None);
            }
        };
        if crc32c::crc32c_source_with_zeroed(page, 28, 4) != Some(u32_le(page, 28)) {
            self.emit(
                ValidationReason::PageCrcMismatch,
                object,
                Some(page_number),
                None,
                None,
            )?;
            self.progress
                .mark_untraversable(object_has_addresses(object))?;
            return Ok(None);
        }
        Ok(Some(page))
    }

    pub(crate) fn mark_allocated(
        &mut self,
        page_number: u32,
        object: ValidationObject,
    ) -> Result<()> {
        self.checkpoint()?;
        if page_number < 2 || u64::from(page_number) >= self.meta.page_count {
            self.emit(
                ValidationReason::PageOutOfBounds,
                object,
                Some(page_number),
                None,
                None,
            )?;
            return Ok(());
        }
        if self.claims.add(page_number, ALLOCATION)? != UNCLAIMED {
            self.emit(
                ValidationReason::AllocationPartitionInvalid,
                object,
                Some(page_number),
                None,
                None,
            )?;
        }
        Ok(())
    }

    pub(crate) fn validate_partition(&mut self) -> Result<()> {
        let mut page = 2u64;
        while page < self.meta.page_count {
            self.checkpoint()?;
            let Some((start, end)) = self.next_unclaimed(page)? else {
                break;
            };
            self.emit(
                ValidationReason::AllocationPartitionInvalid,
                ValidationObject::FileGeometry,
                Some(start as u32),
                Some(partition_bytes(start, end)?),
                None,
            )?;
            page = end;
        }
        Ok(())
    }

    fn next_unclaimed(&self, page: u64) -> Result<Option<(u64, u64)>> {
        let page = self.skip_claimed(page)?;
        if page == self.meta.page_count {
            return Ok(None);
        }
        Ok(Some((page, self.skip_unclaimed(page)?)))
    }

    fn skip_claimed(&self, mut page: u64) -> Result<u64> {
        while page < self.meta.page_count && self.claims.get(page as u32)? != UNCLAIMED {
            if page & 63 == 0 {
                self.checkpoint()?;
            }
            page += 1;
        }
        Ok(page)
    }

    fn skip_unclaimed(&self, mut page: u64) -> Result<u64> {
        while page < self.meta.page_count && self.claims.get(page as u32)? == UNCLAIMED {
            if page & 63 == 0 {
                self.checkpoint()?;
            }
            page += 1;
        }
        Ok(page)
    }

    pub(crate) fn emit(
        &mut self,
        reason: ValidationReason,
        object: ValidationObject,
        page_number: Option<u32>,
        physical_bytes: Option<PhysicalByteInterval>,
        address_fence: Option<ValidationAddressFence>,
    ) -> Result<()> {
        self.progress.count_finding(reason)?;
        let finding = ValidationFinding {
            sequence: self.progress.finding_count,
            reason,
            object,
            page_number,
            physical_bytes,
            related_page_number: None,
            address_fence,
        };
        match self.sink.finding(&finding) {
            Ok(ValidationSinkControl::Continue) => Ok(()),
            Ok(ValidationSinkControl::Stop) => Err(Error::StoppedBySink),
            Err(cause) => Err(Error::SinkFailed(Box::new(cause))),
        }
    }
}

fn object_has_addresses(object: ValidationObject) -> bool {
    object == ValidationObject::RangeTree
}

fn graph_reuse_reason(path: &[u32], page: u32) -> ValidationReason {
    if path.contains(&page) {
        ValidationReason::TreeCycle
    } else {
        ValidationReason::PageAlias
    }
}

fn partition_bytes(start: u64, end: u64) -> Result<PhysicalByteInterval> {
    Ok(PhysicalByteInterval {
        start: start
            .checked_mul(PAGE_SIZE as u64)
            .ok_or(Error::ArithmeticOverflow(
                "validation partition byte offset",
            ))?,
        end_exclusive: end
            .checked_mul(PAGE_SIZE as u64)
            .ok_or(Error::ArithmeticOverflow(
                "validation partition byte offset",
            ))?,
    })
}

struct Claims {
    bytes: Vec<u8>,
    page_count: u64,
}

impl Claims {
    fn new(page_count: u64, max_heap_bytes: u64) -> Result<Self> {
        let byte_count = page_count
            .checked_add(3)
            .ok_or(Error::ArithmeticOverflow("validation claim bitmap"))?
            / 4;
        if byte_count > max_heap_bytes {
            return Err(Error::BudgetExceeded("validation page-claim bitmap"));
        }
        let length = usize::try_from(byte_count)
            .map_err(|_| Error::BudgetExceeded("validation page-claim bitmap"))?;
        let mut bytes = Vec::new();
        bytes
            .try_reserve_exact(length)
            .map_err(|_| Error::BudgetExceeded("validation page-claim bitmap"))?;
        bytes.resize(length, 0);
        Ok(Self { bytes, page_count })
    }

    fn add(&mut self, page: u32, state: u8) -> Result<u8> {
        let previous = self.get(page)?;
        let index = page as usize / 4;
        let shift = (page as usize % 4) * 2;
        self.bytes[index] |= state << shift;
        Ok(previous)
    }

    fn get(&self, page: u32) -> Result<u8> {
        if u64::from(page) >= self.page_count {
            return Err(Error::Corrupt("validation claim is outside page bounds"));
        }
        let index = page as usize / 4;
        let shift = (page as usize % 4) * 2;
        Ok((self.bytes[index] >> shift) & 3)
    }

    fn retained_bytes(&self) -> u64 {
        self.bytes.len() as u64
    }
}
