//! Test-only process crash points.

#[cfg(test)]
pub(crate) fn crash(point: &'static str) {
    if std::env::var_os("IPRANGE_V4_TEST_CRASH_AT").as_deref() == Some(point.as_ref()) {
        unsafe { libc::_exit(86) }
    }
}

#[cfg(not(test))]
#[inline(always)]
pub(crate) fn crash(_point: &'static str) {}
