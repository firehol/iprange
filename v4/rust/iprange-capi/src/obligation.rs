//! Explicit ownership for cleanup and residue obligations.

use std::cell::UnsafeCell;
use std::sync::atomic::AtomicBool;

use iprange_livedb::publication::{PublicationProblem, PublicationResidueHandle};
use iprange_livedb::recovery::RecoverySourceCleanupGuard;

use crate::abi::STATUS_OK;
use crate::error::{
    call, call_with_output, required_output, BoundaryError, CallError, ErrorHandle,
};
use crate::handle::{Gate, Header, OpaqueHandle, CLEANUP_GUARD_KIND, RESIDUE_KIND};

/// Opaque retry authority for source-coordination cleanup.
#[repr(C)]
#[derive(Debug)]
pub struct CleanupGuardHandle {
    header: Header,
    busy: AtomicBool,
    guard: UnsafeCell<Option<RecoverySourceCleanupGuard>>,
}

unsafe impl Send for CleanupGuardHandle {}
unsafe impl Sync for CleanupGuardHandle {}

unsafe impl OpaqueHandle for CleanupGuardHandle {
    const KIND: u32 = CLEANUP_GUARD_KIND;
}

impl CleanupGuardHandle {
    pub(crate) fn new(guard: RecoverySourceCleanupGuard) -> Self {
        Self {
            header: Header::new(CLEANUP_GUARD_KIND),
            busy: AtomicBool::new(false),
            guard: UnsafeCell::new(Some(guard)),
        }
    }

    fn retry(&self) -> Result<bool, CallError> {
        self.header.require(CLEANUP_GUARD_KIND)?;
        let _gate = Gate::enter(&self.busy)?;
        // SAFETY: the gate provides exclusive access to the retained authority.
        let slot = unsafe { &mut *self.guard.get() };
        let Some(guard) = slot.as_mut() else {
            return Ok(false);
        };
        match guard.retry_cleanup() {
            Ok(changed) => {
                if !guard.cleanup_pending() {
                    *slot = None;
                }
                Ok(changed)
            }
            Err(problem) => Err(ErrorHandle::from_publication_problem(problem).into()),
        }
    }

    fn close(&self) -> Result<(), CallError> {
        self.header.require(CLEANUP_GUARD_KIND)?;
        let _gate = Gate::enter(&self.busy)?;
        // SAFETY: the gate provides exclusive access to the retained authority.
        let slot = unsafe { &mut *self.guard.get() };
        let Some(guard) = slot.as_mut() else {
            return Ok(());
        };
        guard
            .retry_cleanup()
            .map_err(ErrorHandle::from_publication_problem)?;
        if !guard.cleanup_pending() {
            *slot = None;
        }
        Ok(())
    }

    fn pending(&self) -> Result<(Gate<'_>, bool), BoundaryError> {
        self.header.require(CLEANUP_GUARD_KIND)?;
        let gate = Gate::enter(&self.busy)?;
        // SAFETY: the returned gate keeps the retained authority stable.
        let pending = unsafe { &*self.guard.get() }.is_some();
        Ok((gate, pending))
    }
}

/// Opaque same-process authority for publication-residue removal.
#[repr(C)]
#[derive(Debug)]
pub struct ResidueHandle {
    header: Header,
    busy: AtomicBool,
    residue: UnsafeCell<Option<PublicationResidueHandle>>,
}

unsafe impl Send for ResidueHandle {}
unsafe impl Sync for ResidueHandle {}

unsafe impl OpaqueHandle for ResidueHandle {
    const KIND: u32 = RESIDUE_KIND;
}

impl ResidueHandle {
    pub(crate) fn new(residue: PublicationResidueHandle) -> Self {
        Self {
            header: Header::new(RESIDUE_KIND),
            busy: AtomicBool::new(false),
            residue: UnsafeCell::new(Some(residue)),
        }
    }

