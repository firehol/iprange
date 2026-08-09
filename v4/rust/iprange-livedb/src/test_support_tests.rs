//! Shared unit-test support.

use std::path::PathBuf;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{SystemTime, UNIX_EPOCH};

use crate::contract::PAGE_SIZE;
use crate::error::{Error, Result};

static NEXT_PATH: AtomicU64 = AtomicU64::new(0);

pub(crate) fn unique_path(prefix: &str) -> PathBuf {
    let time = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap()
        .as_nanos();
    let sequence = NEXT_PATH.fetch_add(1, Ordering::Relaxed);
    std::env::temp_dir().join(format!("{prefix}-{}-{time}-{sequence}", std::process::id()))
}

pub(crate) fn copy_pages<'a, T>(
    pages: &'a mut [[u8; PAGE_SIZE]],
    source: u32,
    destination: u32,
    copy: impl FnOnce(&'a [u8; PAGE_SIZE], &'a mut [u8; PAGE_SIZE]) -> Result<T>,
) -> Result<T> {
    let source = source as usize;
    let destination = destination as usize;
    if source == destination || source >= pages.len() || destination >= pages.len() {
        return Err(Error::Corrupt("test copy pages are invalid"));
    }
    let (source_page, destination_page) = if source < destination {
        let (left, right) = pages.split_at_mut(destination);
        (&left[source], &mut right[0])
    } else {
        let (left, right) = pages.split_at_mut(source);
        (&right[0], &mut left[destination])
    };
    copy(source_page, destination_page)
}
