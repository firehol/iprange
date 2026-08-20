// Publication staging tests (Rust publication output/workflow/cleanup
// tests): attempt naming, the fail-if-exists refusal, atomic exchange
// and plain replacement, custody failures, discard cleanup states, and
// the Rust-verbatim error surface.

package writer_test

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	iprangedb "github.com/firehol/iprange/v4/go"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

var attemptNamePattern = regexp.MustCompile(`^\.iprange-publish-[0-9a-f]{32}\.tmp$`)

// stagedBuilder creates the attempt and builds one finished direct
// output into it.
func stagedBuilder(t *testing.T, destination string, policy writer.PublicationPolicy) (*writer.OutputAttempt, *writer.OutputBuilder) {
	t.Helper()
	attempt, err := writer.CreateAttempt(destination, policy)
	if err != nil {
		t.Fatalf("CreateAttempt: %v", err)
	}
	b, err := writer.NewOutputBuilder(attempt.AttemptPath(), directSpec(format.AddressFamilyIPv4), generousBudget(), nil)
	if err != nil {
		t.Fatalf("NewOutputBuilder: %v", err)
	}
	if err := b.PushDirectV4(0, 42, 1); err != nil {
		t.Fatalf("PushDirectV4: %v", err)
	}
	if err := b.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	return attempt, b
}

func closeBuilder(t *testing.T, b *writer.OutputBuilder) {
	t.Helper()
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func errorCode(t *testing.T, err error) format.ErrorCode {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	for err != nil {
		if fe, ok := err.(*format.Error); ok {
			return fe.Code
		}
		if u, ok := err.(interface{ Unwrap() error }); ok {
			err = u.Unwrap()
			continue
		}
		break
	}
	t.Fatalf("error %v is not a format.Error", err)
	return 0
}

func errorDetail(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	for err != nil {
		if fe, ok := err.(*format.Error); ok {
			return fe.Detail
		}
		if u, ok := err.(interface{ Unwrap() error }); ok {
			err = u.Unwrap()
			continue
		}
		break
	}
	t.Fatalf("error %v is not a format.Error", err)
	return ""
}

func TestCreateAttemptNamesAndValidates(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "output.iprdb")
	attempt, err := writer.CreateAttempt(destination, writer.PolicyFailIfExists)
	if err != nil {
		t.Fatalf("CreateAttempt: %v", err)
	}
	if !attemptNamePattern.MatchString(attempt.Name()) {
		t.Fatalf("attempt name %q does not match the private naming contract", attempt.Name())
	}
	if attempt.AttemptPath() != filepath.Join(dir, attempt.Name()) {
		t.Fatalf("attempt path %q not inside the destination directory", attempt.AttemptPath())
	}
	if attempt.Destination() != destination {
		t.Fatalf("destination = %q, want %q", attempt.Destination(), destination)
	}
	if attempt.AttemptID() == [16]byte{} {
		t.Fatal("attempt id is zero")
	}
	device, inode := attempt.DirectoryIdentity()
	if device == 0 || inode == 0 {
		t.Fatalf("directory identity (%d,%d) is not a real identity", device, inode)
	}
	if attempt.AttemptPath() == destination {
		t.Fatal("attempt path collides with the destination")
	}
}

func TestCreateAttemptInvalidDestination(t *testing.T) {
	for _, destination := range []string{"", "/", "..", "."} {
		_, err := writer.CreateAttempt(destination, writer.PolicyReplaceExisting)
		if err == nil {
			t.Fatalf("CreateAttempt(%q) succeeded", destination)
		}
		if code := errorCode(t, err); code != format.CodeNameInvalid {
			t.Fatalf("CreateAttempt(%q) code = %d, want NameInvalid", destination, code)
		}
		if detail := errorDetail(t, err); detail != "invalid destination name" {
			t.Fatalf("CreateAttempt(%q) detail = %q, want the Rust detail", destination, detail)
		}
	}
	dir := t.TempDir()
	for _, name := range []string{".iprange-reserved", "output.readers"} {
		destination := filepath.Join(dir, name)
		_, err := writer.CreateAttempt(destination, writer.PolicyReplaceExisting)
		if err == nil {
			t.Fatalf("CreateAttempt(%q) with the reserved name succeeded", destination)
		}
		if code := errorCode(t, err); code != format.CodeNameInvalid {
			t.Fatalf("CreateAttempt(%q) code = %d, want NameInvalid", destination, code)
		}
		if detail := errorDetail(t, err); detail != "invalid destination name" {
			t.Fatalf("CreateAttempt(%q) detail = %q, want the Rust detail", destination, detail)
		}
	}
}

