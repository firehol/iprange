//go:build !windows

package publication

import "github.com/firehol/iprange/v4/go/internal/format"

// listWindowsHousekeeping is the refused Windows-only housekeeping
// listing (Rust list_windows_housekeeping non-windows arm).
func listWindowsHousekeeping(path string, check func() error, sink func(entry *windowsHousekeepingEntry) error) (windowsHousekeepingList, error) {
	return windowsHousekeepingList{}, problem(format.CodeOSUnsupported, "Windows housekeeping is unavailable on this platform")
}

// removeWindowsHousekeeping is the refused Windows-only housekeeping
// removal (Rust remove_windows_housekeeping non-windows arm).
func removeWindowsHousekeeping(path string, expectedDirectory LocalFileIdentity, attempt [16]byte, ordinal uint32, expectedEnvelope LocalFileIdentity, expectedPayloadIdentity *residuePayloadIdentity, check func() error) (windowsHousekeepingRemoval, error) {
	return windowsHousekeepingRemoval{}, problem(format.CodeOSUnsupported, "Windows housekeeping is unavailable on this platform")
}
