// Public Windows housekeeping maintenance surface (Rust publication.rs
// list_windows_housekeeping / remove_windows_housekeeping): the listing
// streams every exact GC housekeeping candidate of one directory in
// constant memory without granting deletion authority, and the removal
// resolves and best-effort removes one exact authenticated GC pair
// after the caller certified the expected identities. On every
// non-Windows platform both entries refuse with the OS-unsupported
// class exactly like the Rust non-windows arms. Every entry point
// accepts the shared cancellation token.

package iprangedb

import (
	"github.com/firehol/iprange/v4/go/internal/publication"
)

// WindowsHousekeepingCandidateKind classifies one scanned housekeeping
// name (Rust WindowsHousekeepingCandidateKind).
type WindowsHousekeepingCandidateKind = publication.WindowsHousekeepingCandidateKind

const (
	// WindowsHousekeepingCandidateEnvelope is one GC envelope name.
	WindowsHousekeepingCandidateEnvelope = publication.WindowsHousekeepingCandidateEnvelope
	// WindowsHousekeepingCandidateInertPayload is one inert payload
	// name whose envelope is absent or already retired.
	WindowsHousekeepingCandidateInertPayload = publication.WindowsHousekeepingCandidateInertPayload
)

// WindowsHousekeepingEntry is one exact scanned housekeeping candidate
// (Rust WindowsHousekeepingEntry). The entry is valid only for the
// duration of one sink call; the scan never grants deletion authority.
type WindowsHousekeepingEntry struct {
	DirectoryIdentity FileIdentity
	CandidateKind     WindowsHousekeepingCandidateKind
	BasenameEncoding  uint16
	Basename          []byte
	Identity          *FileIdentity
	AttemptID         *[16]byte
	Ordinal           *uint32
	Artifact          *HousekeepingArtifact
	Problem           error
}

// WindowsHousekeepingList is one completed constant-memory scan (Rust
// WindowsHousekeepingList).
type WindowsHousekeepingList = publication.WindowsHousekeepingList

// WindowsHousekeepingPayloadIdentity is the optional exact content
// evidence supplied to one removal (Rust HousekeepingPayloadIdentity):
// the tuple must be complete or fully absent and the digest must be
// the exact complete-file evidence.
type WindowsHousekeepingPayloadIdentity = publication.WindowsHousekeepingPayloadIdentity

// WindowsHousekeepingRemoval is the factual terminal state of one
// removal (Rust WindowsHousekeepingRemoval).
type WindowsHousekeepingRemoval struct {
	Housekeeping        Housekeeping
	VisibleHousekeeping []HousekeepingArtifact
	Cause               error
}

// ListWindowsHousekeeping streams one offline housekeeping scan of one
// directory in constant memory (Rust list_windows_housekeeping): every
// GC candidate is inspected against its envelope and delivered to the
// sink; no deletion authority is granted. On non-Windows platforms the
// call refuses with the OS-unsupported class.
func ListWindowsHousekeeping(directory string, cancellation *CancellationToken, sink func(entry *WindowsHousekeepingEntry) error) (WindowsHousekeepingList, error) {
	list, err := publication.ListWindowsHousekeeping(directory, publicationCheck(cancellation), func(entry *publication.WindowsHousekeepingEntry) error {
		return sink(publicWindowsHousekeepingEntry(entry))
	})
	if err != nil {
		return WindowsHousekeepingList{}, publicError(err)
	}
	return list, nil
}

// RemoveWindowsHousekeeping resolves and best-effort removes one exact
// authenticated GC pair after the caller certified the expected
// identities (Rust remove_windows_housekeeping): a malformed payload
// expectation is InvalidArgument, and every outcome keeps the exact
// housekeeping and visible-artifact evidence.
func RemoveWindowsHousekeeping(directory string, expectedDirectory FileIdentity, attempt [16]byte, ordinal uint32, expectedEnvelope FileIdentity, expectedPayload *WindowsHousekeepingPayloadIdentity, cancellation *CancellationToken) (WindowsHousekeepingRemoval, error) {
	removal, err := publication.RemoveWindowsHousekeeping(directory, expectedDirectory, attempt, ordinal, expectedEnvelope, expectedPayload, publicationCheck(cancellation))
	if err != nil {
		return WindowsHousekeepingRemoval{}, publicError(err)
	}
	return publicWindowsHousekeepingRemoval(removal), nil
}

// publicWindowsHousekeepingRemoval folds one internal removal terminal
// onto the public shape (Rust WindowsHousekeepingRemoval): the
// housekeeping and visible-artifact facts are the same alias types,
// and the internal cause class maps to the exported error type.
func publicWindowsHousekeepingRemoval(removal publication.WindowsHousekeepingRemoval) WindowsHousekeepingRemoval {
	return WindowsHousekeepingRemoval{
		Housekeeping:        removal.Housekeeping,
		VisibleHousekeeping: removal.Visible,
		Cause:               publicError(removal.Cause),
	}
}

// publicWindowsHousekeepingEntry folds one internal scan entry onto the
// public shape (Rust WindowsHousekeepingEntry): the identity, attempt,
// ordinal, and artifact facts are the same alias types, and the
// internal problem class maps to the exported error type.
func publicWindowsHousekeepingEntry(entry *publication.WindowsHousekeepingEntry) *WindowsHousekeepingEntry {
	if entry == nil {
		return nil
	}
	return &WindowsHousekeepingEntry{
		DirectoryIdentity: entry.DirectoryIdentity,
		CandidateKind:     entry.CandidateKind,
		BasenameEncoding:  entry.BasenameEncoding,
		Basename:          entry.Basename,
		Identity:          entry.Identity,
		AttemptID:         entry.AttemptID,
		Ordinal:           entry.Ordinal,
		Artifact:          entry.Artifact,
		Problem:           publicError(entry.Problem),
	}
}
