//go:build windows

package mapping

import "github.com/firehol/iprange/v4/go/internal/format"

// StatIdentity satisfies the common surface; the Windows mapping
// owner refuses identity probes (the reader refuses Windows opens at
// the mapping step, so the cross-check is unreachable there).
func StatIdentity(string) (uint64, uint64, error) {
	return 0, 0, &format.Error{Code: format.CodeOSUnsupported, Detail: "windows identity probe not implemented"}
}
