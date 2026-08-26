package recovery

// Worker scratch-checkpoint hook (Rust worker.rs
// start_scratch_checkpoint / add_scratch_checkpoint): the domain
// scratch owner records its created artifacts into the process-wide
// worker session when one is installed. Library processes have no
// session and the hooks stay nil, so the calls are no-ops exactly
// like the Rust CURRENT_CONTROL null arm; the worker session
// (internal/worker EnterSession) installs its control-page writer
// through the exported setter.

import "github.com/firehol/iprange/v4/go/internal/publication"

// scratchCheckpointStartFunc records the attempt facts of one scratch
// checkpoint (Rust Control::start_scratch_checkpoint).
type scratchCheckpointStartFunc func(attemptID [16]byte, directoryIdentity publication.LocalFileIdentity, creationSecurity *publication.CreationSecurity) error

// scratchCheckpointAddFunc records one created scratch artifact (Rust
// Control::add_scratch_checkpoint).
type scratchCheckpointAddFunc func(ordinal uint32, identity publication.LocalFileIdentity) error

var (
	scratchCheckpointStart scratchCheckpointStartFunc
	scratchCheckpointAdd   scratchCheckpointAddFunc
)

// SetScratchCheckpointHooks installs or clears the worker session
// scratch-checkpoint writer (Rust worker.rs CURRENT_CONTROL arm: the
// session installs the control writer on enter and restores the
// no-op on leave).
func SetScratchCheckpointHooks(start scratchCheckpointStartFunc, add scratchCheckpointAddFunc) {
	scratchCheckpointStart = start
	scratchCheckpointAdd = add
}

// startScratchCheckpoint records the attempt facts (Rust
// worker::start_scratch_checkpoint; a nil hook is the library-mode
// no-op).
func startScratchCheckpoint(attemptID [16]byte, directoryIdentity publication.LocalFileIdentity, creationSecurity *publication.CreationSecurity) error {
	if scratchCheckpointStart == nil {
		return nil
	}
	return scratchCheckpointStart(attemptID, directoryIdentity, creationSecurity)
}

// addScratchCheckpoint records one created artifact (Rust
// worker::add_scratch_checkpoint; a nil hook is the library-mode
// no-op).
func addScratchCheckpoint(ordinal uint32, identity publication.LocalFileIdentity) error {
	if scratchCheckpointAdd == nil {
		return nil
	}
	return scratchCheckpointAdd(ordinal, identity)
}
