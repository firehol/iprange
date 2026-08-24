//go:build linux

package mapping

// ExchangeAvailable reports whether the target has an atomic name
// exchange (Rust require_exchange_available: linux and apple only).
func ExchangeAvailable() bool { return true }
