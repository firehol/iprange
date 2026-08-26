//go:build windows

package recovery

import "github.com/firehol/iprange/v4/go/internal/live"

// scratchCreationSecurityKind is the platform creator-only security
// kind recorded in scratch headers (Rust
// publication::namespace::CREATION_SECURITY_KIND windows arm = 2).
func scratchCreationSecurityKind() uint16 { return 2 }

// scratchBasenameEncoding is the platform basename encoding of the
// fixed scratch names (Rust namespace::BASENAME_ENCODING_KIND
// windows arm = 2).
func scratchBasenameEncoding() live.BasenameEncoding { return 2 }
