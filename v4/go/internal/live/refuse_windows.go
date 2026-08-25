//go:build windows

package live

import "github.com/firehol/iprange/v4/go/internal/format"

// liveRefusal is the typed refusal of the still-unimplemented Windows
// live surfaces (namespace/install machinery, SOW-0026 work package
// C4). The lock machine itself is real (lock_windows.go); the stub
// surfaces keep using this helper until they are ported.
func liveRefusal() error {
	return &format.Error{Code: format.CodeLiveCoordinationUnsupported, Detail: "live coordination is not implemented on this platform"}
}