func TestCreateAttemptFailIfExistsRefusesExisting(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "output.iprdb")
	if err := os.WriteFile(destination, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := writer.CreateAttempt(destination, writer.PolicyFailIfExists)
	if err == nil {
		t.Fatal("CreateAttempt over an existing destination succeeded")
	}
	if code := errorCode(t, err); code != format.CodeNameExists {
		t.Fatalf("code = %d, want NameExists", code)
	}
	if detail := errorDetail(t, err); detail != "publication name already exists" {
		t.Fatalf("detail = %q, want the Rust detail", detail)
	}
	got, readErr := os.ReadFile(destination)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "old" {
		t.Fatalf("destination changed to %q, want untouched %q", got, "old")
	}
}

func TestCreateAttemptMissingParent(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "missing", "output.iprdb")
	_, err := writer.CreateAttempt(destination, writer.PolicyFailIfExists)
	if err == nil {
		t.Fatal("CreateAttempt with a missing parent succeeded")
	}
	if code := errorCode(t, err); code != format.CodeNameNotFound {
		t.Fatalf("code = %d, want NameNotFound", code)
	}
	if detail := errorDetail(t, err); detail != "publication name is missing" {
		t.Fatalf("detail = %q, want the Rust detail", detail)
	}
}

func TestPublishFailIfExistsPublishes(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "output.iprdb")
	attempt, b := stagedBuilder(t, destination, writer.PolicyFailIfExists)
	result, err := writer.Publish(attempt, b, writer.PolicyFailIfExists)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if result.Status != writer.PublicationPublished {
		t.Fatalf("status = %v, want Published", result.Status)
	}
	if result.DestinationContent != writer.DestinationContentDesired {
		t.Fatalf("content = %v, want Desired", result.DestinationContent)
	}
	if result.Cleanup != writer.CleanupStateClean {
		t.Fatalf("cleanup = %v, want Clean", result.Cleanup)
	}
	if result.Cause != nil {
		t.Fatalf("unexpected cause %v", result.Cause)
	}
	closeBuilder(t, b)
	if _, err := os.Lstat(attempt.AttemptPath()); !os.IsNotExist(err) {
		t.Fatalf("attempt file still exists after publish: %v", err)
	}
	r, err := iprangedb.OpenImmutable(destination)
	if err != nil {
		t.Fatalf("published destination does not reopen: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPublishFailIfExistsRefused(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "output.iprdb")
	attempt, b := stagedBuilder(t, destination, writer.PolicyFailIfExists)
	// The destination appears between CreateAttempt and Publish, so the
	// refusal surfaces from the atomic no-replace rename.
	if err := os.WriteFile(destination, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := writer.Publish(attempt, b, writer.PolicyFailIfExists)
	if err != nil {
		t.Fatalf("Publish returned an error for a refused rename: %v", err)
	}
	if result.Status != writer.PublicationNotPublished {
		t.Fatalf("status = %v, want NotPublished", result.Status)
	}
	if result.Cause == nil {
		t.Fatal("refused publish carries no cause")
	}
	if code := errorCode(t, result.Cause); code != format.CodeNameExists {
		t.Fatalf("cause code = %d, want NameExists", code)
	}
	if detail := errorDetail(t, result.Cause); detail != "publication name already exists" {
		t.Fatalf("cause detail = %q, want the Rust detail", detail)
	}
	if result.Cleanup != writer.CleanupStateClean {
		t.Fatalf("cleanup = %v, want Clean", result.Cleanup)
	}
	closeBuilder(t, b)
	got, readErr := os.ReadFile(destination)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "old" {
		t.Fatalf("destination changed to %q, want untouched %q", got, "old")
	}
	if _, err := os.Lstat(attempt.AttemptPath()); !os.IsNotExist(err) {
		t.Fatalf("attempt file still exists after refusal: %v", err)
	}
}

func TestPublishExchangeReplaces(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "output.iprdb")
	if err := os.WriteFile(destination, []byte("previous content"), 0o644); err != nil {
		t.Fatal(err)
	}
	attempt, b := stagedBuilder(t, destination, writer.PolicyReplaceExisting)
	result, err := writer.Publish(attempt, b, writer.PolicyReplaceExisting)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if result.Status != writer.PublicationPublished {
		t.Fatalf("status = %v, want Published", result.Status)
	}
	if result.DestinationContent != writer.DestinationContentDesired {
		t.Fatalf("content = %v, want Desired", result.DestinationContent)
	}
	closeBuilder(t, b)
	r, err := iprangedb.OpenImmutable(destination)
	if err != nil {
		t.Fatalf("published destination does not reopen: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPublishExchangeMissingDestination(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "output.iprdb")
	attempt, b := stagedBuilder(t, destination, writer.PolicyReplaceExisting)
	result, err := writer.Publish(attempt, b, writer.PolicyReplaceExisting)
	if err != nil {
		t.Fatalf("Publish returned an error for a missing destination: %v", err)
	}
	if result.Status != writer.PublicationNotPublished {
		t.Fatalf("status = %v, want NotPublished", result.Status)
	}
	if result.DestinationContent != writer.DestinationContentAbsent {
		t.Fatalf("content = %v, want Absent", result.DestinationContent)
	}
	if code := errorCode(t, result.Cause); code != format.CodeNameNotFound {
		t.Fatalf("cause code = %d, want NameNotFound", code)
	}
	if detail := errorDetail(t, result.Cause); detail != "publication name is missing" {
		t.Fatalf("cause detail = %q, want the Rust detail", detail)
	}
	if result.Cleanup != writer.CleanupStateClean {
		t.Fatalf("cleanup = %v, want Clean", result.Cleanup)
	}
	closeBuilder(t, b)
	if _, err := os.Lstat(attempt.AttemptPath()); !os.IsNotExist(err) {
		t.Fatalf("attempt file still exists after failed exchange: %v", err)
	}
}

func TestPublishPlainReplaces(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "output.iprdb")
	if err := os.WriteFile(destination, []byte("previous content"), 0o644); err != nil {
		t.Fatal(err)
	}
	attempt, b := stagedBuilder(t, destination, writer.PolicyReplaceExistingNoRollback)
	result, err := writer.Publish(attempt, b, writer.PolicyReplaceExistingNoRollback)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if result.Status != writer.PublicationPublished {
		t.Fatalf("status = %v, want Published", result.Status)
	}
	closeBuilder(t, b)
	r, err := iprangedb.OpenImmutable(destination)
	if err != nil {
		t.Fatalf("published destination does not reopen: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPublishSameIdentityRefused(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "output.iprdb")
	attempt, b := stagedBuilder(t, destination, writer.PolicyReplaceExisting)
	// A hard link at the destination makes the destination identical to
	// the attempt file (Rust replacement SameIdentity).
	if err := os.Link(attempt.AttemptPath(), destination); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	_, err := writer.Publish(attempt, b, writer.PolicyReplaceExisting)
	if err == nil {
		t.Fatal("same-identity replacement succeeded")
	}
	failure, ok := err.(*writer.PublicationPreparationFailure)
	if !ok {
		t.Fatalf("error %T is not a PublicationPreparationFailure", err)
	}
	if code := errorCode(t, failure); code != format.CodeConflict {
		t.Fatalf("cause code = %d, want Conflict", code)
	}
	if detail := errorDetail(t, failure); detail != "replacement source and destination identities match" {
		t.Fatalf("cause detail = %q, want the Rust detail", detail)
	}
	if failure.Cleanup != writer.CleanupStateClean {
		t.Fatalf("cleanup = %v, want Clean", failure.Cleanup)
	}
	closeBuilder(t, b)
}

func TestPublishMissingAttempt(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "output.iprdb")
	attempt, b := stagedBuilder(t, destination, writer.PolicyFailIfExists)
	if err := os.Remove(attempt.AttemptPath()); err != nil {
		t.Fatal(err)
	}
	_, err := writer.Publish(attempt, b, writer.PolicyFailIfExists)
	if err == nil {
		t.Fatal("publish of a missing attempt succeeded")
	}
	failure, ok := err.(*writer.PublicationPreparationFailure)
	if !ok {
		t.Fatalf("error %T is not a PublicationPreparationFailure", err)
	}
	if code := errorCode(t, failure); code != format.CodeNameNotFound {
		t.Fatalf("cause code = %d, want NameNotFound", code)
	}
	if detail := errorDetail(t, failure); detail != "publication name is missing" {
		t.Fatalf("cause detail = %q, want the Rust detail", detail)
	}
	if failure.Cleanup != writer.CleanupStateClean {
		t.Fatalf("cleanup = %v, want Clean for an already-absent attempt", failure.Cleanup)
	}
	closeBuilder(t, b)
}

func TestPublishUnfinishedBuilderRefused(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "output.iprdb")
	attempt, err := writer.CreateAttempt(destination, writer.PolicyFailIfExists)
	if err != nil {
		t.Fatalf("CreateAttempt: %v", err)
	}
	b, err := writer.NewOutputBuilder(attempt.AttemptPath(), directSpec(format.AddressFamilyIPv4), generousBudget(), nil)
	if err != nil {
		t.Fatalf("NewOutputBuilder: %v", err)
	}
	_, err = writer.Publish(attempt, b, writer.PolicyFailIfExists)
	if err == nil {
		t.Fatal("publish of an unfinished builder succeeded")
	}
	failure, ok := err.(*writer.PublicationPreparationFailure)
	if !ok {
		t.Fatalf("error %T is not a PublicationPreparationFailure", err)
	}
	if code := errorCode(t, failure); code != format.CodeWrongState {
		t.Fatalf("cause code = %d, want WrongState", code)
	}
	if failure.Cleanup != writer.CleanupStateClean {
		t.Fatalf("cleanup = %v, want Clean", failure.Cleanup)
	}
	closeBuilder(t, b)
}

func TestDiscardStates(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "output.iprdb")
	attempt, err := writer.CreateAttempt(destination, writer.PolicyFailIfExists)
	if err != nil {
		t.Fatal(err)
	}
	// Absent attempt: Clean.
	if state := attempt.Discard(); state != writer.CleanupStateClean {
		t.Fatalf("discard of an absent attempt = %v, want Clean", state)
	}
	// Present attempt: Clean after removal.
	if err := os.WriteFile(attempt.AttemptPath(), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if state := attempt.Discard(); state != writer.CleanupStateClean {
		t.Fatalf("discard of a present attempt = %v, want Clean", state)
	}
	if _, err := os.Lstat(attempt.AttemptPath()); !os.IsNotExist(err) {
		t.Fatalf("attempt still exists after discard: %v", err)
	}
	// Unremovable attempt (read-only directory): ResiduePossible.
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission-based residue test needs an unprivileged POSIX user")
	}
	if err := os.WriteFile(attempt.AttemptPath(), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	state := attempt.Discard()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if state != writer.CleanupStateResiduePossible {
		t.Fatalf("discard over a read-only directory = %v, want ResiduePossible", state)
	}
	if err := os.Remove(attempt.AttemptPath()); err != nil {
		t.Fatal(err)
	}
}

func TestPublishFailureCarriesResidue(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission-based residue test needs an unprivileged POSIX user")
	}
	dir := t.TempDir()
	destination := filepath.Join(dir, "output.iprdb")
	attempt, b := stagedBuilder(t, destination, writer.PolicyFailIfExists)
	// A blocked rename (existing destination) with an unremovable
	// attempt dir: the refused publish discards, and the unprovable
	// removal surfaces as ResiduePossible.
	if err := os.WriteFile(destination, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	result, err := writer.Publish(attempt, b, writer.PolicyFailIfExists)
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if result.Status != writer.PublicationNotPublished {
		t.Fatalf("status = %v, want NotPublished", result.Status)
	}
	if result.Cleanup != writer.CleanupStateResiduePossible {
		t.Fatalf("cleanup = %v, want ResiduePossible", result.Cleanup)
	}
	// The residue contract: the attempt file provably remains, so the
	// caller can retry its removal.
	if _, err := os.Lstat(attempt.AttemptPath()); os.IsNotExist(err) {
		t.Fatal("residue attempt file is missing")
	}
	if err := os.Remove(attempt.AttemptPath()); err != nil {
		t.Fatal(err)
	}
	closeBuilder(t, b)
}

func TestFailureErrorText(t *testing.T) {
	failure := &writer.PublicationPreparationFailure{
		Cause:   &format.Error{Code: format.CodeNameExists, Detail: "publication name already exists"},
		Cleanup: writer.CleanupStateClean,
	}
	text := failure.Error()
	if !strings.Contains(text, "publication name already exists") {
		t.Fatalf("error text %q lacks the cause detail", text)
	}
	if failure.Unwrap() == nil {
		t.Fatal("Unwrap returned nil")
	}
}

func TestPublishReplacementDestinationNotRegular(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "output.iprdb")
	if err := os.Mkdir(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	attempt, b := stagedBuilder(t, destination, writer.PolicyReplaceExisting)
	_, err := writer.Publish(attempt, b, writer.PolicyReplaceExisting)
	if err == nil {
		t.Fatal("replacement over a destination directory succeeded")
	}
	failure, ok := err.(*writer.PublicationPreparationFailure)
	if !ok {
		t.Fatalf("error %T is not a PublicationPreparationFailure", err)
	}
	if code := errorCode(t, failure); code != format.CodeConflict {
		t.Fatalf("cause code = %d, want Conflict", code)
	}
	if detail := errorDetail(t, failure); detail != "publication name is not a regular file" {
		t.Fatalf("cause detail = %q, want the Rust detail", detail)
	}
	if failure.Cleanup != writer.CleanupStateClean {
		t.Fatalf("cleanup = %v, want Clean", failure.Cleanup)
	}
	fi, err := os.Lstat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.IsDir() {
		t.Fatal("destination directory was replaced")
	}
	closeBuilder(t, b)
}
