package iprangedb

import (
	"io"
	"os"
	"sort"
)

// SortedStream is an in-memory sorted, coalesced stream of desired records.
type SortedStream[K ipKey[K]] struct {
	records []DesiredRecord[K]
	pos     int
}

// Clone returns a copy of the stream at the current position.
func (s *SortedStream[K]) Clone() *SortedStream[K] {
	records := make([]DesiredRecord[K], len(s.records))
	copy(records, s.records)
	return &SortedStream[K]{records: records, pos: s.pos}
}

// FromUnsorted builds a sorted, normalized, coalesced stream from unsorted records.
// Overlapping input is split into disjoint segments with last-wins semantics for
// different scope_ids; same-scope overlaps are merged.
func FromUnsorted[K ipKey[K]](records []DesiredRecord[K]) *SortedStream[K] {
	sort.Slice(records, func(i, j int) bool {
		return records[i].From.cmp(records[j].From) < 0
	})
	normalized := normalizeChunk(records)
	return &SortedStream[K]{records: normalized}
}

// sweepEvent is a sweep-line event for normalizeChunk.
type sweepEvent struct {
	pos     Uint128
	isStart bool
	idx     int
}

// normalizeChunk resolves overlaps in a sorted chunk into disjoint segments
// using an O(n log n) sweep line with u128 events. Last-wins for different
// scope_ids (later records overwrite earlier); merge for same scope_ids.
// Correctly handles tails and max-address boundaries (checkedInc returns
// false at family_max → no end event, record covers to end).
func normalizeChunk[K ipKey[K]](sorted []DesiredRecord[K]) []DesiredRecord[K] {
	if len(sorted) <= 1 {
		return sorted
	}

	// Fast path: check if already disjoint.
	disjoint := true
	for i := 1; i < len(sorted); i++ {
		if sorted[i].From.cmp(sorted[i-1].To) <= 0 {
			disjoint = false
			break
		}
	}
	if disjoint {
		return coalesceAdjacent(sorted)
	}

	// Sweep line: collect (position, is_start, record_index) events.
	events := make([]sweepEvent, 0, len(sorted)*2)
	for i, r := range sorted {
		events = append(events, sweepEvent{pos: r.From.toU128(), isStart: true, idx: i})
		if after, ok := r.To.checkedInc(); ok {
			events = append(events, sweepEvent{pos: after.toU128(), isStart: false, idx: i})
		} else {
			// To is family_max: synthetic end at u128 max so the final
			// segment is processed (fixes tail loss at max address).
			events = append(events, sweepEvent{pos: Uint128{Hi: maxUint64, Lo: maxUint64}, isStart: false, idx: i})
		}
	}
	// Sort by position, then ends before starts at the same position.
	sort.Slice(events, func(i, j int) bool {
		c := cmpU128(events[i].pos, events[j].pos)
		if c != 0 {
			return c < 0
		}
		return !events[i].isStart && events[j].isStart
	})

	// Sweep: maintain active record indices. At each segment, last-wins = highest idx.
	var active []int
	var zero K
	var out []DesiredRecord[K]

	i := 0
	for i < len(events) {
		pos := events[i].pos

		// Process all events at this position.
		for i < len(events) && cmpU128(events[i].pos, pos) == 0 {
			ev := events[i]
			if ev.isStart {
				active = append(active, ev.idx)
			} else {
				active = removeActive(active, ev.idx)
			}
			i++
		}

		// Determine segment end.
		if i >= len(events) {
			break // no more segments
		}
		nextPos := events[i].pos

		if len(active) == 0 {
			continue
		}

		segFrom := zero.fromU128(pos)
		// When nextPos is the synthetic u128 max (max-address end event),
		// the segment extends to the family maximum, not fromU128(max-1).
		var segTo K
		if nextPos.Hi == maxUint64 && nextPos.Lo == maxUint64 {
			segTo = zero.maxKey()
		} else {
			segTo = zero.fromU128(decU128(nextPos))
		}

		// Last-wins: highest index in active.
		maxIdx := active[0]
		for _, idx := range active[1:] {
			if idx > maxIdx {
				maxIdx = idx
			}
		}
		scope := sorted[maxIdx].ScopeID

		// Coalesce with previous if same scope and adjacent.
		if len(out) > 0 {
			last := &out[len(out)-1]
			if last.ScopeID == scope {
				if after, ok := last.To.checkedInc(); ok && after.cmp(segFrom) == 0 {
					last.To = segTo
					continue
				}
			}
		}
		out = append(out, DesiredRecord[K]{From: segFrom, To: segTo, ScopeID: scope})
	}

	return out
}

