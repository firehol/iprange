package writer

// Empty database creation tests (Rust live_lifecycle/creation.rs
// create_live physical half, SOW-0025 chunk-6 design record D2): the
// created file is a two-page txn-1 database with the identical meta on
// both meta pages, refuses an existing destination, and validates the
// value-kind/structure-kind combination before touching the file.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/reader"
)

// isCode reports whether err is a typed format.Error carrying code.
func isCode(err error, code format.ErrorCode) bool {
	var fe *format.Error
	return errors.As(err, &fe) && fe.Code == code
}

// createDirect writes one fresh direct database through the production
// Create path and returns the created identity.
func createDirect(t *testing.T, family uint8) (string, Created) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "created.iprdb")
	created, err := Create(path, family, format.ValueKindDirect, format.StructureKindNone, [16]byte{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if created.DatabaseID == [16]byte{} || created.CommitNonce == [16]byte{} {
		t.Fatalf("created database carries an empty identity: %+v", created)
	}
	return path, created
}

// TestCreateWritesTxn1TwoPageMetaPair verifies the physical contract of a
// fresh database: page count 2, transaction 1, all roots zero, both meta
// pages identical and proven-current, and no committed metadata.
func TestCreateWritesTxn1TwoPageMetaPair(t *testing.T) {
	path, created := createDirect(t, format.AddressFamilyIPv4)
	r, err := reader.OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	m := r.Meta()
	if m.TxnID != 1 || m.PageCount != 2 {
		t.Fatalf("created meta = txn %d pages %d, want txn 1 pages 2", m.TxnID, m.PageCount)
	}
	if m.DatabaseID != created.DatabaseID || m.CommitNonce != created.CommitNonce {
		t.Fatalf("reader identity (%x, %x) != created identity (%x, %x)", m.DatabaseID, m.CommitNonce, created.DatabaseID, created.CommitNonce)
	}
	if got := fileSize(t, path); got != 2*int64(format.PageSize) {
		t.Fatalf("created size = %d, want %d", got, 2*format.PageSize)
	}
	// The read-only view of a created database reports zero records, zero
	// feeds, and no committed metadata (present=false).
	if m.RangeRecordCount != 0 || m.ActiveFeedCount != 0 || m.RangeRoot != 0 || m.MetadataRoot != 0 {
		t.Fatalf("fresh database reports records=%d feeds=%d roots=%d/%d, want 0/0/0/0", m.RangeRecordCount, m.ActiveFeedCount, m.RangeRoot, m.MetadataRoot)
	}
	if _, present, err := r.ReadMetadataJSON(); err != nil || present {
		t.Fatalf("fresh metadata = present %v err %v, want absent", present, err)
	}
}

// TestCreateRejectsExistingDestination verifies the O_EXCL contract: an
// existing destination is refused with ErrorNameExists and the existing
// file is left untouched.
func TestCreateRejectsExistingDestination(t *testing.T) {
	path, _ := createDirect(t, format.AddressFamilyIPv4)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Create(path, format.AddressFamilyIPv4, format.ValueKindDirect, format.StructureKindNone, [16]byte{}, nil)
	if !isCode(err, format.CodeNameExists) {
		t.Fatalf("second create err = %v, want CodeNameExists", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("refused create modified the existing database")
	}
}

// TestCreateRejectsInvalidKindCombinations verifies the creation.rs
// validate_kinds surface: structured databases must name a structure and
// membership/direct databases must not.
func TestCreateRejectsInvalidKindCombinations(t *testing.T) {
	cases := []struct {
		name      string
		kind      uint8
		structure uint8
	}{
		{"structured without structure", format.ValueKindStructured, format.StructureKindNone},
		{"direct with structure", format.ValueKindDirect, format.StructureKindNetworkEnrichmentV1},
		{"membership with structure", format.ValueKindMembership, format.StructureKindNetworkEnrichmentV1},
		{"unknown kind", 0xff, format.StructureKindNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "invalid.iprdb")
			_, err := Create(path, format.AddressFamilyIPv4, tc.kind, tc.structure, [16]byte{}, nil)
			if !isCode(err, format.CodeWrongStructureKind) {
				t.Fatalf("create err = %v, want CodeWrongStructureKind", err)
			}
			if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
				t.Fatalf("rejected create left a file behind (stat err %v)", statErr)
			}
		})
	}
}

