//go:build !windows

package worker

// faultCodeValid reports whether one recorded worker fault code is
// plausible (Rust control.rs fault_code_valid): the POSIX SIGBUS
// si_code is a small positive enumeration (SEGV_MAPERR etc.), so
// every claimed fault must carry a positive code.
func faultCodeValid(code int32) bool { return code > 0 }
