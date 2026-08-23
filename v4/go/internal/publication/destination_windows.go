//go:build windows

// Windows stub of the publication destination surface: the whole
// namespace and live surface refuses on Windows (Rust namespace/
// windows.rs is a tracked M5 item), so binding is a typed refusal of
// the Unsupported class before any path access.

package publication

import "github.com/firehol/iprange/v4/go/internal/live"

type destination struct{}

// bindDestination refuses on Windows (Rust namespace/windows.rs is a
// tracked M5 surface; Go keeps the honest-refusal stance).
func bindDestination(string) (*destination, error) {
	return nil, &live.NamespaceError{Kind: live.NamespaceUnsupported}
}