// TestCreateMembershipAndStructuredLimits verifies the empty_meta limit
// fields: membership databases seed membership_id_limit 1 and structured
// databases seed both id limits 1 (Rust database_file.rs empty_meta).
func TestCreateMembershipAndStructuredLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "membership.iprdb")
	if _, err := Create(path, format.AddressFamilyIPv4, format.ValueKindMembership, format.StructureKindNone, [16]byte{}, nil); err != nil {
		t.Fatal(err)
	}
	r, err := reader.OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	if m := r.Meta(); m.MembershipIDLimit != 1 || m.StructureIDLimit != 0 {
		t.Fatalf("membership limits = (%d,%d), want (1,0)", m.MembershipIDLimit, m.StructureIDLimit)
	}
	r.Close()

	path = filepath.Join(t.TempDir(), "structured.iprdb")
	if _, err := Create(path, format.AddressFamilyIPv6, format.ValueKindStructured, format.StructureKindNetworkEnrichmentV1, [16]byte{}, nil); err != nil {
		t.Fatal(err)
	}
	r, err = reader.OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	if m := r.Meta(); m.MembershipIDLimit != 1 || m.StructureIDLimit != 1 {
		t.Fatalf("structured limits = (%d,%d), want (1,1)", m.MembershipIDLimit, m.StructureIDLimit)
	}
	if m := r.Meta(); m.ValueKind != format.ValueKindStructured || m.StructureKind != format.StructureKindNetworkEnrichmentV1 || m.AddressFamily != format.AddressFamilyIPv6 {
		t.Fatalf("structured meta identity = kind %d struct %d family %d", m.ValueKind, m.StructureKind, m.AddressFamily)
	}
	r.Close()
}

// TestCreateRemovesPartialFileOnFailure pins the Rust live_cleanup
// parity: when a creation step fails after the O_EXCL destination was
// created, the partial file must be removed so a retried Create is not
// permanently poisoned (mapping_create.go deferred cleanup).
func TestCreateRemovesPartialFileOnFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "poison.iprdb")

	// A failing namespace check runs under the lifetime lock after the
	// exclusive creation: the file exists, then must be removed.
	boom := func(clean string) error {
		return &format.Error{Code: format.CodeConflict, Detail: "probe namespace rejection"}
	}
	if _, err := Create(path, format.AddressFamilyIPv4, format.ValueKindDirect, format.StructureKindNone, [16]byte{}, boom); err == nil {
		t.Fatal("create with failing check succeeded, want error")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("partial file still present after failed create: %v", err)
	}

	// The retried create must succeed, proving the path is not poisoned.
	if _, err := Create(path, format.AddressFamilyIPv4, format.ValueKindDirect, format.StructureKindNone, [16]byte{}, nil); err != nil {
		t.Fatalf("retried create failed: %v", err)
	}
	r, err := reader.OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
}

// TestJoinErrorSurface pins the combined create failure record: the
// write error stays reachable through Unwrap while the text carries both
// the primary failure and the failed close (Rust cleanup absorbs and
// reports both sides of a failed create).
func TestJoinErrorSurface(t *testing.T) {
	primary := &format.Error{Code: format.CodeSourceFailed, Detail: "primary"}
	joined := joinError{text: primary.Error() + "; close failed: secondary", cause: primary}
	if joined.Error() != primary.Error()+"; close failed: secondary" {
		t.Fatalf("joined text = %q", joined.Error())
	}
	if !errors.Is(joined, primary) {
		t.Fatal("joinError does not unwrap to the primary cause")
	}
}
