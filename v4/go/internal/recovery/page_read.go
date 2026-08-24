package recovery

// Shared mapped-page access for explicit recovery (Rust
// recovery/page_read.rs): one checked page access classifies the
// mapping failure as the I/O-unreadable class and the checksum
// failure as the CRC class, exactly like the Rust authority.

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/validation"
)

// pageReadProblem is the classified refusal of one checked page access
// (Rust page_read::Problem).
type pageReadProblem struct {
	reason       validation.ValidationReason
	ioUnreadable bool
}

// checkedPage reads one page of the mapped source and verifies its
// checksum (Rust page_read::checked: the mapping refusal is the
// I/O-unreadable class, the checksum refusal the CRC class).
func checkedPage(m *mapping.Mapping, pageNumber uint32, pageCount uint64) ([]byte, *pageReadProblem) {
	page, err := m.Page(pageNumber)
	if err != nil {
		return nil, &pageReadProblem{reason: validation.ReasonIoError, ioUnreadable: true}
	}
	if !format.PageChecksumValid(page) {
		return nil, &pageReadProblem{reason: validation.ReasonPageCrcMismatch, ioUnreadable: false}
	}
	return page, nil
}
