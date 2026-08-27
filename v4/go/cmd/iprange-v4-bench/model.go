// Bench database and budget model (Rust
// benches/update_ipsets/model.rs): one fresh temp directory per case with
// the canonical main and snapshot names, plus the exact transaction and
// snapshot budget shapes the Rust harness applies.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
)

var nextDirectory atomic.Uint64

type testDatabase struct {
	directory string
	main      string
}

func newTestDatabase(label string) (*testDatabase, error) {
	ordinal := nextDirectory.Add(1) - 1
	directory := filepath.Join(os.TempDir(), fmt.Sprintf("iprange-v4-bench-%s-%d-%d", label, os.Getpid(), ordinal))
	if err := os.Mkdir(directory, 0o700); err != nil {
		return nil, err
	}
	return &testDatabase{directory: directory, main: filepath.Join(directory, "live.v4")}, nil
}

func (d *testDatabase) snapshot() string { return filepath.Join(d.directory, "snapshot.v4") }

func (d *testDatabase) path(name string) string { return filepath.Join(d.directory, name) }

func (d *testDatabase) privateArtifacts() (uint64, error) {
	entries, err := os.ReadDir(d.directory)
	if err != nil {
		return 0, err
	}
	var count uint64
	for _, entry := range entries {
		name := entry.Name()
		if len(name) > 9 && name[:9] == ".iprange-" {
			count++
		}
	}
	return count, nil
}

func (d *testDatabase) cleanup() { _ = os.RemoveAll(d.directory) }

// transactionBudget mirrors Rust transaction_budget: the page grants
// scale with the record and feed counts, floored at 20k.
func transactionBudget(records, feeds int) pageBudgetShape {
	scale := uint64(records)*8 + uint64(feeds)*128
	if scale < 20_000 {
		scale = 20_000
	}
	return pageBudgetShape{
		MaxHeapBytes:    64 * 1024 * 1024,
		MaxPrivatePages: scale,
		MaxGrowthPages:  scale,
		MaxOpenFiles:    2,
	}
}

type pageBudgetShape struct {
	MaxHeapBytes    uint64
	MaxPrivatePages uint64
	MaxGrowthPages  uint64
	MaxOpenFiles    uint32
}

// snapshotBudget mirrors Rust snapshot_budget (heap, output pages,
// open files).
func snapshotBudget(records int) snapshotBudgetShape {
	pages := uint64(records) * 16
	if pages < 20_000 {
		pages = 20_000
	}
	return snapshotBudgetShape{
		MaxHeapBytes:   64 * 1024 * 1024,
		MaxOutputPages: pages,
		MaxOpenFiles:   3,
	}
}

type snapshotBudgetShape struct {
	MaxHeapBytes   uint64
	MaxOutputPages uint64
	MaxOpenFiles   uint32
}
