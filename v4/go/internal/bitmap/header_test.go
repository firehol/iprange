// Header validation parity pins (Rust page_header::common_valid): the
// bitmap parse must reject a torn magic, nonzero flags, or a non-32-byte
// header size BEFORE any COW copy re-seals the page with a fresh
// checksum.

package bitmap

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

func TestHeaderProblemRejectsTornCommonHeader(t *testing.T) {
	page := make([]byte, format.PageSize)
	copy(page[format.HeaderMagic:], format.PageMagic[:])
	page[format.HeaderFlags] = 0
	format.PutU16(page[format.HeaderSizePos:], format.SlottedHeaderSize)
	format.PutU16(page[format.HeaderLower:], uint16(PageLower(1)))
	format.PutU16(page[format.HeaderUpper:], format.PageSize)
	format.PutU64(page[format.HeaderBorn:], 1)
	format.PutU16(page[format.HeaderLevel:], 1)
	page[format.HeaderType] = byte(format.PageTypeBitmapBranch)
	format.PutU32(page[format.HeaderAux:], uint32(KindFree))
	format.PutU16(page[format.HeaderCount:], 1)

	level := uint16(1)
	if err := headerProblem(page, 1, KindFree, &level); err != nil {
		t.Fatalf("valid bitmap page rejected: %v", err)
	}

	torn := append([]byte(nil), page...)
	torn[0] ^= 0xFF
	if err := headerProblem(torn, 1, KindFree, &level); err == nil {
		t.Fatal("torn magic accepted")
	}

	flags := append([]byte(nil), page...)
	flags[format.HeaderFlags] = 1
	if err := headerProblem(flags, 1, KindFree, &level); err == nil {
		t.Fatal("nonzero flags accepted")
	}

	size := append([]byte(nil), page...)
	format.PutU16(size[format.HeaderSizePos:], 24)
	if err := headerProblem(size, 1, KindFree, &level); err == nil {
		t.Fatal("non-32 header size accepted")
	}
}
