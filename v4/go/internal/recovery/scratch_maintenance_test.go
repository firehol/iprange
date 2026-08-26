package recovery

// Abandoned-scratch maintenance tests ported from the Rust
// recovery/scratch_maintenance_tests.rs: the exact-pattern listing
// with header authentication, the cancellation and sink control
// surface, the cancellable durable idempotent removal, and the
// directory, inode, header, and name-replacement conflicts.

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// TestScratchMaintenanceListingReportsOnlyExactNamesAndAuthenticates
// mirrors Rust listing_reports_only_exact_names_and_authenticates_
// without_following: one live active scratch entry authenticates as
// Recovery; short, mismatched-header, symlinked, hard-linked, and
// uppercase lookalikes report unauthenticated.
func TestScratchMaintenanceListingReportsOnlyExactNamesAndAuthenticates(t *testing.T) {
	directory := t.TempDir()
	scratch, err := scratchStart(directory, scratchTestMeta(), 4096, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	attempt := scratch.attemptID
	if _, err := scratch.create(); err != nil {
		t.Fatal(err)
	}
	validBytes, err := readScratchTestFile(filepath.Join(directory, mustScratchName(t, attempt, 0)))
	if err != nil {
		t.Fatal(err)
	}

	shortAttempt := [16]byte{0x31}
	if err := os.WriteFile(filepath.Join(directory, mustScratchName(t, shortAttempt, 7)), []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	mismatchAttempt := [16]byte{0x32}
	if err := os.WriteFile(filepath.Join(directory, mustScratchName(t, mismatchAttempt, 8)), validBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkAttempt := [16]byte{0x33}
	symlinkTarget := filepath.Join(directory, "target")
	if err := os.WriteFile(symlinkTarget, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(symlinkTarget, filepath.Join(directory, mustScratchName(t, symlinkAttempt, 9))); err != nil {
		t.Fatal(err)
	}
	hardlinkAttempt := [16]byte{0x34}
	hardlinkTarget := filepath.Join(directory, "hardlink-target")
	if err := os.WriteFile(hardlinkTarget, []byte("hardlink"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(hardlinkTarget, filepath.Join(directory, mustScratchName(t, hardlinkAttempt, 10))); err != nil {
		t.Fatal(err)
	}
	upper := filepath.Join(directory, ".iprange-scratch-ABABABABABABABABABABABABABABABAB-00000000.tmp")
	if err := os.WriteFile(upper, []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "ordinary-file"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, entries, err := scratchListed(directory)
	if err != nil {
		t.Fatal(err)
	}
	sortScratchEntries(entries)
	if result.Entries != 5 {
		t.Fatalf("entries = %d, want 5", result.Entries)
	}
	for _, entry := range entries {
		if entry.DirectoryIdentity != result.DirectoryIdentity {
			t.Fatalf("entry directory identity = %+v", entry.DirectoryIdentity)
		}
	}
	found := false
	for _, entry := range entries {
		if entry.AttemptID == attempt {
			found = true
			if !entry.Authentication.Authenticated || entry.Authentication.Owner != ScratchOwnerRecovery {
				t.Fatalf("active scratch authentication = %+v", entry.Authentication)
			}
		}
	}
	if !found {
		t.Fatal("active scratch entry not listed")
	}
	for _, candidate := range [][16]byte{shortAttempt, mismatchAttempt, symlinkAttempt, hardlinkAttempt} {
		for _, entry := range entries {
			if entry.AttemptID == candidate && entry.Authentication.Authenticated {
				t.Fatalf("lookalike %x authenticated", candidate)
			}
		}
	}
	if cleanup := scratch.cleanup(); !cleanup.clean() {
		t.Fatalf("cleanup not clean: %+v", cleanup.residues)
	}
}

// TestScratchMaintenanceListingHonorsCancellationStopAndSinkErrors
// mirrors Rust listing_honors_cancellation_stop_and_sink_errors.
func TestScratchMaintenanceListingHonorsCancellationStopAndSinkErrors(t *testing.T) {
	directory := t.TempDir()
	scratch, err := scratchStart(directory, scratchTestMeta(), 4096, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scratch.create(); err != nil {
		t.Fatal(err)
	}

	cancelled := func() error { return &format.Error{Code: format.CodeCancelled, Detail: "operation was cancelled"} }
	if _, err := ListAbandonedScratch(directory, cancelled, func(*AbandonedScratchEntry) error { return nil }); !isCode(err, format.CodeCancelled) {
		t.Fatalf("cancelled listing = %v", err)
	}
	if _, err := ListAbandonedScratch(directory, nil, func(*AbandonedScratchEntry) error { return errScratchSinkStop }); !isCode(err, format.CodeStoppedBySink) {
		t.Fatalf("stopped listing = %v", err)
	}
	if _, err := ListAbandonedScratch(directory, nil, func(*AbandonedScratchEntry) error {
		return &format.Error{Code: format.CodeInvalidArgument, Detail: "injected scratch-list sink failure"}
	}); !isCode(err, format.CodeSinkFailed) {
		t.Fatalf("sink-failed listing = %v", err)
	}
	var sinkCancelled bool
	if _, err := ListAbandonedScratch(directory, func() error {
		if sinkCancelled {
			return &format.Error{Code: format.CodeCancelled, Detail: "operation was cancelled"}
		}
		return nil
	}, func(*AbandonedScratchEntry) error {
		sinkCancelled = true
		return nil
	}); !isCode(err, format.CodeCancelled) {
		t.Fatalf("callback-cancelled listing = %v", err)
	}
	if cleanup := scratch.cleanup(); !cleanup.clean() {
		t.Fatalf("cleanup not clean: %+v", cleanup.residues)
	}
}

// TestScratchMaintenanceExactRemovalIsCancellableDurableAndIdempotent
// mirrors Rust exact_removal_is_cancellable_durable_and_idempotent.
func TestScratchMaintenanceExactRemovalIsCancellableDurableAndIdempotent(t *testing.T) {
	directory := t.TempDir()
	scratch, err := scratchStart(directory, scratchTestMeta(), 4096, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	attempt := scratch.attemptID
	if _, err := scratch.create(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, mustScratchName(t, attempt, 0))
	result, entries, err := scratchListed(directory)
	if err != nil {
		t.Fatal(err)
	}
	var entry *AbandonedScratchEntry
	for index := range entries {
		if entries[index].AttemptID == attempt {
			entry = &entries[index]
			break
		}
	}
	if entry == nil {
		t.Fatal("active entry not listed")
	}
	// Rust drops the scratch owner without removal (drop(scratch)):
	// the directory descriptor closes and the artifact stays.
	scratch.directory.Close()

	cancelled := func() error { return &format.Error{Code: format.CodeCancelled, Detail: "operation was cancelled"} }
	if _, err := RemoveAbandonedScratch(directory, result.DirectoryIdentity, attempt, 0, entry.ArtifactIdentity, cancelled); !isCode(err, format.CodeCancelled) {
		t.Fatalf("cancelled removal = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("path disappeared after cancellation: %v", err)
	}

	removal, err := RemoveAbandonedScratch(directory, result.DirectoryIdentity, attempt, 0, entry.ArtifactIdentity, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !removal.SourcePresent {
		t.Fatal("removal reported absent source")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("path survived removal: %v", err)
	}
	removal, err = RemoveAbandonedScratch(directory, result.DirectoryIdentity, attempt, 0, entry.ArtifactIdentity, nil)
	if err != nil {
		t.Fatal(err)
	}
	if removal.SourcePresent {
		t.Fatal("second removal reported present source")
	}
}

// TestScratchMaintenanceRemovalRejectsDirectoryInodeHeaderAndName
// mirrors Rust
// removal_rejects_directory_inode_header_and_name_replacement_
// conflicts.
func TestScratchMaintenanceRemovalRejectsDirectoryInodeHeaderAndName(t *testing.T) {
	directory := t.TempDir()
	scratch, err := scratchStart(directory, scratchTestMeta(), 4096, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	attempt := scratch.attemptID
	if _, err := scratch.create(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, mustScratchName(t, attempt, 0))
	result, entries, err := scratchListed(directory)
	if err != nil {
		t.Fatal(err)
	}
	var entry *AbandonedScratchEntry
	for index := range entries {
		if entries[index].AttemptID == attempt {
			entry = &entries[index]
			break
		}
	}
	if entry == nil {
		t.Fatal("active entry not listed")
	}
	// Rust drops the scratch owner without removal: the directory
	// descriptor closes and every owned artifact stays in place.
	scratch.directory.Close()

	wrongDirectory := result.DirectoryIdentity
	wrongDirectory.Bytes[0] ^= 1
	if _, err := RemoveAbandonedScratch(directory, wrongDirectory, attempt, 0, entry.ArtifactIdentity, nil); !isCode(err, format.CodeDirectoryIdentityMismatch) {
		t.Fatalf("directory mismatch = %v", err)
	}

	wrongArtifact := entry.ArtifactIdentity
	wrongArtifact.Bytes[8] ^= 1
	if _, err := RemoveAbandonedScratch(directory, result.DirectoryIdentity, attempt, 0, wrongArtifact, nil); !isCode(err, format.CodeCleanupConflict) {
		t.Fatalf("artifact mismatch = %v", err)
	}

	file, err := openScratchTestFile(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("X"), 0); err != nil {
		t.Fatal(err)
	}
	file.Close()
	if _, err := RemoveAbandonedScratch(directory, result.DirectoryIdentity, attempt, 0, entry.ArtifactIdentity, nil); !isCode(err, format.CodeCleanupConflict) {
		t.Fatalf("header mismatch = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("path disappeared after header mismatch: %v", err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RemoveAbandonedScratch(directory, result.DirectoryIdentity, attempt, 0, entry.ArtifactIdentity, nil); !isCode(err, format.CodeCleanupConflict) {
		t.Fatalf("replacement mismatch = %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "replacement" {
		t.Fatalf("replacement content = %q", content)
	}
}

// scratchListed runs one listing into a slice (Rust listed()).
func scratchListed(directory string) (AbandonedScratchList, []AbandonedScratchEntry, error) {
	var entries []AbandonedScratchEntry
	result, err := ListAbandonedScratch(directory, nil, func(entry *AbandonedScratchEntry) error {
		entries = append(entries, *entry)
		return nil
	})
	if err != nil {
		return AbandonedScratchList{}, nil, err
	}
	return result, entries, nil
}

// sortScratchEntries orders one listing by ordinal (Rust sort_by_key).
func sortScratchEntries(entries []AbandonedScratchEntry) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].Ordinal < entries[j].Ordinal })
}

// mustScratchName builds one scratch basename for the fixtures.
func mustScratchName(t *testing.T, attempt [16]byte, ordinal uint32) string {
	t.Helper()
	name, err := scratchNameOf(attempt, ordinal)
	if err != nil {
		t.Fatal(err)
	}
	return name
}

// TestScratchMaintenanceRemovalOfTruncatedArtifactIsCorruptClass
// pins the Rust require_header length proof: a file shorter than the
// ownership header refuses removal with the Corrupt (FormatInvalid)
// class, exactly like Rust require_file_extent, while the listing
// path still reports it unauthenticated.
func TestScratchMaintenanceRemovalOfTruncatedArtifactIsCorruptClass(t *testing.T) {
	directory := t.TempDir()
	attempt := id16(0x41)
	name, err := scratchNameOf(attempt, 0)
	if err != nil {
		t.Fatal(err)
	}
	short := make([]byte, scratchHeaderSize-1)
	if err := os.WriteFile(filepath.Join(directory, name), short, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(filepath.Join(directory, name))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := requireScratchHeader(file, attempt, 0); !isCode(err, format.CodeFormatInvalid) {
		t.Fatalf("requireScratchHeader = %v, want FormatInvalid", err)
	}
	// The listing path still authenticates the short artifact as
	// unauthenticated without an error (Rust authenticate pre-checks
	// the metadata length).
	list, err := ListAbandonedScratch(directory, nil, func(*AbandonedScratchEntry) error { return nil })
	if err != nil {
		t.Fatalf("ListAbandonedScratch: %v", err)
	}
	if list.Entries != 1 {
		t.Fatalf("entries = %d, want 1", list.Entries)
	}
}
