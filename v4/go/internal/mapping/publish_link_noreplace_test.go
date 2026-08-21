package mapping

// FreeBSD link_noreplace machine tests (Rust publication namespace
// tests: freebsd_noreplace_transition_finishes_only_the_exact_pair,
// freebsd_noreplace_transition_rejects_extra_or_foreign_links,
// no_replace_never_overwrites_and_exact_unlink_checks_identity). The
// machine is compiled on the build host so the exact linkat/state/
// unlink sequence runs here; the FreeBSD build-tagged entry point is
// the same machine with the production wiring.

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// linkAttempt creates one regular attempt file and returns its path and
// identity (the writer's creation-time capture).
func linkAttempt(t *testing.T, dir, name string) (path string, device, inode uint64) {
	t.Helper()
	path = filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("output"), 0o644); err != nil {
		t.Fatal(err)
	}
	device, inode, err := StatIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, device, inode
}

// linkProbe reports whether path exists with exactly nlink links.
func linkProbe(t *testing.T, path string) (exists bool, nlink uint64) {
	t.Helper()
	var st unix.Stat_t
	err := unix.Lstat(path, &st)
	if err != nil {
		if os.IsNotExist(err) {
			return false, 0
		}
		t.Fatal(err)
	}
	return true, uint64(st.Nlink)
}

func TestLinkNoReplaceHappyPath(t *testing.T) {
	dir := t.TempDir()
	source, device, inode := linkAttempt(t, dir, "attempt")
	destination := filepath.Join(dir, "out.iprdb")
	if err := linkNoReplace(dir, source, destination, device, inode); err != nil {
		t.Fatal(err)
	}
	if exists, nlink := linkProbe(t, destination); !exists || nlink != 1 {
		t.Fatalf("destination exists=%v nlink=%d, want true/1", exists, nlink)
	}
	if exists, _ := linkProbe(t, source); exists {
		t.Fatal("source alias survived the transition")
	}
	got, err := os.ReadFile(destination)
	if err != nil || string(got) != "output" {
		t.Fatalf("destination content = %q err=%v, want output", got, err)
	}
}

func TestLinkNoReplaceForeignDestinationRefused(t *testing.T) {
	dir := t.TempDir()
	source, device, inode := linkAttempt(t, dir, "attempt")
	destination := filepath.Join(dir, "out.iprdb")
	if err := os.WriteFile(destination, []byte("foreign"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := linkNoReplace(dir, source, destination, device, inode)
	e, ok := err.(*format.Error)
	if !ok || e.Code != format.CodeNameExists || e.Detail != "publication name already exists" {
		t.Fatalf("refusal = %v, want CodeNameExists", err)
	}
	got, readErr := os.ReadFile(destination)
	if readErr != nil || string(got) != "foreign" {
		t.Fatalf("foreign destination changed: %q err=%v", got, readErr)
	}
	if exists, _ := linkProbe(t, source); !exists {
		t.Fatal("source disappeared after refusal")
	}
}

func TestLinkNoReplaceMissingSource(t *testing.T) {
	dir := t.TempDir()
	err := linkNoReplace(dir, filepath.Join(dir, "missing"), filepath.Join(dir, "out.iprdb"), 1, 2)
	e, ok := err.(*format.Error)
	if !ok || e.Code != format.CodeNameNotFound {
		t.Fatalf("error = %v, want CodeNameNotFound", err)
	}
}

func TestLinkNoReplaceSourceExtraLinkRefused(t *testing.T) {
	dir := t.TempDir()
	source, device, inode := linkAttempt(t, dir, "attempt")
	if err := os.Link(source, filepath.Join(dir, "third")); err != nil { // nlink 2
		t.Fatal(err)
	}
	err := linkNoReplace(dir, source, filepath.Join(dir, "out.iprdb"), device, inode)
	e, ok := err.(*format.Error)
	if !ok || e.Code != format.CodeConflict || e.Detail != "publication inode link count changed" {
		t.Fatalf("error = %v, want link-count conflict", err)
	}
}

func TestLinkNoReplaceSourceNotRegularRefused(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "attemptdir")
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}
	err := linkNoReplace(dir, source, filepath.Join(dir, "out.iprdb"), 1, 2)
	e, ok := err.(*format.Error)
	if !ok || e.Code != format.CodeConflict || e.Detail != "publication name is not a regular file" {
		t.Fatalf("error = %v, want not-regular conflict", err)
	}
}

