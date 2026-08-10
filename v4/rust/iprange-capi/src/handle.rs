//! Opaque reader/writer ownership and fail-fast writer serialization.

use std::cell::UnsafeCell;
use std::mem::align_of;
use std::ptr::NonNull;
use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};
use std::sync::Arc;

use iprange_livedb::c_abi_support::{MembershipAlgebra, MembershipScope};
use iprange_livedb::c_abi_support::{MembershipToken, Reader, ReaderCursor, Writer};

use crate::error::{BoundaryError, CallError};
use crate::ip::Key;

pub(crate) const HANDLE_MAGIC: u64 = 0x4950_5234_4142_4931;
pub(crate) const READER_KIND: u32 = 1;
pub(crate) const WRITER_KIND: u32 = 2;
pub(crate) const MEMBERSHIP_VIEW_KIND: u32 = 3;
pub(crate) const FEED_REF_KIND: u32 = 4;
pub(crate) const MEMBERSHIP_BUILDER_KIND: u32 = 5;
pub(crate) const MEMBERSHIP_REF_KIND: u32 = 6;
pub(crate) const CURSOR_KIND: u32 = 7;
pub(crate) const BORROWED_MEMBERSHIP_VIEW_KIND: u32 = 8;
pub(crate) const CLEANUP_GUARD_KIND: u32 = 9;
pub(crate) const RESIDUE_KIND: u32 = 10;
pub(crate) const ERROR_KIND: u32 = 11;
pub(crate) const REPORT_KIND: u32 = 12;
pub(crate) const MEMBERSHIP_SCOPE_KIND: u32 = 13;
pub(crate) const MEMBERSHIP_ALGEBRA_KIND: u32 = 14;

#[repr(C)]
#[derive(Clone, Copy, Debug)]
pub(crate) struct Header {
    magic: u64,
    kind: u32,
    reserved: u32,
}

/// Opaque persistent membership view.
#[repr(C)]
#[derive(Debug)]
pub struct MembershipViewHandle {
    header: Header,
    busy: AtomicBool,
    reader: UnsafeCell<Option<Arc<Reader>>>,
    membership: MembershipToken,
}

// Mutable lifetime state is protected by the fail-fast gate.
unsafe impl Send for MembershipViewHandle {}
unsafe impl Sync for MembershipViewHandle {}

impl MembershipViewHandle {
    pub(crate) fn new(reader: Arc<Reader>, membership: MembershipToken) -> Self {
        Self {
            header: Header::new(MEMBERSHIP_VIEW_KIND),
            busy: AtomicBool::new(false),
            reader: UnsafeCell::new(Some(reader)),
            membership,
        }
    }

    pub(crate) fn with<T>(
        &self,
        operation: impl FnOnce(&Reader, MembershipToken) -> Result<T, CallError>,
    ) -> Result<T, CallError> {
        self.header.require(MEMBERSHIP_VIEW_KIND)?;
        let _guard = Gate::enter(&self.busy)?;
        // SAFETY: the gate protects the lifetime slot.
        let reader = unsafe { &*self.reader.get() }
            .as_deref()
            .ok_or_else(|| BoundaryError::wrong_state("membership view is closed"))?;
        operation(reader, self.membership)
    }

    pub(crate) fn close(&self) -> Result<(), BoundaryError> {
        self.header.require(MEMBERSHIP_VIEW_KIND)?;
        let _guard = Gate::enter(&self.busy)?;
        // SAFETY: the gate provides exclusive access to the lifetime slot.
        if unsafe { &mut *self.reader.get() }.take().is_none() {
            return Err(BoundaryError::wrong_state(
                "membership view is already closed",
            ));
        }
        Ok(())
    }

    pub(crate) fn is_closed(&self) -> Result<bool, BoundaryError> {
        self.header.require(MEMBERSHIP_VIEW_KIND)?;
        let _guard = Gate::enter(&self.busy)?;
        // SAFETY: the gate protects the lifetime slot.
        Ok(unsafe { &*self.reader.get() }.is_none())
    }
}

/// Opaque view borrowed from a scan callback or cursor step.
#[repr(C)]
#[derive(Debug)]
pub struct BorrowedMembershipViewHandle {
    header: Header,
    reader: NonNull<Reader>,
    membership: MembershipToken,
}

