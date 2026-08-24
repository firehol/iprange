package validation

// Metadata chain validation (Rust validation/metadata.rs): the opaque
// metadata chain is walked page by page through the graph claims, every
// chunk is validated and fed to the zlib decoder in the same visit, and
// the declared lengths are proved exactly. The compressed stream is never
// accumulated in owned memory: the decoder consumes the mapped chunk
// views directly (Rust Inflater parity over the Go zlib reader).

import (
	"compress/zlib"
	"errors"
	"io"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// inflaterOverhead is the reserved heap beyond the declared uncompressed
// length (Rust validation/metadata.rs INFLATER_OVERHEAD).
const inflaterOverhead = 64 * 1024

// validateMetadata runs the metadata validator (Rust metadata::validate):
// the heap reservation, the exact-output allocation, the chain walk, and
// the release on every successful path.
func validateMetadata(ctx *context) error {
	if ctx.meta.MetadataRoot == 0 {
		return nil
	}
	outputLen := ctx.meta.MetadataUncompressed
	if outputLen > uint64(^uint(0)>>1) {
		return &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "validation metadata output"}
	}
	retained := outputLen + inflaterOverhead
	if retained < outputLen {
		return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "validation metadata heap"}
	}
	if err := ctx.reserveHeap(retained, "validation metadata output"); err != nil {
		return err
	}
	output := make([]byte, int(outputLen))
	result := validateMetadataChain(ctx, output)
	ctx.releaseHeap(retained)
	return result
}

// metadataChainSource serves the declared compressed stream of one
// metadata chain through the mapped chunk views (Rust consume_page +
// Inflater feed parity): every page is visited, validated, and fed before
// any of its payload bytes are served. The walk stops with aborted on the
// first page refusal or the chain-depth cap, mirroring the Rust chain
// return without a decoder finish (so the zlib finding is not emitted on
// top of the refusal).
type metadataChainSource struct {
	ctx        *context
	pageNumber uint32 // page of the current view
	next       uint32 // next chain page to visit; 0 ends the chain
	view       []byte // unread payload of the current chunk
	remaining  uint64 // declared compressed bytes still to serve
	offset     uint64 // expected logical stream offset of the next page
	pages      uint64 // chain pages visited
	path       [format.MaxMetadataChainPages]uint32
	aborted    bool // a refusal or the depth cap stopped the chain
}

// Read implements io.Reader over the chunk views.
func (s *metadataChainSource) Read(p []byte) (int, error) {
	delivered := 0
	for delivered < len(p) {
		b, err := s.ReadByte()
		if err != nil {
			return delivered, err
		}
		p[delivered] = b
		delivered++
	}
	return delivered, nil
}

// ReadByte serves one stream byte, visiting and validating the next chain
// page when the current view is exhausted (flate.Reader contract: the
// zlib decoder must not buffer the source, so the per-page visit order
// and the byte accounting stay exact).
func (s *metadataChainSource) ReadByte() (byte, error) {
	for len(s.view) == 0 {
		if s.remaining == 0 {
			return 0, io.EOF
		}
		if err := s.visitNext(); err != nil {
			return 0, err
		}
		if s.aborted {
			return 0, io.EOF
		}
	}
	b := s.view[0]
	s.view = s.view[1:]
	return b, nil
}

// visitNext visits and validates the next chain page and prepares its
// payload (Rust validate_chain checkpoint + read_graph_page + parse_page;
// the depth cap is the MetadataLengthInvalid class).
func (s *metadataChainSource) visitNext() error {
	if err := s.ctx.checkpoint(); err != nil {
		return err
	}
	if s.pages == format.MaxMetadataChainPages {
		if err := metadataLengthFinding(s.ctx, s.next); err != nil {
			return err
		}
		if err := s.ctx.markUntraversable(false); err != nil {
			return err
		}
		s.aborted = true
		return nil
	}
	if s.next == 0 {
		// A chain that runs out of pages before the declared stream is
		// unreachable through chunk_fields (a nonfinal chunk always
		// links); kept as the Rust read_graph_page(0) refusal arm.
		pageNumber := s.next
		if err := s.ctx.emit(ReasonPageOutOfBounds, ObjectMetadata, &pageNumber, nil, nil); err != nil {
			return err
		}
		if err := s.ctx.markUntraversable(false); err != nil {
			return err
		}
		s.aborted = true
		return nil
	}
	s.path[s.pages] = s.next
	page, err := s.ctx.readGraphPage(s.next, ObjectMetadata, s.path[:s.pages])
	if err != nil {
		return err
	}
	if page == nil {
		s.aborted = true
		return nil
	}
	chunk, ok, err := s.parseChunk(page)
	if err != nil {
		return err
	}
	if !ok {
		s.aborted = true
		return nil
	}
	s.pageNumber = s.next
	s.view = chunk.Data
	s.remaining -= uint64(chunk.ChunkLen)
	s.offset += uint64(chunk.ChunkLen)
	s.next = chunk.Next
	s.pages++
	return nil
}

