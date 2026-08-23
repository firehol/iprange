// Shared private-creation result types (Rust
// live_namespace::CreatedPrivate + PrivateCreationFailure). The types
// are platform-neutral: every platform implements createPrivate with
// the same contract, and the Windows refusal carries the same fact
// shape.

package live

import "os"

// createdPrivate is the created artifact of one private creation
// (Rust live_namespace::CreatedPrivate).
type createdPrivate struct {
	file     *os.File
	identity FileIdentity
}

// privateCreationFailure is the exact failure record of one private
// creation (Rust live_namespace::PrivateCreationFailure): the cause,
// the cleanup outcome of the created artifact when creation proceeded
// past the identity capture, and the created identity when it is known.
// The caller folds these facts into the attempt result exactly like
// Rust; errors are never flattened before that fold.
type privateCreationFailure struct {
	cause    error
	cleanup  cleanupOutcome
	identity *FileIdentity
}
