// Publication basename binding surface (Rust name_binding.rs); the
// commitment authority lives in internal/live where the GC envelope
// codec runs. The publication wrapper folds the live invalid-name
// class to the internal error the reservation and destination machines
// expect.

package publication

import (
	"github.com/firehol/iprange/v4/go/internal/live"
)

// basenameEncoding is the live platform basename encoding tag (Rust
// BasenameEncoding); the platform values live in
// name_binding_posix.go / name_binding_windows.go.
type basenameEncoding = live.BasenameEncoding

// basenameCommitment computes the exact basename commitment (Rust
// basename_commitment); the caller folds every arm to InvalidName.
func basenameCommitment(encoding basenameEncoding, bytes []byte) ([32]byte, error) {
	commitment, err := live.BasenameCommitment(encoding, bytes)
	if err != nil {
		return [32]byte{}, &nameBindingError{}
	}
	return commitment, nil
}

// nameBindingError is the internal commitment failure (Rust
// BasenameBindingError); the caller folds every arm to InvalidName.
type nameBindingError struct{}

func (*nameBindingError) Error() string { return "basename binding is invalid" }
