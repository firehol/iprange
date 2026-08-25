package publication

import (
	"github.com/firehol/iprange/v4/go/internal/live"
)

// windowsHousekeepingCandidateKind classifies one scanned housekeeping
// name (Rust WindowsHousekeepingCandidateKind; the live GC machine
// owns the values).
type windowsHousekeepingCandidateKind = live.GCHousekeepingCandidateKind

const (
	windowsHousekeepingCandidateEnvelope     = live.GCCandidateEnvelope
	windowsHousekeepingCandidateInertPayload = live.GCCandidateInertPayload
)

// windowsHousekeepingEntry is one exact scanned housekeeping
// candidate (Rust WindowsHousekeepingEntry).
type windowsHousekeepingEntry = live.GCHousekeepingEntry

// windowsHousekeepingList is one completed constant-memory scan (Rust
// WindowsHousekeepingList).
type windowsHousekeepingList = live.GCHousekeepingList

// windowsHousekeepingRemoval is the factual terminal state of one
// removal (Rust WindowsHousekeepingRemoval).
type windowsHousekeepingRemoval = live.GCHousekeepingRemoval
