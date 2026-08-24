//go:build windows

package validation

// The Windows validation surface refuses before any path access: the
// GC-custody source binding of Rust validation (require_main_available
// and the Windows bootstrap arms) is a tracked M5 item, consistent
// with the live and publication Windows stubs. The types compile on
// every target so the SDK facade has one shape.
