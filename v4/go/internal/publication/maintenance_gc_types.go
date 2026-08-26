package publication

import (
	"github.com/firehol/iprange/v4/go/internal/live"
)

// WindowsHousekeepingCandidateKind classifies one scanned housekeeping
// name (Rust WindowsHousekeepingCandidateKind; the live GC machine
// owns the values).
type WindowsHousekeepingCandidateKind = live.GCHousekeepingCandidateKind

const (
	WindowsHousekeepingCandidateEnvelope     = live.GCCandidateEnvelope
	WindowsHousekeepingCandidateInertPayload = live.GCCandidateInertPayload
)

// WindowsHousekeepingEntry is one exact scanned housekeeping
// candidate (Rust WindowsHousekeepingEntry).
type WindowsHousekeepingEntry = live.GCHousekeepingEntry

// WindowsHousekeepingList is one completed constant-memory scan (Rust
// WindowsHousekeepingList).
type WindowsHousekeepingList = live.GCHousekeepingList

// WindowsHousekeepingPayloadIdentity is the optional exact content
// evidence supplied to one housekeeping removal (Rust
// HousekeepingPayloadIdentity): the tuple must be complete or fully
// absent and the digest must be the exact complete-file evidence.
type WindowsHousekeepingPayloadIdentity struct {
	Tuple  *PublicationTuple
	Digest PublicationDigest
}

// WindowsHousekeepingRemoval is the factual terminal state of one
// removal (Rust WindowsHousekeepingRemoval).
type WindowsHousekeepingRemoval = live.GCHousekeepingRemoval
