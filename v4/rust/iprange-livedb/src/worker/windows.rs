use std::ffi::c_void;
use std::ptr;
use std::sync::atomic::{AtomicPtr, Ordering};

use windows_sys::Win32::Foundation::EXCEPTION_IN_PAGE_ERROR;
use windows_sys::Win32::System::Diagnostics::Debug::{
    AddVectoredExceptionHandler, RemoveVectoredExceptionHandler, EXCEPTION_CONTINUE_SEARCH,
    EXCEPTION_POINTERS,
};
use windows_sys::Win32::System::Threading::{GetCurrentProcess, TerminateProcess};

use crate::error::{Error, Result};

use super::control::{Control, State, OWNED_FAULT_EXIT};
use super::fault_memory::{atomic_u32, read_u32, read_u64, write_i32, write_u32, write_u64};

static ACTIVE_CONTROL: AtomicPtr<u8> = AtomicPtr::new(ptr::null_mut());

pub(super) struct Handler<'a> {
    control: &'a Control,
    handle: *mut c_void,
}

impl<'a> Handler<'a> {
    pub(super) fn install(control: &'a Control) -> Result<Self> {
        if ACTIVE_CONTROL
            .compare_exchange(
                ptr::null_mut(),
                control.base(),
                Ordering::Release,
                Ordering::Relaxed,
            )
            .is_err()
        {
            return Err(Error::Conflict(
                "mapped-fault worker handler is already installed",
            ));
        }
        // SAFETY: Installs one first-priority handler in the isolated worker.
        let handle = unsafe { AddVectoredExceptionHandler(1, Some(exception_handler)) };
        if handle.is_null() {
            ACTIVE_CONTROL.store(ptr::null_mut(), Ordering::Release);
            return Err(std::io::Error::last_os_error().into());
        }
        Ok(Self {
            control,
            handle: handle.cast(),
        })
    }
}

impl Drop for Handler<'_> {
    fn drop(&mut self) {
        self.control.disarm();
        ACTIVE_CONTROL.store(ptr::null_mut(), Ordering::Release);
        // SAFETY: `handle` was returned by AddVectoredExceptionHandler.
        let _ = unsafe { RemoveVectoredExceptionHandler(self.handle) };
    }
}

pub(super) fn verify_owned(control: &Control) -> Result<()> {
    if ACTIVE_CONTROL.load(Ordering::Acquire) == control.base() {
        Ok(())
    } else {
        Err(Error::Conflict(
            "mapped-fault worker handler ownership was lost",
        ))
    }
}

unsafe extern "system" fn exception_handler(pointers: *mut EXCEPTION_POINTERS) -> i32 {
    let control = ACTIVE_CONTROL.load(Ordering::Acquire);
    if control.is_null() || pointers.is_null() {
        return EXCEPTION_CONTINUE_SEARCH;
    }
    // SAFETY: Windows supplies valid exception pointers for this callback.
    let record = unsafe { (*pointers).ExceptionRecord };
    if record.is_null() || unsafe { (*record).ExceptionCode } != EXCEPTION_IN_PAGE_ERROR {
        return EXCEPTION_CONTINUE_SEARCH;
    }
    // EXCEPTION_IN_PAGE_ERROR exposes operation, accessed address, and NTSTATUS.
    if unsafe { (*record).NumberParameters } < 3 {
        return EXCEPTION_CONTINUE_SEARCH;
    }
    let address = unsafe { (*record).ExceptionInformation[1] as u64 };
    let fields = Control::fault_fields();
    if atomic_u32(control, fields.armed).load(Ordering::Acquire) != 1 {
        return EXCEPTION_CONTINUE_SEARCH;
    }
    let generation = read_u64(control, fields.generation);
    let role = read_u32(control, fields.role);
    let base = read_u64(control, fields.base);
    let len = read_u64(control, fields.len);
    let Some(relative) = address.checked_sub(base) else {
        return EXCEPTION_CONTINUE_SEARCH;
    };
    if generation == 0 || !(1..=4).contains(&role) || len == 0 || relative >= len {
        return EXCEPTION_CONTINUE_SEARCH;
    }

    if atomic_u32(control, fields.handling)
        .compare_exchange(0, 1, Ordering::AcqRel, Ordering::Relaxed)
        .is_err()
    {
        return EXCEPTION_CONTINUE_SEARCH;
    }
    write_u64(control, fields.fault_generation, generation);
    write_u32(control, fields.fault_role, role);
    write_i32(control, fields.fault_code, unsafe {
        (*record).ExceptionInformation[2] as i32
    });
    write_u64(control, fields.fault_relative, relative);
    write_u64(control, fields.fault_address, address);
    write_u32(control, fields.fault_marker, fields.marker);
    atomic_u32(control, fields.state).store(State::Fault as u32, Ordering::Release);
    // SAFETY: Owned faults terminate only this isolated worker.
    unsafe { TerminateProcess(GetCurrentProcess(), OWNED_FAULT_EXIT as u32) };
    EXCEPTION_CONTINUE_SEARCH
}
