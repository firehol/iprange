//go:build (linux || darwin || freebsd || windows) && (amd64 || arm64)

package handlers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	iprangedb "github.com/firehol/iprange/v4/go"
	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
)

func TestMetadataFollowsUnprotectedDatabase(t *testing.T) {
	installRealWorker(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "src.iprdb")
	tag, err := iprangedb.NewValueTag([]byte("asn"))
	if err != nil {
		t.Fatal(err)
	}
	old := setUmask(0)
	created, err := iprangedb.CreateLive(source, iprangedb.AddressFamilyIPv4, iprangedb.ValueKindDirect, iprangedb.StructureKindNone, tag, 2, nil, false)
	setUmask(old)
	if err != nil {
		t.Fatal(err)
	}
	if created.State != iprangedb.CreationStateCreated {
		t.Fatalf("state = %v", created.State)
	}
	writer, err := iprangedb.OpenLiveWriter(source, iprangedb.DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := writer.BeginDirect(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.SetMetadataJSON([]byte("meta")); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(dir, "meta.bin")
	st := rpc.NewSessionState()
	body := map[string]any{
		"source": map[string]any{"path": source, "mode": "live"},
		"delivery": map[string]any{
			"mode":               "file",
			"path":               destination,
			"publication_policy": "fail_if_exists",
			"max_output_bytes":   "1048576",
			"max_open_files":     8,
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if _, herr := DatabaseMetadataGet(st, raw); herr != nil {
		t.Fatalf("metadata: %s %s", herr.Code, herr.Message)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() == 0o600 {
		t.Fatal("metadata from an unprotected database was forced to mode 0600")
	}
}