func removeActive(active []int, idx int) []int {
	for i, v := range active {
		if v == idx {
			return append(active[:i], active[i+1:]...)
		}
	}
	return active
}

// coalesceAdjacent merges records that are already adjacent (to+1 == next.from)
// AND same scope.
func coalesceAdjacent[K ipKey[K]](records []DesiredRecord[K]) []DesiredRecord[K] {
	if len(records) <= 1 {
		return records
	}
	out := make([]DesiredRecord[K], 0, len(records))
	out = append(out, records[0])
	for i := 1; i < len(records); i++ {
		prev := &out[len(out)-1]
		curr := records[i]
		if prev.ScopeID == curr.ScopeID {
			if next, ok := prev.To.checkedInc(); ok && next.cmp(curr.From) == 0 {
				prev.To = curr.To
				continue
			}
		}
		out = append(out, curr)
	}
	return out
}

func (s *SortedStream[K]) Peek() *DesiredRecord[K] {
	if s.pos >= len(s.records) {
		return nil
	}
	return &s.records[s.pos]
}

func (s *SortedStream[K]) Next() *DesiredRecord[K] {
	if s.pos >= len(s.records) {
		return nil
	}
	r := &s.records[s.pos]
	s.pos++
	return r
}

// --- u128 helpers for the sweep line ---

func cmpU128(a, b Uint128) int {
	if a.Hi != b.Hi {
		if a.Hi < b.Hi {
			return -1
		}
		return 1
	}
	if a.Lo < b.Lo {
		return -1
	}
	if a.Lo > b.Lo {
		return 1
	}
	return 0
}

func decU128(v Uint128) Uint128 {
	if v.Lo == 0 {
		return Uint128{Hi: v.Hi - 1, Lo: ^uint64(0)}
	}
	return Uint128{Hi: v.Hi, Lo: v.Lo - 1}
}

// --- file-backed spill runs (feeds can be bigger than RAM) ---

func spillRecordSize(kw int) int { return 2*kw + ScopeIDSize }

func writeSpillRecord[K ipKey[K]](buf []byte, rec *DesiredRecord[K], kw int) {
	rec.From.writeLE(buf[0:kw])
	rec.To.writeLE(buf[kw : 2*kw])
	putU32(buf, 2*kw, rec.ScopeID)
}

func readSpillRecord[K ipKey[K]](buf []byte, kw int) DesiredRecord[K] {
	var zero K
	return DesiredRecord[K]{
		From:    zero.readLE(buf[0:kw]),
		To:      zero.readLE(buf[kw : 2*kw]),
		ScopeID: u32le(buf, 2*kw),
	}
}

