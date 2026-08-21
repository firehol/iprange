// Publication staging tests (Rust publication output/workflow/cleanup
// tests): attempt naming, the fail-if-exists refusal, atomic exchange
// and plain replacement, custody failures, discard cleanup states, and
// the Rust-verbatim error surface.

package writer_test

import (
	"errors"
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
	b, err := writer.NewOutputBuilder(attempt.AttemptPath(), directSpec(format.AddressFamilyIPv4), generousBudget(), 0, nil)
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

func TestCreateAttemptFailIfExistsChecksCoordinationTwin(t *testing.T) {
	// Rust require_fail_if_exists_available refuses when EITHER the
	// main name or its .readers coordination twin exists (namespace.rs
	// require_absent twice); a live sidecar or crash residue occupies
	// the destination slot even when the main name is free.
	dir := t.TempDir()
	twin := filepath.Join(dir, "output.iprdb.readers")
	if err := os.WriteFile(twin, []byte("live"), 0o600); err != nil {
		t.Fatal("write twin:", err)
	}
	if _, err := writer.CreateAttempt(filepath.Join(dir, "output.iprdb"), writer.PolicyFailIfExists); err == nil {
		t.Fatal("CreateAttempt succeeded with the coordination twin present")
	} else if code := errorCode(t, err); code != format.CodeNameExists {
		t.Fatalf("CreateAttempt twin-present code = %d, want NameExists", code)
	}
	// The main name itself may also be present, and the refusal is the
	// same class: write it and re-probe with both entries occupying
	// the destination slot.
	main := filepath.Join(dir, "output.iprdb")
	if err := os.WriteFile(main, []byte("published"), 0o600); err != nil {
		t.Fatal("write main:", err)
	}
	if _, err := writer.CreateAttempt(main, writer.PolicyFailIfExists); err == nil {
		t.Fatal("CreateAttempt succeeded with the main and the coordination twin present")
	}
	// Replace policies never check absence (Rust workflow::create uses
	// create() for replace): the twin is no obstacle there.
	if _, err := writer.CreateAttempt(filepath.Join(dir, "output.iprdb"), writer.PolicyReplaceExisting); err != nil {
		t.Fatalf("CreateAttempt replace with twin present: %v", err)
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

func TestCreateAttemptReservedNameASCIIFoldAndLength(t *testing.T) {
	dir := t.TempDir()
	// The reserved matches are ASCII-case-insensitive exactly like Rust
	// eq_ignore_ascii_case: mixed-case spellings refuse.
	for _, name := range []string{".IpRange-mixed", "OUTPUT.READERS", ".iprange-x", "name.readers"} {
		destination := filepath.Join(dir, name)
		_, err := writer.CreateAttempt(destination, writer.PolicyReplaceExisting)
		if err == nil {
			t.Fatalf("CreateAttempt(%q) with a reserved ASCII spelling succeeded", destination)
		}
		if code := errorCode(t, err); code != format.CodeNameInvalid {
			t.Fatalf("CreateAttempt(%q) code = %d, want NameInvalid", destination, code)
		}
	}
	// Unicode folding is NOT applied: U+0130 (Turkish dotted capital I)
	// lowercases to 'i' under Go's Unicode ToLower but is not an ASCII
	// 'i' to eq_ignore_ascii_case, so Rust accepts this spelling and Go
	// must too. The attempt is created at the reserved-check stage, so an
	// accepted name proceeds past validation (the parent dir exists).
	destination := filepath.Join(dir, ".\u0130PRANGE-fine")
	attempt, err := writer.CreateAttempt(destination, writer.PolicyFailIfExists)
	if err != nil {
		t.Fatalf("CreateAttempt with the dotted-I spelling: %v (the ASCII-only fold must accept it)", err)
	}
	_ = attempt
	// The main component and its .readers coordination twin must both fit
	// the 255-byte name bound (Rust require_name_lengths): the main name
	// alone may reach 255 bytes, but with the 8-byte coordination suffix
	// the twin overflows, so 255-byte and 256-byte mains refuse before
	// any filesystem work, and 247 (255-8) is the last accepted length.
	long255 := strings.Repeat("a", 255)
	_, err = writer.CreateAttempt(filepath.Join(dir, long255), writer.PolicyFailIfExists)
	if err == nil {
		t.Fatal("CreateAttempt(255-byte name) succeeded, want NameInvalid")
	}
	if code := errorCode(t, err); code != format.CodeNameInvalid {
		t.Fatalf("CreateAttempt(255-byte name) code = %d, want NameInvalid", code)
	}
	long256 := strings.Repeat("a", 256)
	_, err = writer.CreateAttempt(filepath.Join(dir, long256), writer.PolicyFailIfExists)
	if err == nil {
		t.Fatal("CreateAttempt(256-byte name) succeeded, want NameInvalid")
	}
	if code := errorCode(t, err); code != format.CodeNameInvalid {
		t.Fatalf("CreateAttempt(256-byte name) code = %d, want NameInvalid", code)
	}
	// The coordination twin bound: a 251-byte main name leaves no room
	// for the 8-byte .readers suffix, so it refuses like the Rust
	// require_name_lengths check.
	long251 := strings.Repeat("a", 251)
	_, err = writer.CreateAttempt(filepath.Join(dir, long251), writer.PolicyFailIfExists)
	if err == nil {
		t.Fatal("CreateAttempt(251-byte name) succeeded, want NameInvalid")
	}
	if code := errorCode(t, err); code != format.CodeNameInvalid {
		t.Fatalf("CreateAttempt(251-byte name) code = %d, want NameInvalid", code)
	}
	// 251-255 refuse through the twin bound; 255-8 = 247 is the last
	// accepted main-component length (255 main bytes + 8 .readers bytes
	// would overflow the same 255-byte directory bound).
	long250 := strings.Repeat("a", 250)
	_, err = writer.CreateAttempt(filepath.Join(dir, long250), writer.PolicyFailIfExists)
	if err == nil {
		t.Fatal("CreateAttempt(250-byte name) succeeded, want NameInvalid")
	}
	if code := errorCode(t, err); code != format.CodeNameInvalid {
		t.Fatalf("CreateAttempt(250-byte name) code = %d, want NameInvalid", code)
	}
	long247 := strings.Repeat("a", 247)
	if _, err := writer.CreateAttempt(filepath.Join(dir, long247), writer.PolicyFailIfExists); err != nil {
		t.Fatalf("CreateAttempt(247-byte name) = %v, want success", err)
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

// TestPublishFailIfExistsCoordinationTwinRefused pins the publish-time
// coordination twin check: a foreign .readers file appearing between
// CreateAttempt and Publish refuses the publication with NotPublished +
// NameExists and discards the attempt, preserving the foreign twin
// (Rust reservation_file foreign_coordination_is_preserved_...: the
// reservation rename to the coordination name refuses, NotPublished,
// Absent, Clean, NameExists).
func TestPublishFailIfExistsCoordinationTwinRefused(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "output.iprdb")
	attempt, b := stagedBuilder(t, destination, writer.PolicyFailIfExists)
	twin := destination + ".readers"
	if err := os.WriteFile(twin, []byte("foreign"), 0o600); err != nil {
		t.Fatal("write twin:", err)
	}
	result, err := writer.Publish(attempt, b, writer.PolicyFailIfExists)
	if err != nil {
		t.Fatalf("Publish returned an error for a coordination twin refusal: %v", err)
	}
	if result.Status != writer.PublicationNotPublished {
		t.Fatalf("status = %v, want NotPublished", result.Status)
	}
	if result.DestinationContent != writer.DestinationContentAbsent {
		t.Fatalf("content = %v, want Absent", result.DestinationContent)
	}
	if code := errorCode(t, result.Cause); code != format.CodeNameExists {
		t.Fatalf("cause code = %d, want NameExists", code)
	}
	if result.Cleanup != writer.CleanupStateClean {
		t.Fatalf("cleanup = %v, want Clean", result.Cleanup)
	}
	closeBuilder(t, b)
	got, readErr := os.ReadFile(twin)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "foreign" {
		t.Fatalf("foreign twin changed to %q", got)
	}
	if _, err := os.Lstat(attempt.AttemptPath()); !os.IsNotExist(err) {
		t.Fatalf("attempt file still exists after twin refusal: %v", err)
	}
}

// TestPublishFailIfExistsMainAppearedRefused pins the publish-time main
// check (Rust reservation_file.rs arm_with require_absent(destination.
// main)): a foreign main appearing between CreateAttempt and Publish is
// the NotPublished classification with Unclassified content (attempt.rs
// not_published never reads the foreign main) and the attempt is
// discarded, never a rename-race outcome_unknown.
func TestPublishFailIfExistsMainAppearedRefused(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "output.iprdb")
	attempt, b := stagedBuilder(t, destination, writer.PolicyFailIfExists)
	if err := os.WriteFile(destination, []byte("foreign"), 0o600); err != nil {
		t.Fatal("write main:", err)
	}
	result, err := writer.Publish(attempt, b, writer.PolicyFailIfExists)
	if err != nil {
		t.Fatalf("Publish returned an error for a main-appeared refusal: %v", err)
	}
	if result.Status != writer.PublicationNotPublished {
		t.Fatalf("status = %v, want NotPublished", result.Status)
	}
	if result.DestinationContent != writer.DestinationContentUnclassified {
		t.Fatalf("content = %v, want Unclassified", result.DestinationContent)
	}
	if code := errorCode(t, result.Cause); code != format.CodeNameExists {
		t.Fatalf("cause code = %d, want NameExists", code)
	}
	closeBuilder(t, b)
	got, readErr := os.ReadFile(destination)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "foreign" {
		t.Fatalf("foreign main changed to %q", got)
	}
	if _, err := os.Lstat(attempt.AttemptPath()); !os.IsNotExist(err) {
		t.Fatalf("attempt file still exists after main refusal: %v", err)
	}
	if detail := errorDetail(t, result.Cause); detail != "publication name already exists" {
		t.Fatalf("cause detail = %q, want the Rust detail", detail)
	}
	if result.Cleanup != writer.CleanupStateClean {
		t.Fatalf("cleanup = %v, want Clean", result.Cleanup)
	}
}

// TestPublishReplaceRefusesCoordinationTwin pins the Rust replacement
// bind: require_absent(coordination) inside replacement::open() refuses
// with the early preparation failure and discards the attempt, even
// when the main destination exists.
func TestPublishReplaceRefusesCoordinationTwin(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "output.iprdb")
	if err := os.WriteFile(destination, []byte("previous"), 0o644); err != nil {
		t.Fatal(err)
	}
	attempt, b := stagedBuilder(t, destination, writer.PolicyReplaceExisting)
	twin := destination + ".readers"
	if err := os.WriteFile(twin, []byte("foreign"), 0o600); err != nil {
		t.Fatal("write twin:", err)
	}
	_, err := writer.Publish(attempt, b, writer.PolicyReplaceExisting)
	if err == nil {
		t.Fatal("replace publish succeeded with the coordination twin present")
	}
	var failure *writer.PublicationPreparationFailure
	if !errors.As(err, &failure) {
		t.Fatalf("error type = %T, want *PublicationPreparationFailure", err)
	}
	if code := errorCode(t, failure.Cause); code != format.CodeNameExists {
		t.Fatalf("cause code = %d, want NameExists", code)
	}
	if failure.Cleanup != writer.CleanupStateClean {
		t.Fatalf("cleanup = %v, want Clean", failure.Cleanup)
	}
	closeBuilder(t, b)
	got, readErr := os.ReadFile(twin)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "foreign" {
		t.Fatalf("foreign twin changed to %q", got)
	}
	if _, err := os.Lstat(attempt.AttemptPath()); !os.IsNotExist(err) {
		t.Fatalf("attempt file still exists after twin refusal: %v", err)
	}
}

// TestPublishAttemptLinkCountRefused pins the Rust verify_name link-count
// custody proof (namespace.rs): a hard-linked attempt file has links == 2
// and the publish refuses with the conflict class before any rename. The
// cleanup still removes the exact-name entry (identity stays bound), so
// the namespace is clean.
func TestPublishAttemptLinkCountRefused(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "output.iprdb")
	attempt, b := stagedBuilder(t, destination, writer.PolicyFailIfExists)
	linkPath := filepath.Join(dir, "attempt-hardlink.tmp")
	if err := os.Link(attempt.AttemptPath(), linkPath); err != nil {
		t.Skipf("filesystem refuses hard links: %v", err)
	}
	defer os.Remove(linkPath)
	_, err := writer.Publish(attempt, b, writer.PolicyFailIfExists)
	if err == nil {
		t.Fatal("publish succeeded with a hard-linked attempt file")
	}
	var failure *writer.PublicationPreparationFailure
	if !errors.As(err, &failure) {
		t.Fatalf("error type = %T, want *PublicationPreparationFailure", err)
	}
	if code := errorCode(t, failure.Cause); code != format.CodeConflict {
		t.Fatalf("cause code = %d, want Conflict", code)
	}
	if failure.Cleanup != writer.CleanupStateClean {
		t.Fatalf("cleanup = %v, want Clean (exact-name entry removed)", failure.Cleanup)
	}
	closeBuilder(t, b)
	if _, err := os.Lstat(attempt.AttemptPath()); !os.IsNotExist(err) {
		t.Fatalf("attempt entry still exists after refuse-and-discard: %v", err)
	}
}

// TestPublishReplaceNilCoordinationTwinKeepsWorking: without the twin the
// replace policy still publishes (the twin check is the only added gate).
func TestPublishReplaceNoTwinPublishes(t *testing.T) {
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
	closeBuilder(t, b)
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
	if result.Cleanup != writer.CleanupStateClean {
		t.Fatalf("cleanup = %v, want Clean (Rust retire_steps unlinks the exchanged previous)", result.Cleanup)
	}
	closeBuilder(t, b)
	r, err := iprangedb.OpenImmutable(destination)
	if err != nil {
		t.Fatalf("published destination does not reopen: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	// Rust retire_steps unlink_previous: the exchange swapped the
	// previous destination onto the private attempt name, and the
	// retirement must remove it so no private artifact survives a
	// successful replacement.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal("read directory:", err)
	}
	for _, entry := range entries {
		if len(entry.Name()) >= len(".iprange-") && entry.Name()[:len(".iprange-")] == ".iprange-" {
			t.Errorf("private artifact remained after the exchange: %s", entry.Name())
		}
	}
}

func TestPublishExchangeMissingDestination(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "output.iprdb")
	attempt, b := stagedBuilder(t, destination, writer.PolicyReplaceExisting)
	_, err := writer.Publish(attempt, b, writer.PolicyReplaceExisting)
	if err == nil {
		t.Fatal("Publish succeeded with a missing replacement destination")
	}
	var failure *writer.PublicationPreparationFailure
	if !errors.As(err, &failure) {
		t.Fatalf("error type = %T, want *PublicationPreparationFailure", err)
	}
	// Rust replacement::bind refuses a missing destination at the
	// preparation stage (Early failure) and discards the attempt.
	if code := errorCode(t, failure.Cause); code != format.CodeNameNotFound {
		t.Fatalf("cause code = %d, want NameNotFound", code)
	}
	if detail := errorDetail(t, failure.Cause); detail != "publication name is missing" {
		t.Fatalf("cause detail = %q, want the Rust detail", detail)
	}
	if failure.Cleanup != writer.CleanupStateClean {
		t.Fatalf("cleanup = %v, want Clean", failure.Cleanup)
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
	b, err := writer.NewOutputBuilder(attempt.AttemptPath(), directSpec(format.AddressFamilyIPv4), generousBudget(), 0, nil)
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

func TestPublishOutcomeUnknownRetainsArtifact(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission-based refusal test needs an unprivileged POSIX user")
	}
	dir := t.TempDir()
	destination := filepath.Join(dir, "output.iprdb")
	attempt, b := stagedBuilder(t, destination, writer.PolicyFailIfExists)
	// A blocked rename (read-only directory, destination absent: the
	// fail-if-exists twin/main re-checks pass, so the refusal surfaces
	// from the no-replace rename): the refusal is Rust outcome_unknown -
	// Unclassified, CleanupState::Clean (nothing removed), and the
	// private artifact retained as recovery residue for the caller.
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
	if result.Status != writer.PublicationOutcomeUnknown {
		t.Fatalf("status = %v, want OutcomeUnknown", result.Status)
	}
	if result.Cleanup != writer.CleanupStateClean {
		t.Fatalf("cleanup = %v, want Clean (nothing removed)", result.Cleanup)
	}
	// The residue contract: the attempt file provably remains, so the
	// caller can retry its removal or recovery.
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
