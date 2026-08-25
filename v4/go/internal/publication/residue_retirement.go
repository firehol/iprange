// Platform-specific retirement of one retained coordination inode
// (Rust publication/residue/retirement.rs): the posix arm unlinks the
// exact inode and proves the retained link count; the windows arm runs
// the authenticated GC transition. The retry arm re-proves the
// retirement after the directory synchronization.

package publication

// retirementOutcome is the retirement proof result (Rust
// retirement::Outcome).
type retirementOutcome struct {
	cause        error
	housekeeping Housekeeping
	visible      []HousekeepingArtifact
}
