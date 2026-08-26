//go:build !windows

package publication

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
)

// ListWindowsHousekeeping is the refused Windows-only housekeeping
// listing (Rust list_windows_housekeeping non-windows arm).
func ListWindowsHousekeeping(path string, check func() error, sink func(entry *WindowsHousekeepingEntry) error) (WindowsHousekeepingList, error) {
	if err := live.Checkpoint(check); err != nil {
		return WindowsHousekeepingList{}, sdkProblem(err)
	}
	return WindowsHousekeepingList{}, problem(format.CodeOSUnsupported, "Windows housekeeping is unavailable on this platform")
}

// RemoveWindowsHousekeeping is the refused Windows-only housekeeping
// removal (Rust remove_windows_housekeeping non-windows arm).
func RemoveWindowsHousekeeping(path string, expectedDirectory LocalFileIdentity, attempt [16]byte, ordinal uint32, expectedEnvelope LocalFileIdentity, expectedPayload *WindowsHousekeepingPayloadIdentity, check func() error) (WindowsHousekeepingRemoval, error) {
	if err := live.Checkpoint(check); err != nil {
		return WindowsHousekeepingRemoval{}, sdkProblem(err)
	}
	return WindowsHousekeepingRemoval{}, problem(format.CodeOSUnsupported, "Windows housekeeping is unavailable on this platform")
}
