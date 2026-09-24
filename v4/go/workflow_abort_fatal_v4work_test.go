//go:build v4work

package iprangedb

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const timestampAbortTimeout = 60 * time.Second

func TestTimestampMergeFatalBrandsWriterUnusable(t *testing.T) {
	requireLiveCreation(t)
	path := filepath.Join(t.TempDir(), "timestamp-abort.iprdb")
	if _, err := CreateLive(path, AddressFamilyIPv4, ValueKindDirect, StructureKindNone, ValueTagFirstSeen(), 4, nil); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timestampAbortTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestTimestampMergeFatalChild$")
	env := make([]string, 0, len(os.Environ())+3)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "IPRANGE_V4_TEST_") || strings.HasPrefix(kv, "IPRANGE_TS_ABORT_") {
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = append(env,
		"IPRANGE_TS_ABORT=1",
		"IPRANGE_TS_ABORT_PATH="+path,
		"IPRANGE_V4_TEST_FAIL_AT=timestamp.merge_first_seen_fatal",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child failed: %v\n%s", err, out)
	}
}

func TestTimestampMergeFatalChild(t *testing.T) {
	if os.Getenv("IPRANGE_TS_ABORT") != "1" {
		t.Skip("subprocess entry point")
	}
	time.AfterFunc(timestampAbortTimeout, func() { os.Exit(1) })
	w, err := OpenLiveWriter(os.Getenv("IPRANGE_TS_ABORT_PATH"), DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	refresh, err := w.BeginFirstSeenRefresh(1, NewCancellationToken())
	if err != nil {
		t.Fatal(err)
	}
	if err := refresh.AddRangesV4([]AddressRange4{{From: IPv4(1), To: IPv4(1)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := refresh.FinishInput(); err == nil {
		t.Fatal("FinishInput succeeded under the armed merge fault")
	}
	_, nextErr := w.BeginFirstSeenRefresh(2, nil)
	if nextErr == nil || !strings.Contains(nextErr.Error(), "writer is unusable") {
		t.Fatalf("next begin = %v, want writer is unusable", nextErr)
	}
	if _, err := w.Close(); err != nil {
		t.Fatalf("close after fatal abort: %v", err)
	}
}

var _ = errors.New
