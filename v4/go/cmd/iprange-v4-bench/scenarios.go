// Scenario registry and shared result plumbing (Rust
// benches/update_ipsets/scenarios.rs): every scenario registers itself
// under its Rust name, and the shared helpers reproduce the exact
// output validation, live-info counting, commit/close assertions, reader
// work shaping, and point/cursor counting of the Rust harness.
package main

import (
	"fmt"
	"sort"

	"github.com/firehol/iprange/v4/go"
)

type scenarioFunc func(size int, auxiliary int) (*scenarioResult, error)

var scenarioRegistry = map[string]scenarioFunc{}

func registerScenario(name string, fn scenarioFunc) {
	if _, exists := scenarioRegistry[name]; exists {
		panic("duplicate benchmark scenario " + name)
	}
	scenarioRegistry[name] = fn
}

type scenarioResult struct {
	Name             string
	Size             int
	Auxiliary        int
	WorkUnits        uint64
	EmittedUnits     uint64
	RangeRecords     uint64
	Feeds            uint64
	Measurement      measurement
	File             fileSize
	PrivateArtifacts uint64
}

type immutableResultSpec struct {
	Name         string
	Size         int
	Auxiliary    int
	WorkUnits    uint64
	EmittedUnits uint64
}

// result mirrors Rust scenarios::result: validate the output, count the
// live-range and feed facts, and refuse any leftover private artifacts.
func result(name string, size, auxiliary int, workUnits uint64, db *testDatabase, measured measurement, filePath string) (*scenarioResult, error) {
	if err := validateOutput(filePath, filePath == db.main); err != nil {
		return nil, err
	}
	rangeRecords, feeds, err := liveInfo(db)
	if err != nil {
		return nil, err
	}
	privateArtifacts, err := db.privateArtifacts()
	if err != nil {
		return nil, err
	}
	if privateArtifacts != 0 {
		return nil, fmt.Errorf("%s left %d private temporary artifacts", name, privateArtifacts)
	}
	file, err := fileSizeOf(filePath)
	if err != nil {
		return nil, err
	}
	return &scenarioResult{
		Name:             name,
		Size:             size,
		Auxiliary:        auxiliary,
		WorkUnits:        workUnits,
		RangeRecords:     rangeRecords,
		Feeds:            feeds,
		Measurement:      measured,
		File:             file,
		PrivateArtifacts: privateArtifacts,
	}, nil
}

// immutableResult mirrors Rust scenarios::immutable_result for snapshot
// outputs: validate, count facts through an immutable reader, refuse
// leftover private artifacts.
func immutableResult(spec immutableResultSpec, db *testDatabase, measured measurement, filePath string) (*scenarioResult, error) {
	if err := validateOutput(filePath, false); err != nil {
		return nil, err
	}
	reader, err := iprangedb.OpenImmutable(filePath)
	if err != nil {
		return nil, err
	}
	info, err := reader.Info()
	if err != nil {
		return nil, err
	}
	_ = reader.Close()
	privateArtifacts, err := db.privateArtifacts()
	if err != nil {
		return nil, err
	}
	if privateArtifacts != 0 {
		return nil, fmt.Errorf("%s left %d private temporary artifacts", spec.Name, privateArtifacts)
	}
	file, err := fileSizeOf(filePath)
	if err != nil {
		return nil, err
	}
	return &scenarioResult{
		Name:             spec.Name,
		Size:             spec.Size,
		Auxiliary:        spec.Auxiliary,
		WorkUnits:        spec.WorkUnits,
		EmittedUnits:     spec.EmittedUnits,
		RangeRecords:     info.RangeRecordCount,
		Feeds:            info.ActiveFeedCount,
		Measurement:      measured,
		File:             file,
		PrivateArtifacts: privateArtifacts,
	}, nil
}

// validateOutput mirrors Rust scenarios::validate_output: the output
// database must validate clean under the heap-only budget.
func validateOutput(path string, live bool) error {
	mode := iprangedb.ValidationModeImmutableCurrent
	if live {
		mode = iprangedb.ValidationModeLiveCurrent
	}
	var firstFinding string
	result, failure := iprangedb.Validate(path, mode, iprangedb.HeapOnly(64*1024*1024, 2), nil, iprangedb.SinkFunc(func(f *iprangedb.ValidationFinding) (iprangedb.ValidationSinkControl, error) {
		if firstFinding == "" {
			firstFinding = fmt.Sprintf("reason=%d object=%v", f.Reason, f.Object)
		}
		return iprangedb.SinkContinue, nil
	}))
	if failure != nil {
		return fmt.Errorf("benchmark output validation failed: %v", failure)
	}
	if !result.Valid {
		return fmt.Errorf("benchmark output has %d validation findings (first: %s)", result.Progress.FindingCount, firstFinding)
	}
	return nil
}

// requireCommitted mirrors Rust scenarios::require_committed.
func requireCommitted(commit iprangedb.CommitResult, err error) error {
	if err != nil {
		return err
	}
	if commit.Status != iprangedb.CommitCommitted {
		return fmt.Errorf("commit did not complete: %+v", commit)
	}
	return nil
}