impl BorrowedMembershipViewHandle {
    pub(crate) fn new(reader: &Arc<Reader>, membership: MembershipToken) -> Self {
        Self {
            header: Header::new(BORROWED_MEMBERSHIP_VIEW_KIND),
            reader: NonNull::from(reader.as_ref()),
            membership,
        }
    }

    pub(crate) fn with<T>(
        &self,
        operation: impl FnOnce(&Reader, MembershipToken) -> Result<T, CallError>,
    ) -> Result<T, CallError> {
        self.header.require(BORROWED_MEMBERSHIP_VIEW_KIND)?;
        // SAFETY: the originating cursor/scan retains the Arc for this borrow.
        operation(unsafe { self.reader.as_ref() }, self.membership)
    }
}

struct CursorBody {
    reader: Option<Arc<Reader>>,
    cursor: Option<ReaderCursor>,
    borrowed: Option<BorrowedMembershipViewHandle>,
    bounds: Option<CursorBounds>,
}

#[derive(Clone, Copy, Debug)]
pub(crate) struct CursorBounds {
    pub(crate) from: Key,
    pub(crate) to: Key,
    pub(crate) direction: iprange_livedb::RangeDirection,
}

/// Opaque caller-serialized reader cursor.
#[repr(C)]
pub struct CursorHandle {
    header: Header,
    busy: AtomicBool,
    body: UnsafeCell<CursorBody>,
}

impl std::fmt::Debug for CursorHandle {
    fn fmt(&self, output: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        output.write_str("CursorHandle { .. }")
    }
}

// Mutable state is protected by the fail-fast gate.
unsafe impl Send for CursorHandle {}
unsafe impl Sync for CursorHandle {}

impl CursorHandle {
    pub(crate) fn new(
        reader: Arc<Reader>,
        cursor: ReaderCursor,
        bounds: Option<CursorBounds>,
    ) -> Self {
        Self {
            header: Header::new(CURSOR_KIND),
            busy: AtomicBool::new(false),
            body: UnsafeCell::new(CursorBody {
                reader: Some(reader),
                cursor: Some(cursor),
                borrowed: None,
                bounds,
            }),
        }
    }

    pub(crate) fn with_mut<T>(
        &self,
        operation: impl FnOnce(
            &Arc<Reader>,
            &mut ReaderCursor,
            &mut Option<BorrowedMembershipViewHandle>,
            Option<CursorBounds>,
        ) -> Result<T, CallError>,
    ) -> Result<T, CallError> {
        self.header.require(CURSOR_KIND)?;
        let _guard = Gate::enter(&self.busy)?;
        // SAFETY: the gate provides exclusive access and destroy is caller-serialized.
        let body = unsafe { &mut *self.body.get() };
        let reader = body
            .reader
            .as_ref()
            .ok_or_else(|| BoundaryError::wrong_state("cursor is closed"))?;
        let cursor = body
            .cursor
            .as_mut()
            .ok_or_else(|| BoundaryError::wrong_state("cursor is closed"))?;
        operation(reader, cursor, &mut body.borrowed, body.bounds)
    }

    pub(crate) fn close(&self) -> Result<(), CallError> {
        self.header.require(CURSOR_KIND)?;
        let _guard = Gate::enter(&self.busy)?;
        // SAFETY: the gate provides exclusive access and destroy is caller-serialized.
        let body = unsafe { &mut *self.body.get() };
        if body.cursor.take().is_none() {
            return Err(BoundaryError::wrong_state("cursor is already closed").into());
        }
        body.borrowed = None;
        body.reader = None;
        Ok(())
    }

    pub(crate) fn is_closed(&self) -> Result<bool, BoundaryError> {
        self.header.require(CURSOR_KIND)?;
        // SAFETY: destroy/inspection is caller-serialized by the ABI contract.
        Ok(unsafe { (*self.body.get()).cursor.is_none() })
    }
}

/// Opaque operation-bound writer feed reference.
#[repr(C)]
pub struct WriterFeedRefHandle {
    header: Header,
    busy: AtomicBool,
    parent: NonNull<WriterHandle>,
    pub(crate) value: iprange_livedb::FeedRef,
}

unsafe impl Send for WriterFeedRefHandle {}
unsafe impl Sync for WriterFeedRefHandle {}

impl std::fmt::Debug for WriterFeedRefHandle {
    fn fmt(&self, output: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        output.write_str("WriterFeedRefHandle { .. }")
    }
}

