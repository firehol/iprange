//go:build windows && (amd64 || arm64)

package worker

// faultCodeValid reports whether one recorded worker fault code is
// plausible (Rust control.rs fault_code_valid): the Windows
// EXCEPTION_IN_PAGE_ERROR parameter two is an NTSTATUS (a negative
// int32 like STATUS_END_OF_FILE), so every claimed fault must carry a
// nonzero code.
func faultCodeValid(code int32) bool { return code != 0 }
