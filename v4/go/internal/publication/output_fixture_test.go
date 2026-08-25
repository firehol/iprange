//go:build linux

// Shared fixtures for the output-machine tests (Rust output_tests.rs
// metaPage-style builders). Linux is the only Go target whose pure-Go
// security owner proves the creator-only ACL today; the darwin/freebsd
// refusal is recorded with the 4-12 platform gate, so the created/secure arms run
// natively there only after the platform acceptance.

package publication

import (
	"crypto/sha512"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

var testFixtureDBID = [16]byte{0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x3a, 0x3b, 0x3c, 0x3d, 0x3e, 0x3f, 0x40}
var testFixtureNonce = [16]byte{0x41, 0x42, 0x43, 0x44, 0x45, 0x46, 0x47, 0x48, 0x49, 0x4a, 0x4b, 0x4c, 0x4d, 0x4e, 0x4f, 0x50}

// testMetaPage builds one valid empty direct-v4 meta page (Rust
// immutable_output builder's empty direct output shape).
func testMetaPage(txn, pageCount uint64) []byte {
	page := make([]byte, format.PageSize)
	copy(page[0:8], format.MainMagic[:])
	format.PutU16(page[8:10], format.MetaSize)
	page[10] = format.PageShift
	page[11] = format.AddressFamilyIPv4
	page[12] = format.ValueKindDirect
	copy(page[16:32], "direct\x00")
	copy(page[32:48], testFixtureDBID[:])
	format.PutU64(page[48:56], txn)
	copy(page[56:72], testFixtureNonce[:])
	format.PutU64(page[72:80], pageCount)
	format.PutU32(page[252:256], format.MetaCRC32C(page))
	return page
}

// testFinishedPages returns the two meta pages of a finished empty
// direct output plus their SHA-512 over the exact file bytes.
func testFinishedPages() ([]byte, []byte, [64]byte) {
	page0 := testMetaPage(1, 2)
	page1 := testMetaPage(1, 2)
	hasher := sha512.New()
	_, _ = hasher.Write(page0)
	_, _ = hasher.Write(page1)
	var sum [64]byte
	hasher.Sum(sum[:0])
	return page0, page1, sum
}

// testSecuredAttempt creates and secures one private output inside dir
// for the destination mainName, returning the attempt and its file
// (Rust output_tests armed_attempt prefix).
func testSecuredAttempt(t *testing.T, dir, mainName string) (outputAttempt, *os.File) {
	t.Helper()
	created, err := createOutput(filepath.Join(dir, mainName))
	if err != nil {
		t.Fatalf("create output: %v", err)
	}
	secured, failure := created.secure()
	if failure != nil {
		t.Fatalf("secure output: %v", failure)
	}
	attempt, file := secured.intoParts()
	return attempt, file
}

// testFinishedOutput builds the finished empty direct output into the
// secured attempt file and returns the finished output plus the exact
// byte digest (the empty db is two committed meta pages).
func testFinishedOutput(t *testing.T, file *os.File) (FinishedOutput, [64]byte) {
	t.Helper()
	page0, page1, sum := testFinishedPages()
	if _, err := file.WriteAt(page0, 0); err != nil {
		t.Fatalf("write meta page 0: %v", err)
	}
	if _, err := file.WriteAt(page1, format.PageSize); err != nil {
		t.Fatalf("write meta page 1: %v", err)
	}
	mapped, err := mapping.MapFile(file, 2*format.PageSize, false)
	if err != nil {
		t.Fatalf("map finished output: %v", err)
	}
	meta, ok := format.ParseIdentity(page0)
	if !ok {
		t.Fatal("test meta page does not parse")
	}
	return FinishedOutput{File: file, Mapping: mapped, Meta: meta}, sum
}
