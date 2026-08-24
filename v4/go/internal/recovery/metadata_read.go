package recovery

// Recovery metadata read (Rust recovery/metadata.rs): the opaque
// metadata chain of one recovery source is examined page by page
// through the page-ownership set, every chunk feeds the zlib decoder
// as its bytes are demanded, and the decompressed payload is returned
// only when the exact lengths prove. A page refusal stops the walk
// with its own envelope and reports no metadata; a decoder failure
// follows the Rust feed/finish page attribution.

import (
	"compress/zlib"
	"errors"
	"io"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/validation"
)

// metadataInflateOverhead is the fixed heap charge of the recovery
// metadata inflater (Rust metadata.rs DEFLATE_HEAP_OVERHEAD parity: the
// pinned miniz workspace the recovery budget reserves for the reader).
const metadataInflateOverhead = 512 * 1024

// readMetadata reads the metadata chain of one recovery source (Rust
// recovery metadata::read: an absent root is no metadata; the finished
// outcome folds the examined chunks into the accepted or rejected
// class; an incomplete chain reports no metadata without failing the
// analysis).
func readMetadata(m *mapping.Mapping, meta format.Meta, pages *pageSet, maxHeapBytes uint64, check func() error, rep *reporter) ([]byte, error) {
	return readMetadataRetained(m, meta, pages, 0, maxHeapBytes, check, rep)
}

// readMetadataRetained reads the metadata chain with one extra
// retained-heap charge (Rust recovery metadata::read over the indirect
// tables retention): the tables bytes are reserved before the payout
// buffer and the inflater overhead.
func readMetadataRetained(m *mapping.Mapping, meta format.Meta, pages *pageSet, retained uint64, maxHeapBytes uint64, check func() error, rep *reporter) ([]byte, error) {
	if meta.MetadataRoot == 0 {
		return nil, nil
	}
	output, err := metadataOutputBuffer(meta, pages, retained, maxHeapBytes)
	if err != nil {
		return nil, err
	}
	complete, err := scanMetadata(m, meta, pages, check, rep, output)
	if err != nil {
		return nil, err
	}
	if err := rep.metadataFinished(complete); err != nil {
		return nil, err
	}
	if !complete {
		return nil, nil
	}
	return output, nil
}

// metadataOutputBuffer sizes the decompressed metadata output against
// the recovery heap (Rust output_buffer: the page-set retention and
// the inflater overhead reserve the fixed part, and the declared
// uncompressed length must fit the remainder).
func metadataOutputBuffer(meta format.Meta, pages *pageSet, retained uint64, maxHeapBytes uint64) ([]byte, error) {
	outputLen := meta.MetadataUncompressed
	if outputLen > uint64(maxInt) {
		return nil, budgetError("recovery metadata output")
	}
	fixed, ok := checkedAdd(pages.retainedBytes(), retained)
	if !ok {
		return nil, overflowError("recovery metadata heap")
	}
	fixed, ok = checkedAdd(fixed, metadataInflateOverhead)
	if !ok {
		return nil, overflowError("recovery metadata heap")
	}
	available, ok := checkedSub(maxHeapBytes, fixed)
	if !ok {
		return nil, budgetError("recovery metadata output")
	}
	if outputLen > available {
		return nil, budgetError("recovery metadata output")
	}
	// Go make allocates the exact length; the Rust reserved-capacity
	// recheck is the length bound proven above.
	return make([]byte, int(outputLen)), nil
}

