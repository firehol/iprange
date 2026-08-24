//go:build windows

package mapping

// ExchangeAvailable reports whether the target has an atomic name
// exchange (Rust require_exchange_available). Windows publication is
// a tracked M5 surface; the honest refusal stance reports the
// exchange absent.
func ExchangeAvailable() bool { return false }
