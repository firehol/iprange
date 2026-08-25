//go:build windows

package publication

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

// FinishedOutput is one finished immutable output (Rust
// immutable_output::Finished) with the same field surface as the POSIX
// owner. The composition refuses at create on Windows (Rust
// namespace/windows.rs is a tracked SOW-0026 surface), so no finished output
// is ever constructed there in milestone 1.
type FinishedOutput struct {
	File    *os.File
	Mapping *mapping.Mapping
	Meta    format.Meta
}
