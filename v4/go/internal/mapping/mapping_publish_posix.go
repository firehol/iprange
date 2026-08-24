//go:build freebsd || netbsd

package mapping

// ExchangeAvailable reports whether the target has an atomic name
// exchange (Rust require_exchange_available: freebsd and netbsd have
// no atomic exchange primitive, so the rollback-safe replacement
// policy refuses at the composition gate).
func ExchangeAvailable() bool { return false }
