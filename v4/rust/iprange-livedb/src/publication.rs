//! Crash-resolvable publication of complete immutable outputs.

#[cfg(target_os = "linux")]
mod namespace;
#[cfg(target_os = "linux")]
mod output;
mod reservation;
#[cfg(target_os = "linux")]
mod security;
