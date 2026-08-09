//! Signal-safe access to the mapped worker fault record.

use std::ptr;
use std::sync::atomic::AtomicU32;

pub(super) fn read_u32(base: *mut u8, at: usize) -> u32 {
    // SAFETY: Callers pass a fixed aligned field in the mapped control record.
    unsafe { ptr::read_volatile(base.add(at).cast::<u32>()) }
}

pub(super) fn read_u64(base: *mut u8, at: usize) -> u64 {
    // SAFETY: Callers pass a fixed aligned field in the mapped control record.
    unsafe { ptr::read_volatile(base.add(at).cast::<u64>()) }
}

pub(super) fn write_u32(base: *mut u8, at: usize, value: u32) {
    // SAFETY: Callers pass a fixed aligned field in the mapped control record.
    unsafe { ptr::write_volatile(base.add(at).cast::<u32>(), value) }
}

pub(super) fn write_i32(base: *mut u8, at: usize, value: i32) {
    // SAFETY: Callers pass a fixed aligned field in the mapped control record.
    unsafe { ptr::write_volatile(base.add(at).cast::<i32>(), value) }
}

pub(super) fn write_u64(base: *mut u8, at: usize, value: u64) {
    // SAFETY: Callers pass a fixed aligned field in the mapped control record.
    unsafe { ptr::write_volatile(base.add(at).cast::<u64>(), value) }
}

pub(super) fn atomic_u32(base: *mut u8, at: usize) -> &'static AtomicU32 {
    // SAFETY: Callers pass a fixed aligned atomic field in the mapped control
    // record, whose mapping outlives the isolated worker handler.
    unsafe { &*base.add(at).cast::<AtomicU32>() }
}
