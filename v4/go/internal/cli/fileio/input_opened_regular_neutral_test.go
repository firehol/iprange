package fileio

import (
	"errors"
	"os"
	"testing"
)

// Platform-neutral pin of the caller-path input open: the owner must judge
// the handle it opened, and the null device is the node that proves the
// check exists on every platform arm. It is openable for reading and is
// never a regular file, so an input_open_windows.go or input_open_unix.go
// that lost its check hands the caller a readable handle instead of the
// shared sentinel. The directory pin in opened_guard_test.go cannot carry
// that duty on Windows, where the read open of a directory may fail in
// CreateFileW itself.
func TestOpenInputNoBlockRefusesNonRegularOpenedHandle(t *testing.T) {
	var err error
	runOpenedCall(t, "openInputNoBlock(os.DevNull)", func() {
		file, openErr := openInputNoBlock(os.DevNull)
		if file != nil {
			_ = file.Close()
		}
		err = openErr
	})
	if !errors.Is(err, errOpenedNotRegular) {
		t.Fatalf("openInputNoBlock(%s) = %v, want %v: the input owner no longer judges the handle it opened",
			os.DevNull, err, errOpenedNotRegular)
	}
}
