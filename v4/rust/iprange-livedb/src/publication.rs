//! Crash-resolvable publication of complete immutable outputs.

#[cfg(target_os = "linux")]
mod main_file;
#[cfg(target_os = "linux")]
mod namespace;
#[cfg(target_os = "linux")]
mod output;
mod reservation;
#[cfg(target_os = "linux")]
mod reservation_file;
#[cfg(target_os = "linux")]
mod security;
