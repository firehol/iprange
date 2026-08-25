//go:build windows

package publication

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
)

// listWindowsHousekeeping streams one offline housekeeping scan of
// the retained directory (Rust list_windows_housekeeping windows arm
// over gc_maintenance::list): every GC candidate is inspected against
// its envelope and delivered to the sink; no deletion authority is
// granted.
func listWindowsHousekeeping(path string, check func() error, sink func(entry *windowsHousekeepingEntry) error) (windowsHousekeepingList, error) {
	return live.GCListHousekeeping(path, check, sink)
}

// removeWindowsHousekeeping resolves and best-effort removes one exact
// GC pair after the caller certified the expected identities (Rust
// remove_windows_housekeeping windows arm over gc_maintenance::remove;
// the payload expectation must be coherent or the call is
// InvalidArgument, exactly like the Rust payload() fold).
func removeWindowsHousekeeping(path string, expectedDirectory LocalFileIdentity, attempt [16]byte, ordinal uint32, expectedEnvelope LocalFileIdentity, expectedPayloadIdentity *residuePayloadIdentity, check func() error) (windowsHousekeepingRemoval, error) {
	expectedPayload, err := gcHousekeepingPayloadOf(expectedPayloadIdentity)
	if err != nil {
		return windowsHousekeepingRemoval{}, err
	}
	return live.GCRemoveHousekeeping(path, expectedDirectory, attempt, ordinal, expectedEnvelope, expectedPayload, check)
}

// gcHousekeepingPayloadOf validates and converts one residue payload
// expectation to the GC envelope payload (Rust gc_maintenance::payload:
// the digest must be exact and the tuple complete or fully absent).
func gcHousekeepingPayloadOf(expected *residuePayloadIdentity) (*live.GCHousekeepingPayload, error) {
	if expected == nil {
		return nil, nil
	}
	if expected.digest.byteLength == 0 || expected.digest.sha512 == [64]byte{} {
		return nil, &format.Error{Code: format.CodeInvalidArgument, Detail: "housekeeping payload identity is malformed"}
	}
	payload := &live.GCHousekeepingPayload{
		ByteLength: expected.digest.byteLength,
		SHA512:     expected.digest.sha512,
	}
	if expected.tuple != nil {
		if expected.tuple.databaseID == [16]byte{} || expected.tuple.transactionID == 0 || expected.tuple.commitNonce == [16]byte{} {
			return nil, &format.Error{Code: format.CodeInvalidArgument, Detail: "housekeeping payload identity is malformed"}
		}
		payload.DatabaseID = expected.tuple.databaseID
		payload.TransactionID = expected.tuple.transactionID
		payload.CommitNonce = expected.tuple.commitNonce
	}
	return payload, nil
}