// spillRun sorts + normalizes a chunk and writes it to a temp file.
func spillRun[K ipKey[K]](records []DesiredRecord[K], dir string) (string, error) {
	var zero K
	kw := zero.width()
	sort.Slice(records, func(i, j int) bool {
		return records[i].From.cmp(records[j].From) < 0
	})
	normalized := normalizeChunk[K](records)

	f, err := os.CreateTemp(dir, "iprange_extsort_*")
	if err != nil {
		return "", err
	}
	path := f.Name()
	buf := make([]byte, spillRecordSize(kw))
	for i := range normalized {
		writeSpillRecord(buf, &normalized[i], kw)
		if _, err := f.Write(buf); err != nil {
			f.Close()
			os.Remove(path)
			return "", err
		}
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

// runReader streams one record at a time from a spill-run file.
type runReader[K ipKey[K]] struct {
	file    *os.File
	current DesiredRecord[K]
	ok      bool
	kw      int
	buf     []byte
}

func openRunReader[K ipKey[K]](path string) (*runReader[K], error) {
	var zero K
	kw := zero.width()
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	r := &runReader[K]{
		file: f,
		kw:   kw,
		buf:  make([]byte, spillRecordSize(kw)),
	}
	r.advance()
	return r, nil
}

func (r *runReader[K]) advance() {
	_, err := io.ReadFull(r.file, r.buf)
	if err != nil {
		r.ok = false
		if r.file != nil {
			r.file.Close()
			r.file = nil
		}
		return
	}
	r.current = readSpillRecord[K](r.buf, r.kw)
	r.ok = true
}

// --- K-way merge with cross-run overlap normalization ---

// kMinSource identifies whether the minimum record came from a spill run or
// the pending tail list.
type kMinSource int

const (
	kMinRun     kMinSource = iota // minimum is in a runReader
	kMinPending                   // minimum is in the pending tail list
)

type kWayMerge[K ipKey[K]] struct {
	runs    []*runReader[K]
	cached  *DesiredRecord[K]
	pending []DesiredRecord[K] // tail fragments deferred from truncated results
}

func newKWayMerge[K ipKey[K]](paths []string) (*kWayMerge[K], error) {
	runs := make([]*runReader[K], 0, len(paths))
	for _, p := range paths {
		rr, err := openRunReader[K](p)
		if err != nil {
			for _, r := range runs {
				if r.file != nil {
					r.file.Close()
				}
			}
			return nil, err
		}
		runs = append(runs, rr)
	}
	m := &kWayMerge[K]{runs: runs}
	m.cached = m.computeNext()
	return m, nil
}

// findMin returns the source (run or pending) and index of the record with the
// smallest From across all runs AND the pending tail list.
func (m *kWayMerge[K]) findMin() (kMinSource, int, bool) {
	var best K
	hasBest := false
	var bestSrc kMinSource
	bestIdx := 0

	for i := range m.runs {
		if !m.runs[i].ok {
			continue
		}
		if !hasBest || m.runs[i].current.From.cmp(best) < 0 {
			best = m.runs[i].current.From
			bestSrc = kMinRun
			bestIdx = i
			hasBest = true
		}
	}
	for i := range m.pending {
		if !hasBest || m.pending[i].From.cmp(best) < 0 {
			best = m.pending[i].From
			bestSrc = kMinPending
			bestIdx = i
			hasBest = true
		}
	}
	if !hasBest {
		return 0, 0, false
	}
	return bestSrc, bestIdx, true
}

// peekMin returns the fields of the minimum record without consuming it.
func (m *kWayMerge[K]) peekMin() (from, to K, scope uint32, ok bool) {
	src, idx, hasMin := m.findMin()
	if !hasMin {
		var zero K
		return zero, zero, 0, false
	}
	if src == kMinRun {
		r := m.runs[idx].current
		return r.From, r.To, r.ScopeID, true
	}
	r := m.pending[idx]
	return r.From, r.To, r.ScopeID, true
}

func (m *kWayMerge[K]) popMin() (DesiredRecord[K], bool) {
	src, idx, ok := m.findMin()
	if !ok {
		var zero DesiredRecord[K]
		return zero, false
	}
	if src == kMinRun {
		r := m.runs[idx].current
		m.runs[idx].advance()
		return r, true
	}
	// Pending: swap_remove for O(1) (order is irrelevant; findMin scans all).
	r := m.pending[idx]
	last := len(m.pending) - 1
	m.pending[idx] = m.pending[last]
	m.pending = m.pending[:last]
	return r, true
}

// computeNext produces the next coalesced/normalized record, handling
// cross-run overlaps: same-scope overlaps are extended; different-scope
// overlaps split the result (last-wins) and defer the surviving tail to
// `pending` so it is not lost.
func (m *kWayMerge[K]) computeNext() *DesiredRecord[K] {
	result, ok := m.popMin()
	if !ok {
		return nil
	}
	for {
		nextFrom, nextTo, nextScope, hasMin := m.peekMin()
		if !hasMin {
			break
		}
		if nextFrom.cmp(result.To) > 0 {
			// No overlap — check adjacency for same-scope coalescing.
			if nextScope == result.ScopeID {
				if after, canInc := result.To.checkedInc(); canInc && after.cmp(nextFrom) == 0 {
					popped, _ := m.popMin()
					if popped.To.cmp(result.To) > 0 {
						result.To = popped.To
					}
					continue
				}
			}
			break
		}
		// Overlap!
		if nextScope == result.ScopeID {
			// Same scope → extend.
			popped, _ := m.popMin()
			if popped.To.cmp(result.To) > 0 {
				result.To = popped.To
			}
		} else {
			// Different scope: save the result's surviving tail before
			// it gets overwritten, so it can reappear later.
			originalTo := result.To
			originalScope := result.ScopeID
			if nextFrom.cmp(result.From) > 0 {
				// Partial overlap → truncate result at nextFrom-1, defer
				// any tail beyond nextTo back into pending.
				dec, dok := nextFrom.checkedDec()
				if dok {
					result.To = dec
				} else {
					result.To = nextFrom
				}
				if originalTo.cmp(nextTo) > 0 {
					if ts, ok := nextTo.checkedInc(); ok {
						m.pending = append(m.pending, DesiredRecord[K]{From: ts, To: originalTo, ScopeID: originalScope})
					}
				}
				break
			} else {
				// nextFrom <= result.From → next fully covers result's start.
				// Defer result's tail beyond nextTo, then take next as the
				// new result (last-wins).
				if originalTo.cmp(nextTo) > 0 {
					if ts, ok := nextTo.checkedInc(); ok {
						m.pending = append(m.pending, DesiredRecord[K]{From: ts, To: originalTo, ScopeID: originalScope})
					}
				}
				result, _ = m.popMin()
			}
		}
	}
	r := result
	return &r
}

// MergeStream wraps a kWayMerge, cleaning up temp files when exhausted.
type MergeStream[K ipKey[K]] struct {
	merge    *kWayMerge[K]
	runPaths []string
	cleaned  bool
}

func (m *MergeStream[K]) Peek() *DesiredRecord[K] {
	if m.merge.cached == nil {
		m.cleanup()
		return nil
	}
	return m.merge.cached
}

func (m *MergeStream[K]) Next() *DesiredRecord[K] {
	if m.merge.cached == nil {
		m.cleanup()
		return nil
	}
	r := m.merge.cached
	m.merge.cached = m.merge.computeNext()
	if m.merge.cached == nil {
		m.cleanup()
	}
	return r
}

func (m *MergeStream[K]) cleanup() {
	if m.cleaned {
		return
	}
	m.cleaned = true
	for _, r := range m.merge.runs {
		if r.file != nil {
			r.file.Close()
			r.file = nil
		}
	}
	for _, p := range m.runPaths {
		os.Remove(p)
	}
}

// --- entry point ---

type ExtSortConfig struct {
	ChunkSize int
	TempDir   string
}

func DefaultExtSortConfig() *ExtSortConfig {
	return &ExtSortConfig{ChunkSize: 100000}
}

// ExtSort sorts unsorted records with bounded memory.
func ExtSort[K ipKey[K]](records []DesiredRecord[K], config *ExtSortConfig) (DesiredStream[K], error) {
	if config == nil {
		config = DefaultExtSortConfig()
	}
	if config.ChunkSize <= 0 {
		config = DefaultExtSortConfig()
	}

	if len(records) <= config.ChunkSize {
		return FromUnsorted[K](records), nil
	}

	dir := config.TempDir
	if dir == "" {
		dir = os.TempDir()
	}

	runPaths := make([]string, 0, len(records)/config.ChunkSize+1)
	dropAll := func() {
		for _, p := range runPaths {
			os.Remove(p)
		}
	}

	chunk := make([]DesiredRecord[K], 0, config.ChunkSize)
	for i := range records {
		chunk = append(chunk, records[i])
		if len(chunk) >= config.ChunkSize {
			path, err := spillRun[K](chunk, dir)
			if err != nil {
				dropAll()
				return nil, err
			}
			runPaths = append(runPaths, path)
			chunk = make([]DesiredRecord[K], 0, config.ChunkSize)
		}
	}
	if len(chunk) > 0 {
		path, err := spillRun[K](chunk, dir)
		if err != nil {
			dropAll()
			return nil, err
		}
		runPaths = append(runPaths, path)
	}

	// Single run: read back into memory.
	if len(runPaths) == 1 {
		var zero K
		kw := zero.width()
		f, err := os.Open(runPaths[0])
		if err != nil {
			dropAll()
			return nil, err
		}
		buf := make([]byte, spillRecordSize(kw))
		recs := make([]DesiredRecord[K], 0)
		for {
			if _, err := io.ReadFull(f, buf); err != nil {
				break
			}
			recs = append(recs, readSpillRecord[K](buf, kw))
		}
		f.Close()
		os.Remove(runPaths[0])
		return &SortedStream[K]{records: recs}, nil
	}

	// K-way merge across run files.
	merge, err := newKWayMerge[K](runPaths)
	if err != nil {
		dropAll()
		return nil, err
	}
	return &MergeStream[K]{merge: merge, runPaths: runPaths}, nil
}

// --- streaming sorter ---

type ExtSorter[K ipKey[K]] struct {
	config   *ExtSortConfig
	chunk    []DesiredRecord[K]
	runPaths []string
	finished bool
}

func NewExtSorter[K ipKey[K]](config *ExtSortConfig) *ExtSorter[K] {
	if config == nil {
		config = DefaultExtSortConfig()
	}
	if config.ChunkSize <= 0 {
		config = DefaultExtSortConfig()
	}
	return &ExtSorter[K]{config: config}
}

func (s *ExtSorter[K]) Add(from, to K, scopeID uint32) error {
	s.chunk = append(s.chunk, DesiredRecord[K]{From: from, To: to, ScopeID: scopeID})
	if len(s.chunk) >= s.config.ChunkSize {
		return s.spillChunk()
	}
	return nil
}

func (s *ExtSorter[K]) Finish() (DesiredStream[K], error) {
	s.finished = true

	if len(s.chunk) > 0 {
		if err := s.spillChunk(); err != nil {
			return nil, err
		}
	}

	if len(s.runPaths) == 0 {
		return &SortedStream[K]{}, nil
	}

	if len(s.runPaths) == 1 {
		var zero K
		kw := zero.width()
		f, err := os.Open(s.runPaths[0])
		if err != nil {
			s.abort()
			return nil, err
		}
		buf := make([]byte, spillRecordSize(kw))
		recs := make([]DesiredRecord[K], 0)
		for {
			if _, err := io.ReadFull(f, buf); err != nil {
				break
			}
			recs = append(recs, readSpillRecord[K](buf, kw))
		}
		f.Close()
		os.Remove(s.runPaths[0])
		return &SortedStream[K]{records: recs}, nil
	}

	merge, err := newKWayMerge[K](s.runPaths)
	if err != nil {
		s.abort()
		return nil, err
	}
	return &MergeStream[K]{merge: merge, runPaths: s.runPaths}, nil
}

func (s *ExtSorter[K]) Abort() {
	s.abort()
}

func (s *ExtSorter[K]) abort() {
	for _, p := range s.runPaths {
		os.Remove(p)
	}
	s.runPaths = nil
	s.chunk = nil
}

func (s *ExtSorter[K]) spillChunk() error {
	if len(s.chunk) == 0 {
		return nil
	}
	dir := s.config.TempDir
	if dir == "" {
		dir = os.TempDir()
	}
	path, err := spillRun[K](s.chunk, dir)
	if err != nil {
		return err
	}
	s.runPaths = append(s.runPaths, path)
	s.chunk = s.chunk[:0]
	return nil
}
