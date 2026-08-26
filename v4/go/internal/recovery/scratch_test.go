package recovery

// Authorized scratch owner tests ported from the Rust
// recovery/scratch_tests.rs: the exact names, header, I/O, and
// cleanup round trip; the byte and descriptor budget refusals; the
// exclusive-creation protection; and the changed-link-count residue.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
)

// scratchTestMeta builds the fixed scratch source facts of the Rust
// scratch tests (database id 0x11, txn 9, nonce 0x22).
func scratchTestMeta() format.Meta {
	return format.Meta{
		AddressFamily: format.AddressFamilyIPv4,
		ValueKind:     format.ValueKindDirect,
		ValueTag:      tag16("first-seen"),
		DatabaseID:    id16(0x11),
		TxnID:         9,
		CommitNonce:   id16(0x22),
		PageCount:     2,
	}
}

// TestScratchExactNamesHeadersIOAndCleanupRoundTrip mirrors Rust
// exact_names_headers_io_and_cleanup_round_trip: two owned artifacts
// carry the exact names and headers, mapped writes and reads round
// trip, detach/attach preserves ownership, reset restores the bare
// header, and the cleanup leaves the directory empty.
func TestScratchExactNamesHeadersIOAndCleanupRoundTrip(t *testing.T) {
	directory := t.TempDir()
	scratch, err := scratchStart(directory, scratchTestMeta(), 4096, 2, 4)
	if err != nil {
		t.Fatalf("scratchStart: %v", err)
	}
	attempt := scratch.attemptID
	first, err := scratch.create()
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	second, err := scratch.create()
	if err != nil {
		t.Fatalf("create second: %v", err)
	}
	firstBytes, err := scratchNameOf(attempt, 0)
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, err := scratchNameOf(attempt, 1)
	if err != nil {
		t.Fatal(err)
	}
	if firstBytes != scratchTestExpectedName(attempt, "00000000") || secondBytes != scratchTestExpectedName(attempt, "00000001") {
		t.Fatalf("names = %q %q", firstBytes, secondBytes)
	}

	headerBytes, err := readScratchTestFile(filepath.Join(directory, firstBytes))
	if err != nil {
		t.Fatal(err)
	}
	if len(headerBytes) != scratchHeaderSize {
		t.Fatalf("header length %d, want %d", len(headerBytes), scratchHeaderSize)
	}
	if string(headerBytes[0:8]) != "IPR4SCR1" {
		t.Fatalf("magic = %q", headerBytes[0:8])
	}
	if format.U16(headerBytes[8:10]) != 1 || format.U16(headerBytes[10:12]) != 128 || format.U16(headerBytes[12:14]) != 2 {
		t.Fatalf("fixed fields = %d %d %d", format.U16(headerBytes[8:10]), format.U16(headerBytes[10:12]), format.U16(headerBytes[12:14]))
	}
	expectedID := id16(0x11)
	if string(headerBytes[16:32]) != string(expectedID[:]) {
		t.Fatalf("database id = %x", headerBytes[16:32])
	}
	if format.U64(headerBytes[32:40]) != 9 {
		t.Fatalf("txn id = %d", format.U64(headerBytes[32:40]))
	}
	expectedNonce := id16(0x22)
	if string(headerBytes[40:56]) != string(expectedNonce[:]) {
		t.Fatalf("commit nonce = %x", headerBytes[40:56])
	}
	if string(headerBytes[56:72]) != string(attempt[:]) {
		t.Fatalf("attempt = %x", headerBytes[56:72])
	}
	if format.U32(headerBytes[72:76]) != 0 || format.U16(headerBytes[76:78]) != scratchCreationSecurityKind() {
		t.Fatalf("ordinal/security kind = %d %d", format.U32(headerBytes[72:76]), format.U16(headerBytes[76:78]))
	}
	checksum, ok := format.CRC32CWithZeroed(headerBytes, scratchHeaderCRCOffset, scratchHeaderCRCSize)
	if !ok || checksum != format.U32(headerBytes[124:128]) {
		t.Fatalf("header CRC = %x stored %x ok=%v", checksum, format.U32(headerBytes[124:128]), ok)
	}

	if err := scratch.write(first, scratchHeaderSize, []byte("abcdef")); err != nil {
		t.Fatal(err)
	}
	var read [6]byte
	if err := scratch.read(first, scratchHeaderSize, read[:]); err != nil {
		t.Fatal(err)
	}
	if string(read[:]) != "abcdef" {
		t.Fatalf("read = %q", read[:])
	}
	if scratch.length(first) != scratchHeaderSize+6 {
		t.Fatalf("length = %d", scratch.length(first))
	}
	if err := scratch.resize(first, scratchHeaderSize+64); err != nil {
		t.Fatal(err)
	}
	detached := scratch.detach(first)
	if err := detached.write(scratchHeaderSize, []byte("detached")); err != nil {
		t.Fatal(err)
	}
	var detachedRead [8]byte
	if err := detached.read(scratchHeaderSize, detachedRead[:]); err != nil {
		t.Fatal(err)
	}
	if string(detachedRead[:]) != "detached" {
		t.Fatalf("detached read = %q", detachedRead[:])
	}
	if got := scratch.attach(detached); got != first {
		t.Fatalf("attach slot = %+v", got)
	}
	if err := scratch.reset(first); err != nil {
		t.Fatal(err)
	}
	if scratch.length(first) != scratchHeaderSize || scratch.length(second) != scratchHeaderSize {
		t.Fatalf("reset lengths = %d %d", scratch.length(first), scratch.length(second))
	}

	cleanup := scratch.cleanup()
	if !cleanup.clean() {
		t.Fatalf("cleanup not clean: %+v", cleanup.residues)
	}
	if cleanup.attemptID != attempt {
		t.Fatalf("cleanup attempt = %x", cleanup.attemptID)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("directory retained %d entries", len(entries))
	}
}

