// Publication housekeeping facts of one lifecycle attempt (Rust
// publication/types.rs Housekeeping). On POSIX the live lifecycle never
// produces housekeeping artifacts (the Windows GC retirement machinery
// does), so the enum and its merge rule are the whole portable surface;
// the artifact ledger itself lands with the publication resolver slice.

package live

// housekeeping is the fact class of one attempted cleanup (Rust
// publication::Housekeeping).
type housekeeping uint8

const (
	housekeepingNone housekeeping = iota
	housekeepingCrashReappearancePossible
	housekeepingVisible
)

// merge combines two housekeeping facts with the Rust rule: Visible
// dominates, then CrashReappearancePossible.
func (h housekeeping) merge(other housekeeping) housekeeping {
	if h == housekeepingVisible || other == housekeepingVisible {
		return housekeepingVisible
	}
	if h == housekeepingCrashReappearancePossible || other == housekeepingCrashReappearancePossible {
		return housekeepingCrashReappearancePossible
	}
	return housekeepingNone
}
