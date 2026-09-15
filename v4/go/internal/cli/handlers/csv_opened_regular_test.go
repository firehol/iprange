package handlers

import (
	"errors"
	"os"
	"testing"
)

// Platform-neutral pins of the caller-input opens in this package.
//
// Each owner must judge the handle it opened, not the path it statted, and
// the shared sentinel errOpenedNotRegular is how it reports that judgment.
// The null device is the node that proves the arm exists on every platform:
// it is openable for reading and is never a regular file, so an owner that
// lost its check hands the caller a handle instead of the sentinel. A
// directory cannot be used for this on Windows, where the read open of a
// directory may fail in CreateFileW itself (the caller's own path check
// still refuses it, which is why opened_guard_test.go pins the directory
// only for the refusal class).
//
// Before these pins, the Windows arm of the direct-CSV owner
// (csv_open_windows.go) was exercised by no test on any platform: its only
// sibling test file is POSIX-tagged, so it was not even part of the Windows
// test build. These assertions compile and run for GOOS=windows, and the
// same judgment deletion that the POSIX FIFO pins catch on Linux turns them
// red on both platforms through one sentinel.

// The direct-CSV owner refuses a non-regular handle it actually opened with
// the shared sentinel, whichever platform arm produced the open.
func TestOpenDirectCsvNoBlockRefusesNonRegularOpenedHandle(t *testing.T) {
	var err error
	runOpenedCall(t, "openDirectCsvNoBlock(os.DevNull)", func() {
		file, openErr := openDirectCsvNoBlock(os.DevNull)
		if file != nil {
			_ = file.Close()
		}
		err = openErr
	})
	if !errors.Is(err, errOpenedNotRegular) {
		t.Fatalf("openDirectCsvNoBlock(%s) = %v, want %v: the CSV owner no longer judges the handle it opened",
			os.DevNull, err, errOpenedNotRegular)
	}
}

// The metadata-source owner refuses the same node with the same sentinel.
// opened_guard_test.go pins its directory behaviour, but a Windows
// directory open can fail before the judgment is reached, so only this node
// proves the check itself on that platform.
func TestOpenMetadataSourceNoBlockRefusesNonRegularOpenedHandle(t *testing.T) {
	file, err := openMetadataSourceNoBlock(os.DevNull)
	if file != nil {
		_ = file.Close()
	}
	if !errors.Is(err, errOpenedNotRegular) {
		t.Fatalf("openMetadataSourceNoBlock(%s) = %v, want %v: the metadata owner no longer judges the handle it opened",
			os.DevNull, err, errOpenedNotRegular)
	}
}

// The direct-CSV arm's refusal of a node that is already visibly
// non-regular keeps the arm's own class and message, off the POSIX surface
// (the standing-FIFO refusal is pinned with its message in the POSIX-tagged
// csv_fifo_test.go).
func TestOpenDirectCsvNonRegularNodeKeepsArmClass(t *testing.T) {
	source, herr := openDirectCsv(os.DevNull, 1<<20, false)
	if source != nil {
		t.Fatal("openDirectCsv returned a source with a refusal")
	}
	if herr == nil || herr.Code != "invalid_path" {
		t.Fatalf("openDirectCsv(%s) = %+v, want invalid_path", os.DevNull, herr)
	}
	if want := "direct CSV input is not a regular file: " + os.DevNull; herr.Message != want {
		t.Fatalf("openDirectCsv refusal message = %q, want %q", herr.Message, want)
	}
}