// TestScratchByteFileAndDescriptorBudgetsFailBeforeGrowth mirrors
// Rust byte_file_and_descriptor_budgets_fail_before_growth.
func TestScratchByteFileAndDescriptorBudgetsFailBeforeGrowth(t *testing.T) {
	directory := t.TempDir()
	if _, err := scratchStart(directory, scratchTestMeta(), 127, 2, 4); !isBudgetError(err, "recovery scratch bytes") {
		t.Fatalf("byte refusal = %v", err)
	}
	if _, err := scratchStart(directory, scratchTestMeta(), 4096, 0, 4); !isBudgetError(err, "recovery scratch requires one file descriptor") {
		t.Fatalf("file refusal = %v", err)
	}
	if _, err := scratchStart(directory, scratchTestMeta(), 4096, 2, 2); !isBudgetError(err, "recovery scratch requires one file descriptor") {
		t.Fatalf("descriptor refusal = %v", err)
	}

	scratch, err := scratchStart(directory, scratchTestMeta(), 4096, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := scratch.requireExternalSort(); !isBudgetError(err, "external recovery sort requires two scratch files") {
		t.Fatalf("single-file sort refusal = %v", err)
	}
	if cleanup := scratch.cleanup(); !cleanup.clean() {
		t.Fatalf("cleanup not clean: %+v", cleanup.residues)
	}

	scratch, err = scratchStart(directory, scratchTestMeta(), 4096, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := scratch.requireExternalSort(); !isBudgetError(err, "external recovery sort requires two scratch files") {
		t.Fatalf("open-file sort refusal = %v", err)
	}
	if cleanup := scratch.cleanup(); !cleanup.clean() {
		t.Fatalf("cleanup not clean: %+v", cleanup.residues)
	}

	scratch, err = scratchStart(directory, scratchTestMeta(), 256, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	first, err := scratch.create()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scratch.create(); err != nil {
		t.Fatal(err)
	}
	if err := scratch.write(first, scratchHeaderSize, []byte{1}); !isBudgetError(err, "recovery scratch bytes") {
		t.Fatalf("write refusal = %v", err)
	}
	if cleanup := scratch.cleanup(); !cleanup.clean() {
		t.Fatalf("cleanup not clean: %+v", cleanup.residues)
	}
}

// TestScratchExclusiveCreationNeverReplacesAMatchingLookalike mirrors
// Rust exclusive_creation_never_replaces_a_matching_lookalike: a
// foreign file at the exact name refuses creation and survives the
// cleanup.
func TestScratchExclusiveCreationNeverReplacesAMatchingLookalike(t *testing.T) {
	directory := t.TempDir()
	scratch, err := scratchStart(directory, scratchTestMeta(), 4096, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	name, err := scratchNameOf(scratch.attemptID, 0)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := scratch.create(); !isCode(err, format.CodeNameExists) {
		t.Fatalf("exclusive refusal = %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "foreign" {
		t.Fatalf("lookalike content = %q", content)
	}
	if cleanup := scratch.cleanup(); !cleanup.clean() {
		t.Fatalf("cleanup not clean: %+v", cleanup.residues)
	}
	content, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "foreign" {
		t.Fatalf("lookalike content after cleanup = %q", content)
	}
}

// TestScratchChangedLinkCountIsReturnedAsExactResidue mirrors Rust
// changed_link_count_is_returned_as_exact_residue: an aliased owned
// artifact reports one exact residue with the cleanup-conflict class
// and keeps both links.
func TestScratchChangedLinkCountIsReturnedAsExactResidue(t *testing.T) {
	directory := t.TempDir()
	scratch, err := scratchStart(directory, scratchTestMeta(), 4096, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	attempt := scratch.attemptID
	if _, err := scratch.create(); err != nil {
		t.Fatal(err)
	}
	name, err := scratchNameOf(attempt, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(directory, name), filepath.Join(directory, "alias")); err != nil {
		t.Fatal(err)
	}
	cleanup := scratch.cleanup()
	if cleanup.clean() || len(cleanup.residues) != 1 {
		t.Fatalf("cleanup = %+v, want one residue", cleanup.residues)
	}
	if cleanup.residues[0].ordinal != 0 || string(cleanup.residues[0].basename) != name {
		t.Fatalf("residue = %+v", cleanup.residues[0])
	}
	if cleanup.residues[0].problem.code != scratchChangedLinkResidueClass() {
		t.Fatalf("residue problem = %+v", cleanup.residues[0].problem)
	}
	file, err := openScratchTestFile(filepath.Join(directory, name), false)
	if err != nil {
		t.Fatal(err)
	}
	links, err := live.RegularLinkCount(file)
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if links != 2 {
		t.Fatalf("link count after cleanup = %d, want 2", links)
	}
}

// scratchTestExpectedName builds the exact scratch basename for one
// attempt and ordinal (Rust scratch_tests expected_name).
func scratchTestExpectedName(attempt [16]byte, ordinal string) string {
	expected := ".iprange-scratch-"
	for _, b := range attempt {
		expected += string([]byte{hexNibble(b >> 4), hexNibble(b & 0x0f)})
	}
	return expected + "-" + ordinal + ".tmp"
}

// isBudgetError reports whether one error is the budget class with
// the exact detail.
func isBudgetError(err error, detail string) bool {
	return isCode(err, format.CodeInsufficientResourceBudget) && errDetail(err) == detail
}

// isCode reports whether one error is the exact SDK class.
func isCode(err error, code format.ErrorCode) bool {
	var full *format.Error
	if !errors.As(err, &full) {
		return false
	}
	return full.Code == code
}

// errDetail extracts the detail of one typed SDK error.
func errDetail(err error) string {
	var full *format.Error
	if !errors.As(err, &full) {
		return ""
	}
	return full.Detail
}