// closeWriter mirrors Rust scenarios::close_writer.
func closeWriter(w *iprangedb.LiveWriter) error {
	result, err := w.Close()
	if err != nil {
		return err
	}
	if result.Outcome != iprangedb.CloseOutcomeClosed {
		return fmt.Errorf("writer close did not complete: %+v", result)
	}
	return nil
}

// closeLiveReader mirrors Rust scenarios::close_reader.
func closeLiveReader(r *iprangedb.LiveReader) error {
	result, err := r.Close()
	if err != nil {
		return err
	}
	if result.Outcome != iprangedb.CloseOutcomeClosed {
		return fmt.Errorf("reader close did not complete: %+v", result)
	}
	return nil
}

// liveInfo mirrors Rust scenarios::live_info: ranges and active feeds
// through a live reader on the main file.
func liveInfo(db *testDatabase) (uint64, uint64, error) {
	reader, err := iprangedb.OpenLiveReader(db.main, nil)
	if err != nil {
		return 0, 0, err
	}
	info, err := reader.Info()
	if err != nil {
		return 0, 0, err
	}
	if err := closeLiveReader(reader); err != nil {
		return 0, 0, err
	}
	return info.RangeRecordCount, info.ActiveFeedCount, nil
}

// readerWork mirrors Rust scenarios::reader_work.
func readerWork(size int) (int, uint64, error) {
	if size <= 0 {
		return 0, 0, fmt.Errorf("reader benchmark size must be nonzero")
	}
	repetitions := (1_000_000 + size - 1) / size
	return repetitions, uint64(size) * uint64(repetitions), nil
}

// countPoints mirrors Rust scenarios::count_points (dispersed sweep at
// index*4).
func countPoints(size, repetitions int, present func(iprangedb.IPv4) (bool, error)) (uint64, error) {
	var hits uint64
	for range repetitions {
		for index := 0; index < size; index++ {
			found, err := present(iprangedb.IPv4(uint32(index) * 4))
			if err != nil {
				return 0, err
			}
			if found {
				hits++
			}
		}
	}
	return hits, nil
}

// countRandomPoints mirrors Rust scenarios::count_random_points over the
// dispersed point list.
func countRandomPoints(points []uint32, repetitions int, present func(iprangedb.IPv4) (bool, error)) (uint64, error) {
	var hits uint64
	for range repetitions {
		for _, address := range points {
			found, err := present(iprangedb.IPv4(address))
			if err != nil {
				return 0, err
			}
			if found {
				hits++
			}
		}
	}
	return hits, nil
}

// countCursor mirrors Rust scenarios::count_cursor: one cursor per
// repetition, advancing until the end.
func countCursor[C any](repetitions int, open func() (*C, error), advance func(*C) (bool, error)) (uint64, error) {
	var records uint64
	for range repetitions {
		cursor, err := open()
		if err != nil {
			return 0, err
		}
		for {
			more, err := advance(cursor)
			if err != nil {
				return 0, err
			}
			if !more {
				break
			}
			records++
		}
	}
	return records, nil
}

// toPageBudget converts the bench page budget shape onto the public
// writer budget (Rust TransactionBudget parity).
func toPageBudget(b pageBudgetShape) iprangedb.PageBudget {
	return iprangedb.PageBudget{
		MaxHeapBytes:    b.MaxHeapBytes,
		MaxPrivatePages: b.MaxPrivatePages,
		MaxGrowthPages:  b.MaxGrowthPages,
		MaxOpenFiles:    b.MaxOpenFiles,
	}
}

// immutableSnapshot mirrors Rust scenarios::immutable_snapshot: live
// source to the canonical snapshot path, failing if it exists, clean
// cleanup required.
func immutableSnapshot(db *testDatabase, records int) (string, error) {
	output := db.snapshot()
	snap, err := iprangedb.SnapshotTo(db.main, iprangedb.SnapshotSourceLive, output, iprangedb.PolicyFailIfExists, &iprangedb.SnapshotBudget{
		MaxHeapBytes:   64 * 1024 * 1024,
		MaxOutputPages: snapshotBudget(records).MaxOutputPages,
		MaxOpenFiles:   3,
	}, nil)
	if err != nil {
		return "", err
	}
	if snap.CleanupState() != iprangedb.CleanupStateClean {
		return "", fmt.Errorf("snapshot cleanup is incomplete: %+v", snap)
	}
	return output, nil
}

// dispatchScenario runs the named scenario in-process (the child of the
// case/matrix drivers).
func dispatchScenario(name string, size, auxiliary int) (*scenarioResult, error) {
	if size <= 0 {
		return nil, fmt.Errorf("size must be positive")
	}
	fn, ok := scenarioRegistry[name]
	if !ok {
		return nil, fmt.Errorf("unknown scenario %q", name)
	}
	return fn(size, auxiliary)
}

func sortedScenarioNames() []string {
	names := make([]string, 0, len(scenarioRegistry))
	for name := range scenarioRegistry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