    pub(crate) fn take(&self) -> Result<PublicationResidueHandle, BoundaryError> {
        self.header.require(RESIDUE_KIND)?;
        let _gate = Gate::enter(&self.busy)?;
        // SAFETY: the gate provides exclusive access to the retained authority.
        unsafe { &mut *self.residue.get() }
            .take()
            .ok_or_else(|| BoundaryError::wrong_state("residue handle is closed"))
    }

    fn close(&self) -> Result<(), BoundaryError> {
        self.header.require(RESIDUE_KIND)?;
        let _gate = Gate::enter(&self.busy)?;
        // SAFETY: the gate provides exclusive access to the retained authority.
        let residue = unsafe { &mut *self.residue.get() }
            .take()
            .ok_or_else(|| BoundaryError::wrong_state("residue handle is already closed"))?;
        residue.close();
        Ok(())
    }

    fn is_closed(&self) -> Result<(Gate<'_>, bool), BoundaryError> {
        self.header.require(RESIDUE_KIND)?;
        let gate = Gate::enter(&self.busy)?;
        // SAFETY: the returned gate keeps the retained authority stable.
        let closed = unsafe { &*self.residue.get() }.is_none();
        Ok((gate, closed))
    }
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_cleanup_guard_retry(
    guard: *mut CleanupGuardHandle,
    changed: *mut u8,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call_with_output(
        error_output,
        changed,
        "cleanup changed output is null",
        || {
            // SAFETY: pointers are validated before use.
            let guard =
                unsafe { crate::handle::required_handle_input(guard, "cleanup guard is null")? };
            let changed = unsafe { required_output(changed, "cleanup changed output is null")? };
            *changed = 0;
            *changed = u8::from(guard.retry()?);
            Ok::<_, CallError>(())
        },
    )
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_cleanup_guard_close(
    guard: *mut CleanupGuardHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call(error_output, || {
        // SAFETY: the handle pointer is validated before use.
        let guard =
            unsafe { crate::handle::required_handle_input(guard, "cleanup guard is null")? };
        guard.close()?;
        Ok::<_, CallError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_cleanup_guard_destroy(
    guard: *mut CleanupGuardHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    if guard.is_null() {
        return STATUS_OK;
    }
    call(error_output, || {
        // SAFETY: the handle pointer is validated before ownership is consumed.
        let handle =
            unsafe { crate::handle::required_handle_input(guard, "cleanup guard is null")? };
        let (gate, pending) = handle.pending()?;
        if pending {
            return Err(BoundaryError::handle_busy("cleanup guard is still pending").into());
        }
        drop(gate);
        // SAFETY: this consumes the unique ABI-owned allocation exactly once.
        unsafe { drop(Box::from_raw(guard)) };
        Ok::<_, CallError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_residue_close(
    residue: *mut ResidueHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    call(error_output, || {
        // SAFETY: the handle pointer is validated before use.
        let residue =
            unsafe { crate::handle::required_handle_input(residue, "residue handle is null")? };
        residue.close()?;
        Ok::<_, CallError>(())
    })
}

#[no_mangle]
pub unsafe extern "C" fn iprange_v4_abi1_residue_destroy(
    residue: *mut ResidueHandle,
    error_output: *mut *mut ErrorHandle,
) -> u32 {
    if residue.is_null() {
        return STATUS_OK;
    }
    call(error_output, || {
        // SAFETY: the handle pointer is validated before ownership is consumed.
        let handle =
            unsafe { crate::handle::required_handle_input(residue, "residue handle is null")? };
        let (guard, closed) = handle.is_closed()?;
        if !closed {
            return Err(BoundaryError::handle_busy("residue handle must be closed").into());
        }
        drop(guard);
        // SAFETY: this consumes the unique ABI-owned allocation exactly once.
        unsafe { drop(Box::from_raw(residue)) };
        Ok::<_, CallError>(())
    })
}

impl From<PublicationProblem> for CallError {
    fn from(problem: PublicationProblem) -> Self {
        ErrorHandle::from_publication_problem(problem).into()
    }
}
