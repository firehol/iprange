//go:build windows

package mapping

import (
	"golang.org/x/sys/windows"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// syncFile forces the file's data to stable storage (FlushFileBuffers),
// mirroring Rust mapping.rs sync_file through std on Windows
// (file.sync_all -> FlushFileBuffers).
func syncFile(fd int) error {
	if err := windows.FlushFileBuffers(windows.Handle(fd)); err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "flush file buffers: " + err.Error()}
	}
	return nil
}
