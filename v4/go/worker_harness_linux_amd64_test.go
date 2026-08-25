//go:build linux && amd64

package iprangedb

// Worker harness: builds the real cmd/iprange-v4-worker binary once
// per test run and installs it through the exported test-only
// candidate seam, so the facade tests route through the isolated
// worker exactly like production. A build failure stops the run: the
// routed tests must never fall back silently to the in-process
// machines.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/worker"
)

// workerHarnessDir is the per-run harness directory holding the built
// worker binary (workerHarnessDir/iprange-v4-worker).
var workerHarnessDir string

// TestMain builds the real worker binary once per test run and removes
// the harness directory afterwards (the facade routes through the
// worker client on linux/amd64).
func TestMain(m *testing.M) {
	directory, err := os.MkdirTemp("", "iprange-v4-worker-harness-")
	if err != nil {
		os.Stderr.WriteString("worker harness: " + err.Error() + "\n")
		os.Exit(1)
	}
	workerHarnessDir = directory
	code := 1
	if err := buildWorkerHarness(directory); err != nil {
		os.Stderr.WriteString("worker harness: " + err.Error() + "\n")
	} else {
		code = m.Run()
	}
	_ = os.RemoveAll(directory)
	os.Exit(code)
}

// buildWorkerHarness builds cmd/iprange-v4-worker into the harness
// directory (the same go -C module-root build the internal worker
// tests use).
func buildWorkerHarness(directory string) error {
	command := exec.Command("go", "-C", workerModuleRoot(), "build", "-o", filepath.Join(directory, "iprange-v4-worker"), "./cmd/iprange-v4-worker")
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("build worker: %v\n%s", err, output)
	}
	return nil
}

// workerModuleRoot locates the v4/go module root from the test
// working directory (the internal worker tests use the same walk).
func workerModuleRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		panic("worker harness getwd: " + err.Error())
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			panic("v4/go module root not found from " + dir)
		}
		dir = parent
	}
}

// installWorkerForTestPlatform installs the real worker binary as the
// spawn candidate source of one test (the exported test-only seam; a
// nil restore runs in t.Cleanup).
func installWorkerForTestPlatform(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { worker.SetWorkerCandidatesForTest(nil) })
	worker.SetWorkerCandidatesForTest(func() ([]string, error) {
		return []string{filepath.Join(workerHarnessDir, "iprange-v4-worker")}, nil
	})
}
