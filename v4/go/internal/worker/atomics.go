//go:build (linux || darwin || freebsd || windows) && (amd64 || arm64)

package worker

// mapAtomicLoad32 and mapAtomicStore32 are the mapped-control atomics
// (control_wire.go coordination fields and the naked fault handler).
// Go's sync/atomic is not specified for mapped memory, so each target's
// raw assembly implementation is the single authority: aligned 32-bit
// loads/stores on x86-64 and acquire/release ld/st on arm64, matching
// the Rust volatile/atomic access pattern of control.rs.
func mapAtomicLoad32(base uintptr, off uint32) uint32
func mapAtomicStore32(base uintptr, off uint32, value uint32)

// mapAtomicCas32 performs one compare-and-swap on the mapped control
// (Rust atomic_u32::compare_exchange): returns 1 when the stored word
// equaled old and was replaced by new, 0 otherwise.
func mapAtomicCas32(base uintptr, off uint32, old, new uint32) uint32
