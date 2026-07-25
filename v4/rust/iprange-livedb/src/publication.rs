//! Crash-resolvable publication of complete immutable outputs.

#[cfg(target_os = "linux")]
mod attempt;
#[cfg(target_os = "linux")]
mod cleanup;
#[cfg(target_os = "linux")]
mod file_inspection;
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
mod reservation_inspection;
#[cfg(target_os = "linux")]
mod resolver;
#[cfg(target_os = "linux")]
mod result;
#[cfg(target_os = "linux")]
mod security;

#[cfg(all(test, target_os = "linux"))]
#[path = "publication/crash_tests.rs"]
mod crash_tests;
