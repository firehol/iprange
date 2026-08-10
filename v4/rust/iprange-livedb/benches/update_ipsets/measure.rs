use std::fs;
use std::io::Write;
use std::path::Path;
use std::time::{Duration, Instant};

use crate::allocation::{self, AllocationStats};

#[derive(Clone, Copy, Debug, Default)]
pub(crate) struct FileSize {
    pub(crate) logical: u64,
    pub(crate) physical: Option<u64>,
}

#[derive(Clone, Debug)]
pub(crate) struct Measurement {
    pub(crate) elapsed: Duration,
    pub(crate) allocations: AllocationStats,
    pub(crate) rss_before_kib: Option<u64>,
    pub(crate) rss_after_kib: Option<u64>,
    pub(crate) rss_peak_kib: Option<u64>,
    pub(crate) fds_before: Option<u64>,
    pub(crate) fds_after: Option<u64>,
}

pub(crate) fn operation<T>(callback: impl FnOnce() -> T) -> (T, Measurement) {
    let rss_before_kib = current_rss_kib();
    let fds_before = open_file_descriptors();
    profiler_command(b"enable\n");
    let started = Instant::now();
    let (result, allocations) = allocation::measure(callback);
    let elapsed = started.elapsed();
    profiler_command(b"disable\n");
    let fds_after = open_file_descriptors();
    let rss_after_kib = current_rss_kib();
    (
        result,
        Measurement {
            elapsed,
            allocations,
            rss_before_kib,
            rss_after_kib,
            rss_peak_kib: peak_rss_kib(),
            fds_before,
            fds_after,
        },
    )
}

fn profiler_command(command: &[u8]) {
    let Some(path) = std::env::var_os("IPRANGE_PERF_CONTROL") else {
        return;
    };
    let mut control = fs::OpenOptions::new()
        .write(true)
        .open(&path)
        .unwrap_or_else(|error| panic!("open perf control {path:?}: {error}"));
    control
        .write_all(command)
        .unwrap_or_else(|error| panic!("write perf control {path:?}: {error}"));
}

pub(crate) fn file_size(path: &Path) -> std::io::Result<FileSize> {
    let metadata = fs::metadata(path)?;
    Ok(FileSize {
        logical: metadata.len(),
        physical: physical_bytes(&metadata),
    })
}

#[cfg(unix)]
fn physical_bytes(metadata: &fs::Metadata) -> Option<u64> {
    use std::os::unix::fs::MetadataExt;

    Some(metadata.blocks().saturating_mul(512))
}

#[cfg(not(unix))]
fn physical_bytes(_: &fs::Metadata) -> Option<u64> {
    None
}

#[cfg(target_os = "linux")]
fn current_rss_kib() -> Option<u64> {
    status_value_kib("VmRSS:")
}

#[cfg(not(target_os = "linux"))]
fn current_rss_kib() -> Option<u64> {
    None
}

#[cfg(target_os = "linux")]
fn peak_rss_kib() -> Option<u64> {
    status_value_kib("VmHWM:")
}

#[cfg(all(unix, not(target_os = "linux")))]
fn peak_rss_kib() -> Option<u64> {
    let mut usage = unsafe { std::mem::zeroed::<libc::rusage>() };
    if unsafe { libc::getrusage(libc::RUSAGE_SELF, &mut usage) } != 0 {
        return None;
    }
    let value = u64::try_from(usage.ru_maxrss).ok()?;
    #[cfg(target_os = "macos")]
    {
        Some(value / 1024)
    }
    #[cfg(not(target_os = "macos"))]
    {
        Some(value)
    }
}

#[cfg(not(unix))]
fn peak_rss_kib() -> Option<u64> {
    None
}

#[cfg(target_os = "linux")]
fn status_value_kib(label: &str) -> Option<u64> {
    let status = fs::read_to_string("/proc/self/status").ok()?;
    status.lines().find_map(|line| {
        line.strip_prefix(label)?
            .split_ascii_whitespace()
            .next()?
            .parse()
            .ok()
    })
}

#[cfg(target_os = "linux")]
fn open_file_descriptors() -> Option<u64> {
    Some(fs::read_dir("/proc/self/fd").ok()?.count() as u64)
}

#[cfg(not(target_os = "linux"))]
fn open_file_descriptors() -> Option<u64> {
    None
}
