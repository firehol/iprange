//go:build freebsd || netbsd || openbsd || dragonfly

package mapping

// ExchangeAvailable reports whether the target has an atomic name
// exchange (Rust require_exchange_available: linux and apple only;
// freebsd, netbsd, openbsd and dragonfly have no atomic exchange
// primitive, so the rollback-safe replacement policy refuses at the
// composition gate).
func ExchangeAvailable() bool { return false }
