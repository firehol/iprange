package live

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/calleropen"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/security"
)

// SourceCreatorOnly reports the creator-only choice a live database's
// sidecar records, read from the sidecar header alone: one mapped page,
// no database open, no locks, no reader registration (Rust
// live_sidecar::source_creator_only). Pre-decision (generation 0) and
// generation-1 protected count as protected; generation-1 unprotected
// counts as unprotected. A missing or unreadable sidecar — every
// immutable source has none — and any corrupt header (including a
// valid-CRC header whose identities are zero, the full read_header
// rule) follow the process switch, exactly like an unclassified
// source. This is an advisory policy read for output creation, never
// an access check.
// TransitionCreatorOnly is the transition-compatible classification
// for a main file whose coordination is being (re)published
// (initialize/reset). A readable sidecar's recorded policy governs as
// in SourceCreatorOnly; a MISSING or corrupt sidecar has no recorded
// choice, and the replacement must be compatible with the retained
// main: Protected only when the main itself already satisfies the
// protected contract (the transition must not silently change
// existing access, and recording Protected over an unprotected main
// locks the next live open out — the sol gate's round-2 P1).
func TransitionCreatorOnly(mainPath string, mainFile *os.File) bool {
	path, err := CanonicalSidecarPath(mainPath)
	if err == nil {
		if file, err := calleropen.Open(path, os.O_RDONLY|calleropen.NonBlocking, 0); err == nil {
			recorded, ok := func() (bool, bool) {
				defer file.Close()
				mapped, err := mapping.MapFile(file, sidecarPageSize, false)
				if err != nil {
					return false, false
				}
				defer mapped.Close()
				page, err := mapped.View(0, sidecarPageSize)
				if err != nil {
					return false, false
				}
				if !headerShapeValid(page) || !headerChecksumValid(page) || !headerIdentitiesValid(page) {
					return false, false
				}
				policy, err := decodePolicy(page)
				if err != nil {
					return false, false
				}
				return policy != policyUnprotected, true
			}()
			if ok {
				return recorded
			}
		}
	}
	return mainSatisfiesProtectedContract(mainFile)
}

func SourceCreatorOnly(main string) bool {
	path, err := CanonicalSidecarPath(main)
	if err != nil {
		return security.CreatorOnlyRequested()
	}
	// The owner-side open keeps this read out of the runtime network
	// poller, whose initialization has no failure path under a low
	// RLIMIT_NOFILE — the same rule the creator-only proof open follows.
	file, err := calleropen.Open(path, os.O_RDONLY|calleropen.NonBlocking, 0)
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
	if !headerShapeValid(page) || !headerChecksumValid(page) || !headerIdentitiesValid(page) {
		return security.CreatorOnlyRequested()
	}
	policy, err := decodePolicy(page)
	if err != nil {
		return security.CreatorOnlyRequested()
	}
	return policy != policyUnprotected
}

// headerIdentitiesValid proves the nonzero database and sidecar
// identities (the Rust read_header identity rule).
func headerIdentitiesValid(page []byte) bool {
	return !allZero(page, databaseIDOff, 16) && !allZero(page, sidecarIDOff, 16)
}