// scanMetadata drives the zlib decoder over the chain source (Rust
// scan + Inflater parity): a page refusal stops the walk with its own
// envelope, the decoder failures follow the Rust feed/finish page
// attribution, and the success fact requires the exact stream end.
func scanMetadata(m *mapping.Mapping, meta format.Meta, pages *pageSet, check func() error, rep *reporter, output []byte) (bool, error) {
	source := &metadataChainSource{
		rep:       rep,
		m:         m,
		meta:      meta,
		pages:     pages,
		check:     check,
		next:      meta.MetadataRoot,
		remaining: meta.MetadataCompressed,
	}
	decoder, err := zlib.NewReader(source)
	if err != nil {
		if source.aborted {
			return false, nil
		}
		var full *format.Error
		if errors.As(err, &full) {
			return false, full
		}
		return false, metadataFeedFinding(rep, source)
	}
	defer decoder.Close()
	written, err := io.ReadFull(decoder, output)
	if source.aborted {
		return false, nil
	}
	if err != nil {
		var full *format.Error
		if errors.As(err, &full) {
			return false, full
		}
		if errors.Is(err, io.EOF) {
			// The stream ended cleanly before the declared output: the
			// trailing bytes of the same chunk are the feed finding on
			// that page, a boundary ending probes the chain.
			if len(source.view) != 0 {
				return false, metadataFeedFinding(rep, source)
			}
			if _, probeErr := source.ReadByte(); probeErr == nil {
				if source.aborted {
					return false, nil
				}
				return false, metadataFeedFinding(rep, source)
			}
			if source.aborted {
				return false, nil
			}
			return false, metadataFinishFinding(rep)
		}
		if errors.Is(err, io.ErrUnexpectedEOF) {
			// The chain ran out with the stream incomplete (Rust
			// finish): no page.
			return false, metadataFinishFinding(rep)
		}
		// Decoder failures (checksum, corruption) while a page feed
		// was in progress: the finding is on the page being fed.
		return false, metadataFeedFinding(rep, source)
	}
	if written != len(output) {
		return false, metadataFinishFinding(rep)
	}
	// The declared output is exactly full: prove the stream ends here.
	var probe [1]byte
	n, probeErr := decoder.Read(probe[:])
	if source.aborted {
		return false, nil
	}
	if probeErr == nil || n != 0 {
		// The stream produces more output than declared: the page that
		// carried the overflow bytes reports the finding.
		return false, metadataFeedFinding(rep, source)
	}
	if !errors.Is(probeErr, io.EOF) {
		var full *format.Error
		if errors.As(probeErr, &full) {
			return false, full
		}
		return false, metadataFinishFinding(rep)
	}
	// Clean stream end: prove the chain carries no trailing bytes.
	if _, err := source.ReadByte(); err == nil {
		if source.aborted {
			return false, nil
		}
		return false, metadataFeedFinding(rep, source)
	}
	if source.aborted {
		return false, nil
	}
	return true, nil
}

// metadataChainSource serves the declared compressed stream of one
// metadata chain through the mapped chunk views (Rust recovery
// metadata chain): every page is checked, claimed, and parsed before
// any of its payload bytes are served, in the exact Rust loop order.
// The chain advances only after the whole chunk was served, exactly
// like the Rust feed-then-advance iteration.
type metadataChainSource struct {
	rep        *reporter
	m          *mapping.Mapping
	meta       format.Meta
	pages      *pageSet
	check      func() error
	pageNumber uint32 // page of the current view
	next       uint32 // next chain page to visit; 0 ends the chain
	view       []byte // unserved payload of the current chunk
	chunkLen   uint16 // payload length of the current chunk
	offset     uint64 // expected logical stream offset of the next page
	remaining  uint64 // declared compressed bytes still to serve
	chain      [format.MaxMetadataChainPages]uint32
	pagesSeen  uint64
	aborted    bool // a page refusal stopped the chain
}

// Read implements io.Reader over the chunk views (the zlib decoder
// must not buffer the source: ReadByte keeps the per-page visit order
// and the byte accounting exact).
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

// ReadByte serves one stream byte, visiting and validating the next
// chain page when the current view is exhausted.
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
	if len(s.view) == 0 {
		// The whole chunk was served: forward the chain exactly like
		// the Rust advance after the feed.
		if err := s.advance(); err != nil {
			return 0, err
		}
	}
	return b, nil
}

