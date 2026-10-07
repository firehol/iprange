package live

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// createPolicySource creates one live database recording creator_only.
func createPolicySource(t *testing.T, main string, creatorOnly bool) {
	t.Helper()
	tag := [16]byte{}
	copy(tag[:], []byte("asn"))
	if _, err := CreateLive(main, format.AddressFamilyIPv4, format.ValueKindDirect,
		format.StructureKindNone, tag, 2, nil, creatorOnly); err != nil {
		t.Fatal(err)
	}
}

// A valid-CRC header whose identities are zero is corrupt under the
// full read_header rule: the classifier must follow the process
// switch, never the policy byte such a header carries. Mutation probe:
// removing headerIdentitiesValid flips the answer to protected. The
// test has no unix constructs: SourceCreatorOnly and the sidecar read
// ship on Windows, so it runs everywhere (Rust twin:
// live_sidecar.rs classifier_rejects_a_zero_identity_header).
func TestSourceCreatorOnlyRejectsZeroIdentityHeader(t *testing.T) {
	if err := CreationSupported(); err != nil {
		t.Skipf("live database creation is not supported on this platform: %v", err)
	}
	t.Setenv("IPRANGE_CREATOR_ONLY", "0")
	dir := t.TempDir()
	main := filepath.Join(dir, "crafted.iprdb")
	createPolicySource(t, main, true)
	sidecar, err := CanonicalSidecarPath(main)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatal(err)
	}
	page := make([]byte, sidecarPageSize)
	copy(page, content)
	for index := databaseIDOff; index < sidecarIDOff+16; index++ {
		page[index] = 0
	}
	checksum, ok := format.CRC32CWithZeroed(page, headerCRCOff, headerCRCLen)
	if !ok {
		t.Fatal("crc window")
	}
	format.PutU32(page[headerCRCOff:], checksum)
	// Rewrite the header page in place: the rest of the sidecar (the
	// reader slots past the first page) is untouched.
	if err := writeSidecarPage(sidecar, page); err != nil {
		t.Fatal(err)
	}
	if SourceCreatorOnly(main) {
		t.Fatal("zero-identity header answered protected instead of following the switch")
	}
	t.Setenv("IPRANGE_CREATOR_ONLY", "1")
	if !SourceCreatorOnly(main) {
		t.Fatal("zero-identity header with the switch on must follow the switch")
	}
}

// writeSidecarPage rewrites the first sidecar page without disturbing
// the slot pages behind it.
func writeSidecarPage(sidecar string, page []byte) error {
	handle, err := os.OpenFile(sidecar, os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := handle.WriteAt(page[:sidecarPageSize], 0); err != nil {
		handle.Close()
		return err
	}
	return handle.Close()
}
