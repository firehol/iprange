package iprangedb

import "github.com/firehol/iprange/v4/go/internal/live"

// SourceCreatorOnly reports the creator-only choice a live database's
// sidecar records, read from the sidecar header alone: one mapped page,
// no database open, no locks, no reader registration (Rust
// live_sidecar::source_creator_only). Pre-decision (generation 0) and
// generation-1 protected count as protected; generation-1 unprotected
// counts as unprotected. A missing or unreadable sidecar — every
// immutable source has none — and any corrupt header follow the process
// switch IPRANGE_CREATOR_ONLY=1, exactly like an unclassified source.
//
// This is an advisory policy read for output creation, never an access
// check: outputs whose creation should follow a source database's
// recorded choice consult this instead of opening the database.
func SourceCreatorOnly(main string) bool {
	return live.SourceCreatorOnly(main)
}
