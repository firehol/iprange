//! Public-SDK benchmarks shaped by the current update-ipsets publisher.
//!
//! Routine smoke:
//! `cargo bench --manifest-path v4/rust/Cargo.toml --bench update_ipsets -- smoke`
//!
//! Explicit production scale:
//! `cargo bench --manifest-path v4/rust/Cargo.toml --bench update_ipsets -- scale`

#[cfg(not(target_os = "freebsd"))]
#[path = "update_ipsets/allocation.rs"]
mod allocation;
#[cfg(not(target_os = "freebsd"))]
#[path = "update_ipsets/driver.rs"]
mod driver;
#[cfg(not(target_os = "freebsd"))]
#[path = "update_ipsets/measure.rs"]
mod measure;
#[cfg(not(target_os = "freebsd"))]
#[path = "update_ipsets/model.rs"]
mod model;
#[cfg(not(target_os = "freebsd"))]
#[path = "update_ipsets/report.rs"]
mod report;
#[cfg(not(target_os = "freebsd"))]
#[path = "update_ipsets/scenarios.rs"]
mod scenarios;
#[cfg(not(target_os = "freebsd"))]
#[path = "update_ipsets/source.rs"]
mod source;
#[cfg(not(target_os = "freebsd"))]
#[path = "update_ipsets/timing.rs"]
mod timing;

#[cfg(not(target_os = "freebsd"))]
fn main() {
    if let Err(error) = driver::run() {
        eprintln!("update-ipsets benchmark failed: {error}");
        std::process::exit(1);
    }
}

#[cfg(target_os = "freebsd")]
fn main() {}
