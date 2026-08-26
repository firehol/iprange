//go:build windows

package mapping

// ExchangeAvailable reports whether the target has an atomic name
// exchange (Rust require_exchange_available: linux/apple only).
// Windows refuses the exchange exactly like Rust; the live sidecar
// replacement path uses replace_discarding_destination
// (live/directory_windows.go RenamePlain).
func ExchangeAvailable() bool { return false }