// visitNext examines and claims the next chain page (Rust claim_page +
// load_chunk: the chain depth cap, the page bounds, the ownership
// proof, the checked page access, and the exact chunk body; every
// refusal streams its envelope and stops the chain).
func (s *metadataChainSource) visitNext() error {
	if err := live.Checkpoint(s.check); err != nil {
		return err
	}
	if err := s.rep.metadataChunkExamined(); err != nil {
		return err
	}
	pageNumber := s.next
	if s.pagesSeen == format.MaxMetadataChainPages {
		if err := s.rep.emitPageUnknown(validation.ReasonMetadataInvalid, validation.ObjectMetadata, nil); err != nil {
			return err
		}
		s.aborted = true
		return nil
	}
	if pageNumber < 2 || uint64(pageNumber) >= s.meta.PageCount {
		if err := s.rep.emitPageUnknown(validation.ReasonPageOutOfBounds, validation.ObjectMetadata, &pageNumber); err != nil {
			return err
		}
		s.aborted = true
		return nil
	}
	inserted, err := s.pages.insert(pageNumber)
	if err != nil {
		return err
	}
	if !inserted {
		reason := validation.ReasonPageAlias
		if containsPage(s.chain[:s.pagesSeen], pageNumber) {
			reason = validation.ReasonTreeCycle
		}
		if err := s.rep.emitPageUnknown(reason, validation.ObjectMetadata, &pageNumber); err != nil {
			return err
		}
		s.aborted = true
		return nil
	}
	s.chain[s.pagesSeen] = pageNumber
	s.pagesSeen++
	page, problem := checkedPage(s.m, pageNumber, s.meta.PageCount)
	if problem != nil {
		return s.rejectPage(pageNumber, problem.reason, problem.ioUnreadable)
	}
	// The Rust parse_page header gates (require_page_header: common
	// identity, metadata kind, and born transaction) run before the
	// chunk body proof; every refusal folds to the
	// metadata-invalid class exactly like the Rust parse_page Err arm.
	if !format.PageCommonValid(page) || !format.PageBornValid(page, s.meta.TxnID) ||
		!format.PageKindValid(page, byte(format.PageTypeMetadataChunk), 0) {
		return s.rejectPage(pageNumber, validation.ReasonMetadataInvalid, false)
	}
	chunk, ok := format.MetadataChunkFields(page, pageNumber, s.meta.PageCount, s.offset, s.remaining)
	if !ok || !format.MetadataChunkTailZero(page, int(chunk.ChunkLen)) {
		return s.rejectPage(pageNumber, validation.ReasonMetadataInvalid, false)
	}
	if err := s.rep.pageAccepted(); err != nil {
		return err
	}
	s.pageNumber = pageNumber
	s.next = chunk.Next
	s.chunkLen = chunk.ChunkLen
	s.view = chunk.Data
	return nil
}

// rejectPage counts and streams one refused chain page (Rust
// reject_page: the rejected page class then the reason envelope).
func (s *metadataChainSource) rejectPage(pageNumber uint32, reason validation.ValidationReason, ioUnreadable bool) error {
	if err := s.rep.pageRejected(ioUnreadable); err != nil {
		return err
	}
	if err := s.rep.emitPageUnknown(reason, validation.ObjectMetadata, &pageNumber); err != nil {
		return err
	}
	s.aborted = true
	return nil
}

// advance forwards the chain after one fully served chunk (Rust
// Chain::advance: the checked logical offset and the remaining
// length). The page attribution stays on the chunk whose bytes were
// served until the next visit, exactly like the Rust feed that has not
// yet stepped.
func (s *metadataChainSource) advance() error {
	length := uint64(s.chunkLen)
	offset, ok := checkedAdd(s.offset, length)
	if !ok {
		return overflowError("recovery metadata offset")
	}
	s.offset = offset
	s.remaining -= length
	return nil
}

// metadataFeedFinding streams the MetadataInvalid finding of the page
// being fed when the decoder failed (Rust recovery scan feed arm).
func metadataFeedFinding(rep *reporter, source *metadataChainSource) error {
	pageNumber := source.pageNumber
	return rep.emitPageUnknown(validation.ReasonMetadataInvalid, validation.ObjectMetadata, &pageNumber)
}

// metadataFinishFinding streams the MetadataInvalid finding without a
// page (Rust recovery scan finish arm).
func metadataFinishFinding(rep *reporter) error {
	return rep.emitPageUnknown(validation.ReasonMetadataInvalid, validation.ObjectMetadata, nil)
}