impl WriterFeedRefHandle {
    pub(crate) fn new(parent: &WriterHandle, value: iprange_livedb::FeedRef) -> Self {
        parent.add_child();
        Self {
            header: Header::new(FEED_REF_KIND),
            busy: AtomicBool::new(false),
            parent: NonNull::from(parent),
            value,
        }
    }

    pub(crate) fn enter(&self) -> Result<Gate<'_>, BoundaryError> {
        self.header.require(FEED_REF_KIND)?;
        Gate::enter(&self.busy)
    }

    pub(crate) fn require_parent(&self, parent: &WriterHandle) -> Result<(), BoundaryError> {
        self.header.require(FEED_REF_KIND)?;
        if !std::ptr::eq(self.parent.as_ptr().cast_const(), parent) {
            return Err(BoundaryError::wrong_state(
                "feed reference belongs to another writer",
            ));
        }
        Ok(())
    }

    pub(crate) fn parent(&self) -> &WriterHandle {
        // SAFETY: the parent cannot be destroyed while this child count is active.
        unsafe { self.parent.as_ref() }
    }
}

/// Opaque incremental membership builder.
#[repr(C)]
pub struct MembershipBuilderHandle {
    header: Header,
    busy: AtomicBool,
    parent: NonNull<WriterHandle>,
    body: UnsafeCell<MembershipBuilderBody>,
}

struct MembershipBuilderBody {
    value: iprange_livedb::MembershipRef,
    finished: bool,
}

unsafe impl Send for MembershipBuilderHandle {}
unsafe impl Sync for MembershipBuilderHandle {}

impl std::fmt::Debug for MembershipBuilderHandle {
    fn fmt(&self, output: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        output.write_str("MembershipBuilderHandle { .. }")
    }
}

impl MembershipBuilderHandle {
    pub(crate) fn new(parent: &WriterHandle, value: iprange_livedb::MembershipRef) -> Self {
        parent.add_child();
        Self {
            header: Header::new(MEMBERSHIP_BUILDER_KIND),
            busy: AtomicBool::new(false),
            parent: NonNull::from(parent),
            body: UnsafeCell::new(MembershipBuilderBody {
                value,
                finished: false,
            }),
        }
    }

    pub(crate) fn with_mut<T>(
        &self,
        operation: impl FnOnce(
            &WriterHandle,
            &mut iprange_livedb::MembershipRef,
            &mut bool,
        ) -> Result<T, CallError>,
    ) -> Result<T, CallError> {
        self.header.require(MEMBERSHIP_BUILDER_KIND)?;
        let _guard = Gate::enter(&self.busy)?;
        // SAFETY: the parent cannot be destroyed while this child count is active.
        let parent = unsafe { self.parent.as_ref() };
        // SAFETY: the gate provides exclusive access to the builder state.
        let body = unsafe { &mut *self.body.get() };
        operation(parent, &mut body.value, &mut body.finished)
    }

    pub(crate) fn enter(&self) -> Result<Gate<'_>, BoundaryError> {
        self.header.require(MEMBERSHIP_BUILDER_KIND)?;
        Gate::enter(&self.busy)
    }

    pub(crate) fn parent_unlocked(&self) -> &WriterHandle {
        // SAFETY: the parent cannot be destroyed while this child count is active.
        unsafe { self.parent.as_ref() }
    }
}

/// Opaque completed operation-bound membership reference.
#[repr(C)]
pub struct MembershipRefHandle {
    header: Header,
    busy: AtomicBool,
    parent: NonNull<WriterHandle>,
    pub(crate) value: iprange_livedb::MembershipRef,
}

unsafe impl Send for MembershipRefHandle {}
unsafe impl Sync for MembershipRefHandle {}

impl std::fmt::Debug for MembershipRefHandle {
    fn fmt(&self, output: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        output.write_str("MembershipRefHandle { .. }")
    }
}

impl MembershipRefHandle {
    pub(crate) fn new(parent: &WriterHandle, value: iprange_livedb::MembershipRef) -> Self {
        parent.add_child();
        Self {
            header: Header::new(MEMBERSHIP_REF_KIND),
            busy: AtomicBool::new(false),
            parent: NonNull::from(parent),
            value,
        }
    }

