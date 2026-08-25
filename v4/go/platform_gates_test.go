package iprangedb

// Platform gates for the public root-package test suite (SOW-0025
// 4-12D): the pure-Go creator-only security machine is linux-only
// (internal/security; the darwin filesec and other-OS libc ACL machines
// would need cgo, which the port forbids), so live database creation
// and every publication-producing offline operation refuse honestly
// on the other platforms. Every suite test that creates or opens a
// live pair, or whose terminal publishes a destination artifact,
// starts with the matching gate; the skip reason names the missing
// capability. On linux both gates are no-ops and nothing skips.
//
// The internal suites carry the same gates at their own package level;
// these helpers are the public-facade peers.

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/security"
)

// requireLiveCreation skips one test whose fixture creates, initializes,
// resets, or opens a live database pair (live creation needs the
// creator-only security machine plus the proven live coordination;
// internal/live.CreationSupported is the single authority).
func requireLiveCreation(t *testing.T) {
	t.Helper()
	if err := live.CreationSupported(); err != nil {
		t.Skipf("live database creation is not supported on this platform: %v", err)
	}
}

// requirePublicationSecurity skips one test whose terminal publishes a
// destination artifact (snapshot output, recovery output, replacement,
// publish-set): the publication attempt applies the creator-only
// security policy, which the pure-Go machine implements only on linux.
func requirePublicationSecurity(t *testing.T) {
	t.Helper()
	if !security.CreatorOnlySupported() {
		t.Skip("creator-only publication security is not available on this platform (pure-Go xattr machine is linux only)")
	}
}

// requireFileCreation skips one test that creates a database file
// through the non-live writer path: every file creation takes the
// exclusive lifetime-lock machine (mapping.CoordinationSupported is
// the authority), which the pure-Go port implements only on linux and
// darwin. Live-creation and publication-gated tests do not need this
// gate: their own gates already skip before any file is created.
func requireFileCreation(t *testing.T) {
	t.Helper()
	if !mapping.CoordinationSupported() {
		t.Skip("database file creation is not supported on this platform (the exclusive lifetime-lock machine is linux/darwin only)")
	}
}