// parseChunk mirrors Rust parse_page: the common header, the born
// transaction, the metadata identity, and the chunk fields; each header
// refusal is one finding and stops the chain, the reserved-tail finding
// keeps it, and the chunk bytes are validated before any byte is served.
func (s *metadataChainSource) parseChunk(page []byte) (format.MetadataChunk, bool, error) {
	if !format.PageCommonValid(page) {
		return format.MetadataChunk{}, false, s.pageStopped(s.next, ReasonPageHeaderInvalid)
	}
	if !format.PageBornValid(page, s.ctx.meta.TxnID) {
		return format.MetadataChunk{}, false, s.pageStopped(s.next, ReasonPageBornTxnInvalid)
	}
	if !format.PageKindValid(page, byte(format.PageTypeMetadataChunk), 0) {
		return format.MetadataChunk{}, false, s.pageStopped(s.next, ReasonPageTypeMismatch)
	}
	chunk, ok := format.MetadataChunkFields(page, s.next, s.ctx.meta.PageCount, s.offset, s.remaining)
	if !ok {
		return format.MetadataChunk{}, false, s.pageStopped(s.next, ReasonMetadataLengthInvalid)
	}
	if !format.MetadataChunkTailZero(page, int(chunk.ChunkLen)) {
		pageNumber := s.next
		if err := s.ctx.emit(ReasonPageReservedNonzero, ObjectMetadata, &pageNumber, nil, nil); err != nil {
			return format.MetadataChunk{}, false, err
		}
	}
	return chunk, true, nil
}

// pageStopped streams one metadata page finding and marks the chain
// untraversable (Rust parse_page refusals: page_finding + the chain
// consume_page mark).
func (s *metadataChainSource) pageStopped(pageNumber uint32, reason ValidationReason) error {
	if err := s.ctx.emit(reason, ObjectMetadata, &pageNumber, nil, nil); err != nil {
		return err
	}
	return s.ctx.markUntraversable(false)
}

// validateMetadataChain drives the zlib decoder over the validating
// source and classifies every terminal exactly like Rust (Rust
// validate_chain + finish_chain): decoder feed failures report the page
// that was being fed, a clean end before the declared chain end reports
// the first page past the stream, the finish-length failures report no
// page, a chain refusal produces no zlib finding at all, and after any
// decoder stop the remaining chain pages are still visited and
// validated with the decoder disabled (Rust feed_chunk keeps the chain
// walking).
func validateMetadataChain(ctx *context, output []byte) error {
	source := &metadataChainSource{
		ctx:       ctx,
		next:      ctx.meta.MetadataRoot,
		remaining: ctx.meta.MetadataCompressed,
	}
	decoder, err := zlib.NewReader(source)
	if err != nil {
		if source.aborted {
			return nil
		}
		var formatErr *format.Error
		if errors.As(err, &formatErr) {
			return formatErr
		}
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			// The chain ran out inside the zlib header: the Rust feed
			// consumes the short page and the finish reports the
			// incomplete stream without a page.
			return metadataZlibFindingNone(ctx)
		}
		return metadataZlibStop(ctx, source)
	}
	defer decoder.Close()
	written, err := io.ReadFull(decoder, output)
	if source.aborted {
		return nil
	}
	if err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			if source.remaining != 0 {
				// The stream ended cleanly before the declared chain
				// end: the first page carrying bytes after the end
				// reports the finding and the chain keeps walking
				// (Rust feed_chunk trailing-bytes arm).
				return metadataZlibTrailingFinding(ctx, source)
			}
			// The chain ran out with the stream incomplete (Rust
			// finish: written != output.len).
			return metadataZlibFindingNone(ctx)
		}
		if errors.Is(err, zlib.ErrChecksum) {
			return metadataZlibStop(ctx, source)
		}
		var formatErr *format.Error
		if errors.As(err, &formatErr) {
			return formatErr
		}
		return metadataZlibStop(ctx, source)
	}
	if written != len(output) {
		return metadataZlibFindingNone(ctx)
	}
	// The declared output is exactly full: prove the stream ends here.
	var probe [1]byte
	n, probeErr := decoder.Read(probe[:])
	if source.aborted {
		// A chain refusal during the probe stops without the zlib
		// finding (Rust read_graph_page return before the next feed).
		return nil
	}
	if probeErr == nil || n != 0 {
		if source.remaining != 0 {
			// The stream produces more output than declared while
			// chain pages remain: the finding is on the page being
			// fed and the chain keeps walking (Rust feed excess arm).
			return metadataZlibStop(ctx, source)
		}
		// The overflow surfaces in the finish with no page left (Rust
		// finish: written exceeds output.len).
		return metadataZlibFindingNone(ctx)
	}
	if !errors.Is(probeErr, io.EOF) {
		if errors.Is(probeErr, io.ErrUnexpectedEOF) {
			// The output filled but the stream did not complete (the
			// Rust finish incomplete/length arm: no page).
			return metadataZlibFindingNone(ctx)
		}
		if errors.Is(probeErr, zlib.ErrChecksum) {
			return metadataZlibStop(ctx, source)
		}
		if errors.Is(probeErr, zlib.ErrHeader) || errors.Is(probeErr, zlib.ErrDictionary) {
			return metadataZlibStop(ctx, source)
		}
		var formatErr *format.Error
		if errors.As(probeErr, &formatErr) {
			return formatErr
		}
		return metadataZlibStop(ctx, source)
	}
	// Clean stream end: prove every declared byte was consumed. A page
	// refusal during the probe stops without the finish findings, like
	// the Rust chain return before finish_chain.
	if _, trailingErr := source.ReadByte(); trailingErr == nil {
		return metadataZlibStop(ctx, source)
	}
	if source.aborted {
		return nil
	}
	if source.next != 0 {
		// The final chunk must close the chain (Rust finish_chain arm;
		// unreachable through chunk_fields, kept as the Rust mirror).
		return metadataLengthFinding(ctx, source.next)
	}
	return nil
}

