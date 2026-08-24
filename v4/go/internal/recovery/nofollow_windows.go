//go:build windows

package recovery

// unixO_NOFOLLOW is the no-follow open flag of the quiescent source
// open. The recovery Windows surface refuses before any path access
// through the live coordination gate (recorded M5 split), so the
// constant only keeps the source compiling.
const unixO_NOFOLLOW = 0
