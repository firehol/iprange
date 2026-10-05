package handlers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	iprangedb "github.com/firehol/iprange/v4/go"
	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
	"golang.org/x/sys/unix"
)

func TestExportFollowsUnprotectedDatabase(t *testing.T) {
	installRealWorker(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "src.iprdb")
	tag, err := iprangedb.NewValueTag([]byte("asn"))
	if err != nil {
		t.Fatal(err)
	}
	old := unix.Umask(0)
	created, err := iprangedb.CreateLive(source, iprangedb.AddressFamilyIPv4, iprangedb.ValueKindDirect, iprangedb.StructureKindNone, tag, 2, nil, false)
	unix.Umask(old)
	if err != nil {
		t.Fatal(err)
	}
	if created.State != iprangedb.CreationStateCreated {
		t.Fatalf("state = %v", created.State)
	}
	destination := filepath.Join(dir, "out.ranges")
	st := rpc.NewSessionState()
	body := map[string]any{
		"source":             map[string]any{"path": source, "mode": "live"},
		"view":               map[string]any{"kind": "direct"},
		"format":             "ranges",
		"destination":        destination,
		"publication_policy": "fail_if_exists",
		"result_budget":      map[string]any{"max_rows": "100", "max_output_bytes": "1048576", "max_open_files": 8},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if _, herr := Export(st, raw); herr != nil {
		t.Fatalf("export: %s %s", herr.Code, herr.Message)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() == 0o600 {
		t.Fatal("export from an unprotected database was forced to mode 0600")
	}
}
