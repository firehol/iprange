//go:build !windows

package recovery

import "github.com/firehol/iprange/v4/go/internal/live"

// scratchCreationSecurityKind is the platform creator-only security
// kind recorded in scratch headers (Rust
// publication::namespace::CREATION_SECURITY_KIND unix arm = 1).
func scratchCreationSecurityKind() uint16 { return 1 }

// scratchBasenameEncoding is the platform basename encoding of the
// fixed scratch names (Rust namespace::BASENAME_ENCODING_KIND
// unix arm = 1).
func scratchBasenameEncoding() live.BasenameEncoding { return 1 }
