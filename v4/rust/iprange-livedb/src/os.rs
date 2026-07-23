//! Platform adapters for retained-descriptor live coordination.

#[cfg(target_os = "linux")]
pub(crate) mod linux;
