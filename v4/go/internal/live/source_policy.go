package live

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/security"
)

// SourceCreatorOnly reports the creator-only choice a live database's
// sidecar records, read from the sidecar header alone: one mapped page,
// no database open, no locks, no reader registration (Rust
// live_sidecar::source_creator_only). Pre-decision (generation 0) and
// generation-1 protected count as protected; generation-1 unprotected
// counts as unprotected. A missing or unreadable sidecar — every
// immutable source has none — and any corrupt header follow the process
// switch, exactly like an unclassified source. This is an advisory
// policy read for output creation, never an access check.
func SourceCreatorOnly(main string) bool {
	path, err := CanonicalSidecarPath(main)
	if err != nil {
		return security.CreatorOnlyRequested()
	}
	file, err := os.Open(path)
	if err != nil {
		return security.CreatorOnlyRequested()
	}
	defer file.Close()
	mapped, err := mapping.MapFile(file, sidecarPageSize, false)
	if err != nil {
		return security.CreatorOnlyRequested()
	}
	defer mapped.Close()
	page, err := mapped.View(0, sidecarPageSize)
	if err != nil {
		return security.CreatorOnlyRequested()
	}
	if !headerShapeValid(page) || !headerChecksumValid(page) {
		return security.CreatorOnlyRequested()
	}
	policy, err := decodePolicy(page)
	if err != nil {
		return security.CreatorOnlyRequested()
	}
	return policy != policyUnprotected
}