    pub(crate) fn enter(&self) -> Result<Gate<'_>, BoundaryError> {
        self.header.require(MEMBERSHIP_REF_KIND)?;
        Gate::enter(&self.busy)
    }

    pub(crate) fn require_parent(&self, parent: &WriterHandle) -> Result<(), BoundaryError> {
        self.header.require(MEMBERSHIP_REF_KIND)?;
        if !std::ptr::eq(self.parent.as_ptr().cast_const(), parent) {
            return Err(BoundaryError::wrong_state(
                "membership reference belongs to another writer",
            ));
        }
        Ok(())
    }

    pub(crate) fn parent(&self) -> &WriterHandle {
        // SAFETY: the parent cannot be destroyed while this child count is active.
        unsafe { self.parent.as_ref() }
    }
}

impl Header {
    pub(crate) const fn new(kind: u32) -> Self {
        Self {
            magic: HANDLE_MAGIC,
            kind,
            reserved: 0,
        }
    }

    pub(crate) fn require(&self, kind: u32) -> Result<(), BoundaryError> {
        if self.magic != HANDLE_MAGIC || self.kind != kind || self.reserved != 0 {
            return Err(BoundaryError::wrong_handle("wrong opaque handle kind"));
        }
        Ok(())
    }
}

/// Marker for opaque handles whose first field is the common ABI header.
///
/// # Safety
///
/// Implementors must be `#[repr(C)]`, place [`Header`] first, and use `KIND`
/// in that header.
pub(crate) unsafe trait OpaqueHandle {
    const KIND: u32;
}

pub(crate) unsafe fn required_handle_input<'a, T: OpaqueHandle>(
    pointer: *const T,
    name: &'static str,
) -> Result<&'a T, BoundaryError> {
    if pointer.is_null() {
        return Err(BoundaryError::null(name));
    }
    if (pointer as usize) % align_of::<T>() != 0 {
        return Err(BoundaryError::misaligned(
            "opaque handle pointer is misaligned",
        ));
    }
    // SAFETY: every opaque handle begins with the fixed common header. Reading
    // only that header is valid even when the caller supplied another handle kind.
    let header = unsafe { pointer.cast::<Header>().read() };
    header.require(T::KIND)?;
    // SAFETY: the checked kind identifies the complete caller-owned allocation.
    Ok(unsafe { &*pointer })
}

pub(crate) unsafe fn required_handle_output<'a, T: OpaqueHandle>(
    pointer: *mut T,
    name: &'static str,
) -> Result<&'a mut T, BoundaryError> {
    // SAFETY: validation happens before a typed mutable reference is created.
    unsafe { required_handle_input(pointer.cast_const(), name)? };
    // SAFETY: the ABI requires exclusive caller ownership for mutable calls.
    Ok(unsafe { &mut *pointer })
}

unsafe impl OpaqueHandle for MembershipViewHandle {
    const KIND: u32 = MEMBERSHIP_VIEW_KIND;
}

unsafe impl OpaqueHandle for BorrowedMembershipViewHandle {
    const KIND: u32 = BORROWED_MEMBERSHIP_VIEW_KIND;
}

unsafe impl OpaqueHandle for CursorHandle {
    const KIND: u32 = CURSOR_KIND;
}

unsafe impl OpaqueHandle for WriterFeedRefHandle {
    const KIND: u32 = FEED_REF_KIND;
}

unsafe impl OpaqueHandle for MembershipBuilderHandle {
    const KIND: u32 = MEMBERSHIP_BUILDER_KIND;
}

unsafe impl OpaqueHandle for MembershipRefHandle {
    const KIND: u32 = MEMBERSHIP_REF_KIND;
}

unsafe impl OpaqueHandle for MembershipScopeHandle {
    const KIND: u32 = MEMBERSHIP_SCOPE_KIND;
}

unsafe impl OpaqueHandle for MembershipAlgebraHandle {
    const KIND: u32 = MEMBERSHIP_ALGEBRA_KIND;
}

unsafe impl OpaqueHandle for ReaderHandle {
    const KIND: u32 = READER_KIND;
}

unsafe impl OpaqueHandle for WriterHandle {
    const KIND: u32 = WRITER_KIND;
}

/// Opaque C reader handle.
#[repr(C)]
#[derive(Debug)]
pub struct ReaderHandle {
    header: Header,
    reader: Option<Arc<Reader>>,
}

impl ReaderHandle {
    pub(crate) fn new(reader: Reader) -> Self {
        Self {
            header: Header::new(READER_KIND),
            reader: Some(Arc::new(reader)),
        }
    }

