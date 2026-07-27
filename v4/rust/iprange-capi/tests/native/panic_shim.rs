#[no_mangle]
pub unsafe extern "C" fn iprange_v4_native_test_panic(error_code: *mut u32) -> u32 {
    let (status, code) = iprange_v4::native_test_panic_probe();
    if !error_code.is_null() {
        // SAFETY: the native test supplies one writable u32.
        unsafe { error_code.write(code) };
    }
    status
}
