//go:build !linux

package exactv4

import (
	"errors"
	"io"
	"math"
	"os"
)

func readFilePageAtStatus(
	file *os.File,
	_ int,
	offset uint64,
	page *[PageSize]byte,
) pageSourceStatus {
	initialOffset := offset
	expected := len(page)
	actual := 0
	remaining := page[:]
	for len(remaining) != 0 {
		if offset > math.MaxInt64 {
			return pageSourceStatus{code: pageSourceErrOffsetOverflow}
		}
		count, err := file.ReadAt(remaining, int64(offset))
		if count != 0 {
			actual += count
			next, ok := checkedAdd(offset, uint64(count))
			if !ok {
				return pageSourceStatus{code: pageSourceErrOffsetOverflow}
			}
			offset = next
			remaining = remaining[count:]
		}
		if err != nil && !errors.Is(err, io.EOF) {
			evidence := pageIOEvidenceFromError(err)
			return pageSourceStatus{
				code:      pageSourceErrIO,
				ioKind:    evidence.kind,
				rawOSCode: evidence.rawOSCode,
				hasRaw:    evidence.hasRawOSCode,
			}
		}
		if count == 0 {
			return pageSourceStatus{
				code:     pageSourceErrShortRead,
				offset:   initialOffset,
				expected: expected,
				actual:   actual,
			}
		}
	}
	return pageSourceStatus{}
}
