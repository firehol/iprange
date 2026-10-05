package fileio

import (
	"os"
	"path/filepath"
	"testing"

	iprangedb "github.com/firehol/iprange/v4/go"
	"golang.org/x/sys/unix"
)

func TestExportWriterFollowsSourceDatabase(t *testing.T) {
	dir := t.TempDir()
	tag, err := iprangedb.NewValueTag([]byte("asn"))
	if err != nil {
		t.Fatal(err)
	}
	old := unix.Umask(0)
	unprotected := filepath.Join(dir, "plain.iprdb")
	created, err := iprangedb.CreateLive(unprotected, iprangedb.AddressFamilyIPv4, iprangedb.ValueKindDirect, iprangedb.StructureKindNone, tag, 2, nil, false)
	if err != nil {
		unix.Umask(old)
		t.Fatal(err)
	}
	if created.State != iprangedb.CreationStateCreated {
		unix.Umask(old)
		t.Fatalf("state = %v", created.State)
	}
	protected := filepath.Join(dir, "protected.iprdb")
	created, err = iprangedb.CreateLive(protected, iprangedb.AddressFamilyIPv4, iprangedb.ValueKindDirect, iprangedb.StructureKindNone, tag, 2, nil, true)
	unix.Umask(old)
	if err != nil {
		t.Fatal(err)
	}
	if created.State != iprangedb.CreationStateCreated {
		t.Fatalf("state = %v", created.State)
	}
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
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("protected source mode = %o, want 0600", info.Mode().Perm())
	}
}