func TestLinkNoReplaceWrongIdentityRefused(t *testing.T) {
	dir := t.TempDir()
	source, _, _ := linkAttempt(t, dir, "attempt")
	err := linkNoReplace(dir, source, filepath.Join(dir, "out.iprdb"), 1, 2)
	e, ok := err.(*format.Error)
	if !ok || e.Code != format.CodeConflict || e.Detail != "publication inode identity changed" {
		t.Fatalf("error = %v, want identity conflict", err)
	}
}

// TestFinishNoReplaceTransitionRecovery drives the Linked and Complete
// recovery states directly, exactly like the Rust namespace tests:
// after a crash between the link and the alias unlink both names name
// the same inode with two links; the transition finishes them. A second
// call on the finished state proves the destination alone (Complete).
func TestFinishNoReplaceTransitionRecovery(t *testing.T) {
	dir := t.TempDir()
	source, device, inode := linkAttempt(t, dir, "attempt")
	destination := filepath.Join(dir, "out.iprdb")
	if err := os.Link(source, destination); err != nil { // crash state: Linked
		t.Fatal(err)
	}
	if err := finishNoReplaceTransition(dir, source, destination, device, inode); err != nil {
		t.Fatal(err)
	}
	if exists, _ := linkProbe(t, source); exists {
		t.Fatal("source alias survived the recovery")
	}
	if exists, nlink := linkProbe(t, destination); !exists || nlink != 1 {
		t.Fatalf("destination exists=%v nlink=%d, want true/1", exists, nlink)
	}
	// The finished state is idempotent: Complete proves only.
	if err := finishNoReplaceTransition(dir, source, destination, device, inode); err != nil {
		t.Fatal("second transition on the complete state:", err)
	}
}

func TestFinishNoReplaceTransitionRejectsExtraLinks(t *testing.T) {
	dir := t.TempDir()
	source, device, inode := linkAttempt(t, dir, "attempt")
	destination := filepath.Join(dir, "out.iprdb")
	if err := os.Link(source, destination); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(source, filepath.Join(dir, "third")); err != nil { // nlink 3
		t.Fatal(err)
	}
	err := finishNoReplaceTransition(dir, source, destination, device, inode)
	e, ok := err.(*format.Error)
	if !ok || e.Code != format.CodeConflict {
		t.Fatalf("error = %v, want link-count conflict", err)
	}
	for _, name := range []string{source, destination, filepath.Join(dir, "third")} {
		if exists, _ := linkProbe(t, name); !exists {
			t.Fatalf("%s disappeared after refusal", name)
		}
	}
}

func TestFinishNoReplaceTransitionRejectsForeignPair(t *testing.T) {
	dir := t.TempDir()
	source, device, inode := linkAttempt(t, dir, "attempt")
	destination := filepath.Join(dir, "out.iprdb")
	if err := os.WriteFile(destination, []byte("foreign"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := finishNoReplaceTransition(dir, source, destination, device, inode)
	e, ok := err.(*format.Error)
	if !ok || e.Code != format.CodeConflict {
		t.Fatalf("error = %v, want identity conflict", err)
	}
	if exists, _ := linkProbe(t, source); !exists {
		t.Fatal("source disappeared after refusal")
	}
}

// TestLinkNoReplaceEntryPathRefusesCrashState documents the Rust
// boundary: the entry path proves the source has exactly one link
// (require_source), so a crash residue must be recovered through
// finishNoReplaceTransition, never by re-entering the operation.
func TestLinkNoReplaceEntryPathRefusesCrashState(t *testing.T) {
	dir := t.TempDir()
	source, device, inode := linkAttempt(t, dir, "attempt")
	destination := filepath.Join(dir, "out.iprdb")
	if err := os.Link(source, destination); err != nil { // nlink 2
		t.Fatal(err)
	}
	err := linkNoReplace(dir, source, destination, device, inode)
	e, ok := err.(*format.Error)
	if !ok || e.Code != format.CodeConflict {
		t.Fatalf("error = %v, want the require_source link-count conflict", err)
	}
}
