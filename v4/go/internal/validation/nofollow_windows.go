//go:build windows

package validation

// unixO_NOFOLLOW is the no-follow open flag of the read-only source
// open. The Windows validation surface refuses before any path access
// (recorded SOW-0026 split), so the constant only keeps the shared source
// compiling.
const unixO_NOFOLLOW = 0
