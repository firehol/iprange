//! Crash-resolvable publication of complete immutable outputs.

#[cfg(target_os = "linux")]
mod attempt;
#[cfg(target_os = "linux")]
mod cleanup;
#[cfg(target_os = "linux")]
mod main_file;
#[cfg(target_os = "linux")]
mod namespace;
#[cfg(target_os = "linux")]
mod output;
#[cfg(target_os = "linux")]
mod problem;
mod reservation;
#[cfg(target_os = "linux")]
mod reservation_file;
#[cfg(target_os = "linux")]
mod result;
#[cfg(target_os = "linux")]
mod security;
