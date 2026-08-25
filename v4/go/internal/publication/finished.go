// FinishedOutput is the terminal immutable output handed to the
// publication owner by the writer core (Rust immutable_output::Finished
// fields used by output.rs): the completed file, its read-only
// mapping, and the decoded meta page. The output machine proves
// custody and digests the mapping while holding the artifact lifetime
// lock; it never re-reads the file through the path.

package publication

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

// FinishedOutput is one finished immutable output (Rust
// immutable_output::Finished). The mapping is the writer's sealed
// read-only view of the complete file; meta is the selected committed
// meta page of that mapping.
type FinishedOutput struct {
	File    *os.File
	Mapping *mapping.Mapping
	Meta    format.Meta
}
