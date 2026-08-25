// Exported discard seam for the isolated worker cleanup boundary (Rust
// worker/cleanup.rs run_worker:14-27). The worker driver must run the
// three-arm discard machine over wire-decoded attempt facts without
// reaching into the package-private machine; this seam is the single
// exported entry that runs exactly those arms (Rust lines 19-25):
// resume_secured_output_for_cleanup succeeds with the attempt ->
// discard_attempt, proves the artifact absent -> confirmed_absent, or
// fails -> failed_attempt with the Problem::output fold. The machine is
// total in Rust (every arm returns the discard facts; the failure arm
// folds the problem into the artifact), so the seam is total too: there
// is no error surface for the caller to fabricate.

package publication

// EarlyDiscardFacts is the exported fact value of one pre-publication
// discard (Rust publication::cleanup::EarlyDiscard): the discarded
// private output attempt, the optional unresolved artifact, and the
// housekeeping evidence. The worker boundary converts these facts
// thinly to its wire EarlyDiscard; publication itself never imports the
// worker package.
type EarlyDiscardFacts struct {
	Output              PrivateOutputAttempt
	Artifact            *CleanupArtifact
	Housekeeping        Housekeeping
	VisibleHousekeeping []HousekeepingArtifact
}

// DiscardSecuredAttempt runs the worker cleanup discard machine over
// one wire-decoded secured output attempt (Rust worker/cleanup.rs
// run_worker:19-25 over publication/cleanup.rs discard_attempt:93 /
// confirmed_absent:130 / failed_attempt:117 and output.rs
// resume_secured_output_for_cleanup:280). A present attempt is removed
// behind the exact identity proof; a proven-absent attempt records no
// artifact; a resume failure folds the fixed Problem::output class into
// the artifact (Go peer: outputProblem).
func DiscardSecuredAttempt(destinationPath string, facts *PrivateOutputAttempt) EarlyDiscardFacts {
	attempt, file, present, err := resumeSecuredOutputForCleanup(destinationPath, facts)
	if err != nil {
		return earlyDiscardFactsOf(failedAttempt(*facts, outputProblem(err)))
	}
	if !present {
		return earlyDiscardFactsOf(confirmedAbsent(*facts))
	}
	return earlyDiscardFactsOf(discardAttempt(&attempt, file))
}

func earlyDiscardFactsOf(discarded earlyDiscard) EarlyDiscardFacts {
	return EarlyDiscardFacts{
		Output:              discarded.output,
		Artifact:            discarded.artifact,
		Housekeeping:        discarded.housekeeping,
		VisibleHousekeeping: discarded.visibleHousekeeping,
	}
}

// FailedAttemptFacts records one attempt whose discard session failed
// before any namespace work (Rust publication/cleanup.rs
// failed_attempt: the problem becomes the exact artifact with no
// further removal). The worker client folds its composed discard
// through this exported entry; the worker boundary then converts the
// facts to its wire EarlyDiscard.
func FailedAttemptFacts(facts *PrivateOutputAttempt, problem error) EarlyDiscardFacts {
	return earlyDiscardFactsOf(failedAttempt(*facts, problem))
}
