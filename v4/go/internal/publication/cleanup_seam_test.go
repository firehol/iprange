//go:build linux

// DiscardSecuredAttempt seam tests (Rust worker/cleanup.rs run_worker
// arms :19-25 over the real output fixture): a present secured attempt
// is discarded with the exact facts and no artifact, a proven-absent
// attempt records confirmed_absent, and a resume failure (the
// destination moved away) folds the fixed Problem::output class into
// the failed-attempt artifact.

package publication

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

func TestDiscardSecuredAttemptPresent(t *testing.T) {
	directory := t.TempDir()
	main := "result.v4"
	attempt, file := testSecuredAttempt(t, directory, main)
	facts := attempt.facts()
	got := DiscardSecuredAttempt(filepath.Join(directory, main), &facts)
	if got.Artifact != nil {
		t.Fatalf("artifact = %+v, want a clean discard", got.Artifact)
	}
	if got.Housekeeping != HousekeepingNone || len(got.VisibleHousekeeping) != 0 {
		t.Fatalf("housekeeping = %+v / %+v, want none", got.Housekeeping, got.VisibleHousekeeping)
	}
	if got.Output.PublicationAttemptID != facts.PublicationAttemptID ||
		got.Output.DirectoryIdentity != facts.DirectoryIdentity ||
		got.Output.BasenameEncoding != facts.BasenameEncoding ||
		string(got.Output.Basename) != string(facts.Basename) ||
		got.Output.Identity != facts.Identity ||
		got.Output.IdentityPresent != facts.IdentityPresent ||
		got.Output.CreationSecurity != facts.CreationSecurity {
		t.Fatalf("discarded output = %+v, want %+v", got.Output, facts)
	}
	if _, err := os.Lstat(filepath.Join(directory, attempt.nameOf())); !os.IsNotExist(err) {
		t.Fatalf("private attempt still exists after the discard")
	}
	file.Close()
}

func TestDiscardSecuredAttemptAbsent(t *testing.T) {
	directory := t.TempDir()
	main := "result.v4"
	attempt, file := testSecuredAttempt(t, directory, main)
	facts := attempt.facts()
	file.Close()
	if err := os.Remove(filepath.Join(directory, attempt.nameOf())); err != nil {
		t.Fatalf("fixture remove attempt: %v", err)
	}
	got := DiscardSecuredAttempt(filepath.Join(directory, main), &facts)
	if got.Artifact != nil {
		t.Fatalf("artifact = %+v, want nothing for a proven-absent attempt", got.Artifact)
	}
	if got.Output.PublicationAttemptID != facts.PublicationAttemptID {
		t.Fatalf("discarded output = %+v, want %+v", got.Output, facts)
	}
}

func TestDiscardSecuredAttemptResumeFailure(t *testing.T) {
	directory := t.TempDir()
	main := "result.v4"
	attempt, file := testSecuredAttempt(t, directory, main)
	facts := attempt.facts()
	file.Close()
	relocated := directory + "-moved"
	if err := os.Rename(directory, relocated); err != nil {
		t.Fatalf("fixture relocate destination: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(relocated) })
	got := DiscardSecuredAttempt(filepath.Join(directory, main), &facts)
	if got.Artifact == nil {
		t.Fatal("artifact is nil, want the failed-attempt artifact")
	}
	if got.Artifact.Kind != ArtifactPrivateOutput || string(got.Artifact.Basename) != string(facts.Basename) {
		t.Fatalf("artifact = %+v, want the private-output attempt artifact", got.Artifact)
	}
	var fe *format.Error
	if !errors.As(got.Artifact.Error, &fe) || fe.Code != format.CodeNameNotFound {
		t.Fatalf("artifact error = %v, want the missing-destination NameNotFound class", got.Artifact.Error)
	}
}