    pub(crate) fn get(&self) -> Result<&Arc<Reader>, BoundaryError> {
        self.header.require(READER_KIND)?;
        self.reader
            .as_ref()
            .ok_or_else(|| BoundaryError::wrong_state("reader handle is closed"))
    }

    pub(crate) fn close(&mut self) -> Result<(), CallError> {
        self.header.require(READER_KIND)?;
        let reader = self
            .reader
            .as_mut()
            .ok_or_else(|| BoundaryError::wrong_state("reader handle is closed"))?;
        let reader = Arc::get_mut(reader)
            .ok_or_else(|| BoundaryError::handle_busy("reader has active child handles"))?;
        reader.close()?;
        self.reader = None;
        Ok(())
    }

    pub(crate) fn is_closed(&self) -> bool {
        self.reader.is_none()
    }
}

/// Opaque reusable named membership scope.
#[repr(C)]
pub struct MembershipScopeHandle {
    header: Header,
    busy: AtomicBool,
    scope: UnsafeCell<Option<Arc<MembershipScope>>>,
}

unsafe impl Send for MembershipScopeHandle {}
unsafe impl Sync for MembershipScopeHandle {}

impl std::fmt::Debug for MembershipScopeHandle {
    fn fmt(&self, output: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        output.write_str("MembershipScopeHandle { .. }")
    }
}

impl MembershipScopeHandle {
    pub(crate) fn new(scope: MembershipScope) -> Self {
        Self {
            header: Header::new(MEMBERSHIP_SCOPE_KIND),
            busy: AtomicBool::new(false),
            scope: UnsafeCell::new(Some(Arc::new(scope))),
        }
    }

    pub(crate) fn with<T>(
        &self,
        operation: impl FnOnce(&MembershipScope) -> Result<T, CallError>,
    ) -> Result<T, CallError> {
        self.header.require(MEMBERSHIP_SCOPE_KIND)?;
        let _guard = Gate::enter(&self.busy)?;
        // SAFETY: the gate protects the lifetime slot.
        let scope = unsafe { &*self.scope.get() }
            .as_deref()
            .ok_or_else(|| BoundaryError::wrong_state("membership scope is closed"))?;
        operation(scope)
    }

    pub(crate) fn close(&self) -> Result<(), BoundaryError> {
        self.header.require(MEMBERSHIP_SCOPE_KIND)?;
        let _guard = Gate::enter(&self.busy)?;
        // SAFETY: the gate provides exclusive access to the lifetime slot.
        if unsafe { &mut *self.scope.get() }.take().is_none() {
            return Err(BoundaryError::wrong_state(
                "membership scope is already closed",
            ));
        }
        Ok(())
    }

    pub(crate) fn clone_scope(&self) -> Result<Arc<MembershipScope>, BoundaryError> {
        self.header.require(MEMBERSHIP_SCOPE_KIND)?;
        let _guard = Gate::enter(&self.busy)?;
        // SAFETY: the gate protects the lifetime slot while the Arc is cloned.
        unsafe { &*self.scope.get() }
            .as_ref()
            .cloned()
            .ok_or_else(|| BoundaryError::wrong_state("membership scope is closed"))
    }

    pub(crate) fn is_closed(&self) -> Result<bool, BoundaryError> {
        self.header.require(MEMBERSHIP_SCOPE_KIND)?;
        let _guard = Gate::enter(&self.busy)?;
        // SAFETY: the gate protects the lifetime slot.
        Ok(unsafe { &*self.scope.get() }.is_none())
    }
}

/// Opaque reusable global membership algebra.
#[repr(C)]
pub struct MembershipAlgebraHandle {
    header: Header,
    busy: AtomicBool,
    algebra: UnsafeCell<Option<MembershipAlgebra>>,
}

unsafe impl Send for MembershipAlgebraHandle {}
unsafe impl Sync for MembershipAlgebraHandle {}

impl std::fmt::Debug for MembershipAlgebraHandle {
    fn fmt(&self, output: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        output.write_str("MembershipAlgebraHandle { .. }")
    }
}

impl MembershipAlgebraHandle {
    pub(crate) fn new(algebra: MembershipAlgebra) -> Self {
        Self {
            header: Header::new(MEMBERSHIP_ALGEBRA_KIND),
            busy: AtomicBool::new(false),
            algebra: UnsafeCell::new(Some(algebra)),
        }
    }

