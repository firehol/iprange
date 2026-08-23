//go:build linux

// Created/secured output machine tests (Rust output_tests.rs created
// and secured arms): absent preconditions, facts, the creator-only
// proof, the hard-link refusal, and the resume/cleanup evidence paths.

package publication

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/live"
)

func TestCreateOutputAbsentRefusesExistingMain(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	if err := os.WriteFile(main, []byte("foreign"), 0o600); err != nil {
		t.Fatalf("write main: %v", err)
	}
	_, err := createOutputAbsent(main)
	nerr, ok := live.AsNamespaceError(err)
	if !ok || nerr.Kind != live.NamespaceExists {
		t.Fatalf("absent bind with existing main: %v", err)
	}
}

func TestCreateOutputSecureFactsAndResume(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	created, err := createOutput(main)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.attemptID == [16]byte{} {
		t.Fatal("created output has a zero attempt id")
	}
	if !strings.HasPrefix(created.name, ".iprange-publish-") || !strings.HasSuffix(created.name, ".tmp") {
		t.Fatalf("unexpected private name %q", created.name)
	}

	// The created facts carry the best-effort identity and the
	// destination commitment.
	createdFacts := created.facts()
	if createdFacts.BasenameEncoding != basenameEncodingKind {
		t.Fatalf("basename encoding %d", createdFacts.BasenameEncoding)
	}
	if !strings.EqualFold(string(createdFacts.Basename), created.name) {
		t.Fatalf("facts basename %q, want %q", createdFacts.Basename, created.name)
	}
	if createdFacts.Identity == nil {
		t.Fatal("created facts must include the retained identity")
	}
	if createdFacts.CreationSecurity.Kind != creationSecurityKind {
		t.Fatalf("creation security kind %d", createdFacts.CreationSecurity.Kind)
	}

	secured, failure := created.secure()
	if failure != nil {
		t.Fatalf("secure: %v", failure)
	}
	attempt, file := secured.intoParts()
	t.Cleanup(func() { file.Close() })
	identity, err := live.RegularIdentity(file, created.destination.directory().Identity())
	if err != nil {
		t.Fatalf("regular identity: %v", err)
	}
	if attempt.identity != identity {
		t.Fatal("secured attempt identity differs from the retained identity")
	}

	// The resume arms rebuild the attempt from the facts alone.
	facts := attempt.facts()
	resumed, resumedFile, err := resumeSecuredOutput(main, &facts)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	t.Cleanup(func() { resumedFile.Close() })
	if resumed.identity != attempt.identity {
		t.Fatal("resumed identity differs from the secured identity")
	}
	if resumed.name != attempt.name {
		t.Fatal("resumed private name differs from the secured name")
	}

	forCleanup, cleanupFile, present, err := resumeSecuredOutputForCleanup(main, &facts)
	if err != nil {
		t.Fatalf("resume for cleanup: %v", err)
	}
	t.Cleanup(func() {
		if cleanupFile != nil {
			cleanupFile.Close()
		}
	})
	if !present {
		t.Fatal("resume for cleanup must find the existing artifact")
	}
	if forCleanup.identity != attempt.identity {
		t.Fatal("cleanup-resumed identity differs")
	}
}

func TestResumeSecuredOutputMissingEvidence(t *testing.T) {
	dir := t.TempDir()
	attempt, file := testSecuredAttempt(t, dir, "result.v4")
	facts := attempt.facts()
	main := filepath.Join(dir, "result.v4")
	private := filepath.Join(dir, attempt.name)
	file.Close()
	if err := os.Remove(private); err != nil {
		t.Fatalf("remove private: %v", err)
	}

	_, _, err := resumeSecuredOutput(main, &facts)
	if nerr, ok := live.AsNamespaceError(err); !ok || nerr.Kind != live.NamespaceMissing {
		t.Fatalf("resume of removed artifact: %v", err)
	}

	_, _, present, err := resumeSecuredOutputForCleanup(main, &facts)
	if err != nil {
		t.Fatalf("cleanup resume of removed artifact: %v", err)
	}
	if present {
		t.Fatal("cleanup resume must prove the artifact absent")
	}
}

func TestSecureRejectsHardLink(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	created, err := createOutput(main)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := os.Link(filepath.Join(dir, created.name), filepath.Join(dir, "extra-link")); err != nil {
		t.Fatalf("hard link: %v", err)
	}
	_, failure := created.secure()
	if failure == nil {
		t.Fatal("secure must reject a hard-linked private output")
	}
	nerr, ok := live.AsNamespaceError(failure.cause)
	if !ok || nerr.Kind != live.NamespaceLinkCount || nerr.Links != 2 {
		t.Fatalf("hard-link failure class: %v (class ok %v)", failure.cause, ok)
	}
	// The created output is returned with the failure for cleanup.
	if failure.owner == nil || failure.owner.name != created.name {
		t.Fatal("failure must carry the still-owned created output")
	}
}
