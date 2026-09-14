//go:build !(linux || darwin || freebsd || netbsd || windows)

package reader

import "testing"

// crossCheckFileIdentityProbe has no mapping probe on this platform:
// internal/mapping builds StatIdentity only for the GOOS listed in the
// counterpart file, so there is nothing to compare the reader's reported
// identity against. Skipping the cross-check subtest keeps the remaining
// FileIdentity assertions running here instead of dropping the whole test.
// Removal criterion: add the platform's StatIdentity variant in
// internal/mapping and list its GOOS in both constraints.
func crossCheckFileIdentityProbe(t *testing.T, _ string, _, _ uint64) {
	t.Helper()
	t.Skip("internal/mapping has no StatIdentity probe for this GOOS")
}