    pub(crate) fn with<T>(
        &self,
        operation: impl FnOnce(&MembershipAlgebra) -> Result<T, CallError>,
    ) -> Result<T, CallError> {
        self.header.require(MEMBERSHIP_ALGEBRA_KIND)?;
        let _guard = Gate::enter(&self.busy)?;
        // SAFETY: the gate protects the lifetime slot.
        let algebra = unsafe { &*self.algebra.get() }
            .as_ref()
            .ok_or_else(|| BoundaryError::wrong_state("membership algebra is closed"))?;
        operation(algebra)
    }

    pub(crate) fn close(&self) -> Result<(), BoundaryError> {
        self.header.require(MEMBERSHIP_ALGEBRA_KIND)?;
        let _guard = Gate::enter(&self.busy)?;
        // SAFETY: the gate provides exclusive access to the lifetime slot.
        if unsafe { &mut *self.algebra.get() }.take().is_none() {
            return Err(BoundaryError::wrong_state(
                "membership algebra is already closed",
            ));
        }
        Ok(())
    }

    pub(crate) fn is_closed(&self) -> Result<bool, BoundaryError> {
        self.header.require(MEMBERSHIP_ALGEBRA_KIND)?;
        let _guard = Gate::enter(&self.busy)?;
        // SAFETY: the gate protects the lifetime slot.
        Ok(unsafe { &*self.algebra.get() }.is_none())
    }
}

/// Opaque C writer handle.
#[repr(C)]
#[derive(Debug)]
pub struct WriterHandle {
    header: Header,
    busy: AtomicBool,
    children: AtomicUsize,
    writer: UnsafeCell<Option<Writer>>,
}

// Every mutable access is protected by the fail-fast gate.
unsafe impl Send for WriterHandle {}
unsafe impl Sync for WriterHandle {}

impl WriterHandle {
    pub(crate) fn new(writer: Writer) -> Self {
        Self {
            header: Header::new(WRITER_KIND),
            busy: AtomicBool::new(false),
            children: AtomicUsize::new(0),
            writer: UnsafeCell::new(Some(writer)),
        }
    }

    pub(crate) fn with_mut<T>(
        &self,
        operation: impl FnOnce(&mut Writer) -> Result<T, CallError>,
    ) -> Result<T, CallError> {
        self.header.require(WRITER_KIND)?;
        let _guard = Gate::enter(&self.busy)?;
        // SAFETY: the gate provides exclusive access and destroy is caller-serialized.
        let writer = unsafe { &mut *self.writer.get() }
            .as_mut()
            .ok_or_else(|| BoundaryError::wrong_state("writer handle is closed"))?;
        operation(writer)
    }

    pub(crate) fn close(&self) -> Result<iprange_livedb::CloseResult, CallError> {
        self.header.require(WRITER_KIND)?;
        if self.children.load(Ordering::Acquire) != 0 {
            return Err(BoundaryError::handle_busy(
                "writer has active operation-bound child handles",
            )
            .into());
        }
        let _guard = Gate::enter(&self.busy)?;
        // SAFETY: the gate provides exclusive access and destroy is caller-serialized.
        let slot = unsafe { &mut *self.writer.get() };
        let writer = slot
            .as_mut()
            .ok_or_else(|| BoundaryError::wrong_state("writer handle is closed"))?;
        let report = writer.close()?;
        if report.outcome == iprange_livedb::CloseOutcome::Closed {
            *slot = None;
        }
        Ok(report)
    }

    pub(crate) fn is_closed(&self) -> bool {
        // SAFETY: destroy/inspection is caller-serialized by the ABI contract.
        unsafe { (*self.writer.get()).is_none() }
    }

    pub(crate) fn add_child(&self) {
        self.children.fetch_add(1, Ordering::AcqRel);
    }

    pub(crate) fn remove_child(&self) {
        self.children.fetch_sub(1, Ordering::AcqRel);
    }
}

pub(crate) struct Gate<'a> {
    busy: &'a AtomicBool,
}

impl<'a> Gate<'a> {
    pub(crate) fn enter(busy: &'a AtomicBool) -> Result<Self, BoundaryError> {
        busy.compare_exchange(false, true, Ordering::Acquire, Ordering::Relaxed)
            .map_err(|_| BoundaryError::handle_busy("handle is already in a callback or call"))?;
        Ok(Self { busy })
    }
}

impl Drop for Gate<'_> {
    fn drop(&mut self) {
        self.busy.store(false, Ordering::Release);
    }
}
