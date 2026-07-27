//! Public-SDK benchmarks shaped by the current update-ipsets publisher.
//!
//! Routine smoke:
//! `cargo bench --manifest-path v4/rust/Cargo.toml --bench update_ipsets -- smoke`
//!
//! Explicit production scale:
//! `cargo bench --manifest-path v4/rust/Cargo.toml --bench update_ipsets -- scale`

#[path = "update_ipsets/allocation.rs"]
mod allocation;
#[path = "update_ipsets/driver.rs"]
mod driver;
#[path = "update_ipsets/measure.rs"]
mod measure;
#[path = "update_ipsets/model.rs"]
mod model;
#[path = "update_ipsets/scenarios.rs"]
mod scenarios;
#[path = "update_ipsets/source.rs"]
mod source;

fn main() {
    if let Err(error) = driver::run() {
        eprintln!("update-ipsets benchmark failed: {error}");
        std::process::exit(1);
    }
}
