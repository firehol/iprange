//go:build !windows

// Public Windows housekeeping refusal test (Rust
// list_windows_housekeeping / remove_windows_housekeeping non-windows
// arms): both entries refuse with the OS-unsupported class of the
// exported error type, before any directory access and with every
// identity argument present.

package iprangedb

import "testing"

// TestPublicWindowsHousekeepingRefusesOffWindows pins the non-Windows
// arm: both entries refuse with the OS-unsupported class of the
// exported error type, before any directory access and with every
// identity argument present.
func TestPublicWindowsHousekeepingRefusesOffWindows(t *testing.T) {
	dir := t.TempDir()
	identity := FileIdentity{Kind: 1}
	attempt := [16]byte{7}
	payload := &WindowsHousekeepingPayloadIdentity{
		Digest: PublicationDigest{ByteLength: 1, SHA512: [64]byte{1}},
	}

	if _, err := ListWindowsHousekeeping(dir, nil, func(entry *WindowsHousekeepingEntry) error {
		t.Fatal("sink called on refusal")
		return nil
	}); !hasPublicCode(err, ErrorOSUnsupported) {
		t.Fatalf("list error = %v, want OSUnsupported", err)
	}
	if _, err := RemoveWindowsHousekeeping(dir, identity, attempt, 3, identity, payload, nil); !hasPublicCode(err, ErrorOSUnsupported) {
		t.Fatalf("remove error = %v, want OSUnsupported", err)
	}
}
