package fileio

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	iprangedb "github.com/firehol/iprange/v4/go"
)

func TestExportWriterFollowsSourceDatabase(t *testing.T) {
	dir := t.TempDir()
	tag, err := iprangedb.NewValueTag([]byte("asn"))
	if err != nil {
		t.Fatal(err)
	}
	old := setUmask(0)
	unprotected := filepath.Join(dir, "plain.iprdb")
	created, err := iprangedb.CreateLive(unprotected, iprangedb.AddressFamilyIPv4, iprangedb.ValueKindDirect, iprangedb.StructureKindNone, tag, 2, nil, false)
	if err != nil {
		setUmask(old)
		t.Fatal(err)
	}
	if created.State != iprangedb.CreationStateCreated {
		setUmask(old)
		t.Fatalf("state = %v", created.State)
	}
	protected := filepath.Join(dir, "protected.iprdb")
	created, err = iprangedb.CreateLive(protected, iprangedb.AddressFamilyIPv4, iprangedb.ValueKindDirect, iprangedb.StructureKindNone, tag, 2, nil, true)
	if err != nil {
		setUmask(old)
		t.Fatal(err)
	}
	if created.State != iprangedb.CreationStateCreated {
		setUmask(old)
		t.Fatalf("state = %v", created.State)
	}
	// A umask that would strip the owner-write bit distinguishes an
	// exact-0600 set from a plain 0600 create (0600 & ~0200 == 0400):
	// the protected output must stay exactly 0600 (Rust twin sets the
	// mode explicitly after create).
	setUmask(0o200)
	defer setUmask(old)
	budget := ExportBudget{MaxRows: 4, MaxOutputBytes: 1024, MaxOpenFiles: 1}
	plain := filepath.Join(dir, "plain.out")
	writer, herr := NewExportWriterFollowing(plain, unprotected, iprangedb.PolicyFailIfExists, budget)
	if herr != nil {
		t.Fatal(herr.Message)
	}
	if _, herr := writer.Finish(); herr != nil {
		t.Fatal(herr.Message)
	}
	info, err := os.Stat(plain)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() == 0o600 {
		t.Fatal("unprotected source forced mode 0600")
	}
	locked := filepath.Join(dir, "locked.out")
	writer, herr = NewExportWriterFollowing(locked, protected, iprangedb.PolicyFailIfExists, budget)
	if herr != nil {
		t.Fatal(herr.Message)
	}
	if _, herr := writer.Finish(); herr != nil {
		t.Fatal(herr.Message)
	}
	info, err = os.Stat(locked)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("protected source mode = %o, want 0600", info.Mode().Perm())
	}
}
