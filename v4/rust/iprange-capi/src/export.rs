//! ABI identity export.

use crate::abi::ABI_VERSION;

#[no_mangle]
pub extern "C" fn iprange_v4_abi1_version() -> u32 {
    ABI_VERSION
}
