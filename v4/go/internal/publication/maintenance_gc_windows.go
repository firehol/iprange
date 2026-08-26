//go:build windows

package publication

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
)

// ListWindowsHousekeeping streams one offline housekeeping scan of
// the retained directory (Rust list_windows_housekeeping windows arm
// over gc_maintenance::list): every GC candidate is inspected against
// its envelope and delivered to the sink; no deletion authority is
// granted.
func ListWindowsHousekeeping(path string, check func() error, sink func(entry *WindowsHousekeepingEntry) error) (WindowsHousekeepingList, error) {
	return live.GCListHousekeeping(path, check, deliverWindowsHousekeeping(sink))
}

// RemoveWindowsHousekeeping resolves and best-effort removes one exact
// GC pair after the caller certified the expected identities (Rust
// remove_windows_housekeeping windows arm over gc_maintenance::remove;
// the payload expectation must be coherent or the call is
// InvalidArgument, exactly like the Rust payload() fold).
func RemoveWindowsHousekeeping(path string, expectedDirectory LocalFileIdentity, attempt [16]byte, ordinal uint32, expectedEnvelope LocalFileIdentity, expectedPayload *WindowsHousekeepingPayloadIdentity, check func() error) (WindowsHousekeepingRemoval, error) {
	expectedGC, err := gcHousekeepingPayloadOf(expectedPayload)
	if err != nil {
		return WindowsHousekeepingRemoval{}, err
	}
	return live.GCRemoveHousekeeping(path, expectedDirectory, attempt, ordinal, expectedEnvelope, expectedGC, check)
}

// gcHousekeepingPayloadOf validates and converts one public payload
// expectation to the GC envelope payload (Rust gc_maintenance::payload:
// the digest must be exact and the tuple complete or fully absent).
func gcHousekeepingPayloadOf(expected *WindowsHousekeepingPayloadIdentity) (*live.GCHousekeepingPayload, error) {
	if expected == nil {
		return nil, nil
	}
	if expected.Digest.ByteLength == 0 || expected.Digest.SHA512 == [64]byte{} {
		return nil, &format.Error{Code: format.CodeInvalidArgument, Detail: "housekeeping payload identity is malformed"}
	}
	payload := &live.GCHousekeepingPayload{
		ByteLength: expected.Digest.ByteLength,
		SHA512:     expected.Digest.SHA512,
	}
	if expected.Tuple != nil {
		if expected.Tuple.DatabaseID == [16]byte{} || expected.Tuple.TransactionID == 0 || expected.Tuple.CommitNonce == [16]byte{} {
			return nil, &format.Error{Code: format.CodeInvalidArgument, Detail: "housekeeping payload identity is malformed"}
		}
		payload.DatabaseID = expected.Tuple.DatabaseID
		payload.TransactionID = expected.Tuple.TransactionID
		payload.CommitNonce = expected.Tuple.CommitNonce
	}
	return payload, nil
}
