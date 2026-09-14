//go:build (linux || darwin || freebsd || windows) && (amd64 || arm64)

package handlers

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
	"github.com/firehol/iprange/v4/go/internal/worker"
)

// The worker drive seam this file drives lives in internal/worker/client.go,
// whose build constraint is exactly the line above: on every other GOOS that
// package does not compile the seam, so worker.SetWorkerCandidatesForTest
// does not exist and this test could not be built there even though the rest
// of the handlers suite can. Removal criterion: change this constraint
// together with internal/worker/client.go:1 when the worker seam gains
// another platform, so the two never disagree about where the worker exists.

// buildExportWorker compiles the real cmd/iprange-v4-worker once per
// test run so the export identity inspection routes through the
// isolated worker exactly like production (the root-package harness
// precedent).
var (
	exportWorkerOnce    sync.Once
	exportWorkerCleanup sync.Once
	exportWorkerPath    string
	exportWorkerErr     error
)

func buildExportWorker() string {
	exportWorkerOnce.Do(func() {
		dir, err := os.MkdirTemp("", "iprange-handlers-worker-")
		if err != nil {
			exportWorkerErr = err
			return
		}
		// Windows exec launches only the image-mapped suffix; the worker
		// must be built under the name the launcher will actually find.
		exportWorkerName := "iprange-v4-worker"
		if runtime.GOOS == "windows" {
			exportWorkerName += ".exe"
		}
		exportWorkerPath = filepath.Join(dir, exportWorkerName)
		root, err := goModuleRoot()
		if err != nil {
			exportWorkerErr = err
			return
		}
		cmd := exec.Command("go", "-C", root, "build", "-o", exportWorkerPath, "./cmd/iprange-v4-worker")
		if output, err := cmd.CombinedOutput(); err != nil {
			exportWorkerErr = fmt.Errorf("build worker: %v\n%s", err, output)
		}
	})
	return exportWorkerPath
}

func goModuleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("v4/go module root not found from %s", dir)
		}
		dir = parent
	}
}

func installRealWorker(t *testing.T) {
	t.Helper()
	path := buildExportWorker()
	if exportWorkerErr != nil {
		t.Fatal(exportWorkerErr)
	}
	t.Cleanup(func() {
		worker.SetWorkerCandidatesForTest(nil)
		exportWorkerCleanup.Do(func() { _ = os.RemoveAll(filepath.Dir(exportWorkerPath)) })
	})
	worker.SetWorkerCandidatesForTest(func() ([]string, error) { return []string{path}, nil })
}

func TestExportDistinctDestinationStillWorks(t *testing.T) {
	installRealWorker(t)
	dir := t.TempDir()
	source := newImmutableFeed(t, dir, "src.db", nil)
	before, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(dir, "export.txt")
	st := rpc.NewSessionState()
	result, herr := Export(st, exportRequest(t, source, destination, "fail_if_exists"))
	if herr != nil {
		t.Fatalf("distinct-destination export refused: %v", herr)
	}
	facts, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("result = %T", result)
	}
	if facts["path"] != destination || facts["rows"] != "1" {
		t.Fatalf("facts = %v", facts)
	}
	content, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "1.1.1.1\n" {
		t.Fatalf("export content %q, want 1.1.1.1\n", content)
	}
	after, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("source changed after a distinct-destination export")
	}
}