// metadataZlibStop streams the zlib finding of the page that was fed
// when the decoder failed and then keeps the chain walking with the
// decoder disabled (Rust feed_chunk emits and continues): the finding
// always precedes the drain, and the drain always follows a stop.
func metadataZlibStop(ctx *context, source *metadataChainSource) error {
	if err := metadataZlibFinding(ctx, source); err != nil {
		return err
	}
	return drainMetadataChain(source)
}

// metadataZlibTrailingFinding probes one byte past a clean stream end
// and reports the trailing bytes on the page that carries them (Rust
// feed rejects the chunk that follows the stream end): the probe pulls
// the next page when the stream ended exactly at a chunk boundary, so
// the page attribution and the parse findings keep the consume_page
// order (parse first, feed finding second). The chain then keeps
// walking with the decoder disabled.
func metadataZlibTrailingFinding(ctx *context, source *metadataChainSource) error {
	if _, err := source.ReadByte(); err != nil {
		if source.aborted {
			return nil
		}
		return err
	}
	pageNumber := source.pageNumber
	if err := ctx.emit(ReasonMetadataZlibInvalid, ObjectMetadata, &pageNumber, nil, nil); err != nil {
		return err
	}
	return drainMetadataChain(source)
}

// drainMetadataChain continues the page-validation walk after the zlib
// decoder stopped (Rust feed_chunk emits the finding and keeps the chain
// walking with the decoder disabled): every remaining page is still
// visited and validated, and only the decode stops.
func drainMetadataChain(source *metadataChainSource) error {
	source.view = nil
	for !source.aborted && source.remaining != 0 {
		if err := source.visitNext(); err != nil {
			return err
		}
	}
	return nil
}

// metadataZlibFinding streams the MetadataZlibInvalid finding of the
// page that was being fed when the decoder failed (Rust feed_chunk).
func metadataZlibFinding(ctx *context, source *metadataChainSource) error {
	pageNumber := source.pageNumber
	return ctx.emit(ReasonMetadataZlibInvalid, ObjectMetadata, &pageNumber, nil, nil)
}

// metadataZlibFindingNone streams the MetadataZlibInvalid finding without
// a page (Rust finish_chain decoder.finish failure).
func metadataZlibFindingNone(ctx *context) error {
	return ctx.emit(ReasonMetadataZlibInvalid, ObjectMetadata, nil, nil, nil)
}

// metadataLengthFinding streams the MetadataLengthInvalid finding of one
// chain page (Rust length_finding).
func metadataLengthFinding(ctx *context, pageNumber uint32) error {
	return ctx.emit(ReasonMetadataLengthInvalid, ObjectMetadata, &pageNumber, nil, nil)
}
