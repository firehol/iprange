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

// normalizeChunk resolves overlaps in a sorted chunk into disjoint segments.
// Last-wins for different scope_ids (later records overwrite earlier);
// merge for same scope_ids. Fixes #4: overlapping input is properly split.
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

	// Boundary-based normalization: collect all boundary points, create
	// segments between consecutive boundaries, assign last-wins scope.
	// This handles ALL overlap cases correctly, including tails.
	var boundaries []K
	for _, r := range sorted {
		boundaries = append(boundaries, r.From)
		if after, ok := r.To.checkedInc(); ok {
			boundaries = append(boundaries, after)
		}
	}
	sort.Slice(boundaries, func(i, j int) bool {
		return boundaries[i].cmp(boundaries[j]) < 0
	})
	// Dedup
	db := boundaries[:1]
	for i := 1; i < len(boundaries); i++ {
		if boundaries[i].cmp(db[len(db)-1]) != 0 {
			db = append(db, boundaries[i])
		}
	}
	boundaries = db

	var out []DesiredRecord[K]
	for i := 0; i+1 < len(boundaries); i++ {
		segFrom := boundaries[i]
		segTo, ok := boundaries[i+1].checkedDec()
		if !ok {
			segTo = boundaries[i+1]
		}
		if segFrom.cmp(segTo) > 0 {
			continue
		}

		// Find the LAST record covering this segment (last-wins).
		var scope uint32
		found := false
		for j := range sorted {
			if sorted[j].From.cmp(segFrom) <= 0 && sorted[j].To.cmp(segTo) >= 0 {
				scope = sorted[j].ScopeID
				found = true
			}
		}

		if found {
			if len(out) > 0 {
				last := &out[len(out)-1]
				if last.ScopeID == scope {
					if inc, ok := last.To.checkedInc(); ok && inc.cmp(segFrom) == 0 {
						last.To = segTo
						continue
					}
				}
			}
			out = append(out, DesiredRecord[K]{From: segFrom, To: segTo, ScopeID: scope})
		}
	}

	return out
}

// coalesceAdjacent merges records that are already adjacent (to+1 == next.from)
// AND same scope. Does NOT split overlaps — retained for backward compatibility
// with the k-way merge path.
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

// --- file-backed spill runs (Rule 1: feeds can be bigger than RAM) ---

// spillRecordSize is the on-disk width of one record: [from:kw, to:kw, scope_id:u32].
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

// spillRun sorts + normalizes a chunk and writes it to a temp file. Returns the
// path. On error any partially-written file is removed.
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
		// EOF or short read: this run is drained. Close immediately so the
		// fd does not linger until the whole merge finishes.
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

// kWayMerge merges multiple sorted run readers, emitting the global minimum.
type kWayMerge[K ipKey[K]] struct {
	runs []*runReader[K]
}

func (m *kWayMerge[K]) findMin() (int, bool) {
	minIdx := -1
	for i := range m.runs {
		if !m.runs[i].ok {
			continue
		}
		if minIdx < 0 || m.runs[i].current.From.cmp(m.runs[minIdx].current.From) < 0 {
			minIdx = i
		}
	}
	if minIdx < 0 {
		return 0, false
	}
	return minIdx, true
}

// MergeStream wraps a kWayMerge, coalescing adjacent same-scope records across
// runs and cleaning up its temp files once the stream is exhausted.
type MergeStream[K ipKey[K]] struct {
	merge    *kWayMerge[K]
	runPaths []string
	current  DesiredRecord[K]
	cleaned  bool
}

func (m *MergeStream[K]) Peek() *DesiredRecord[K] {
	idx, ok := m.merge.findMin()
	if !ok {
		m.cleanup()
		return nil
	}
	return &m.merge.runs[idx].current
}

func (m *MergeStream[K]) Next() *DesiredRecord[K] {
	idx, ok := m.merge.findMin()
	if !ok {
		m.cleanup()
		return nil
	}
	result := m.merge.runs[idx].current
	m.merge.runs[idx].advance()
	// Coalesce adjacent same-scope records whose ranges are contiguous,
	// possibly spanning several runs.
	for {
		nIdx, ok := m.merge.findMin()
		if !ok {
			break
		}
		n := m.merge.runs[nIdx].current
		if n.ScopeID != result.ScopeID {
			break
		}
		inc, canInc := result.To.checkedInc()
		if !canInc || inc.cmp(n.From) != 0 {
			break
		}
		result.To = n.To
		m.merge.runs[nIdx].advance()
	}
	m.current = result
	return &m.current
}

// cleanup closes any still-open run files and removes all temp run files. It
// is idempotent and runs when the stream is observed exhausted (Peek/Next past
// the end). A consumer that abandons the stream before draining would leak the
// temp files; Migrate always drains.
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

// ExtSortConfig configures the external sort.
type ExtSortConfig struct {
	ChunkSize int
	TempDir   string
}

// DefaultExtSortConfig returns sensible defaults.
func DefaultExtSortConfig() *ExtSortConfig {
	return &ExtSortConfig{ChunkSize: 100000}
}

// ExtSort sorts unsorted records with bounded memory. Inputs that fit in
// ChunkSize are sorted in memory; larger inputs are spilled to sorted temp
// run files and k-way merged. The returned stream is sorted, disjoint, and
// coalesced. Callers that receive a MergeStream should drain it so its temp
// files are reclaimed.
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

	// Single run: read it back into memory (already sorted + coalesced).
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
	merge := &kWayMerge[K]{runs: make([]*runReader[K], 0, len(runPaths))}
	for _, p := range runPaths {
		rr, err := openRunReader[K](p)
		if err != nil {
			for _, r := range merge.runs {
				if r.file != nil {
					r.file.Close()
				}
			}
			dropAll()
			return nil, err
		}
		merge.runs = append(merge.runs, rr)
	}
	return &MergeStream[K]{merge: merge, runPaths: runPaths}, nil
}

// --- streaming sorter (fixes #1) ---

// ExtSorter accepts records one at a time via Add, spills sorted chunks when
// the buffer is full, and produces a sorted, disjoint, coalesced stream via
// Finish. Memory bounded by ChunkSize × record_size.
type ExtSorter[K ipKey[K]] struct {
	config   *ExtSortConfig
	chunk    []DesiredRecord[K]
	runPaths []string
	finished bool
}

// NewExtSorter creates an incremental external sorter.
func NewExtSorter[K ipKey[K]](config *ExtSortConfig) *ExtSorter[K] {
	if config == nil {
		config = DefaultExtSortConfig()
	}
	if config.ChunkSize <= 0 {
		config = DefaultExtSortConfig()
	}
	return &ExtSorter[K]{config: config}
}

// Add appends a record. When the chunk buffer reaches ChunkSize, it is sorted,
// normalized, coalesced, and spilled to a temp file.
func (s *ExtSorter[K]) Add(from, to K, scopeID uint32) error {
	s.chunk = append(s.chunk, DesiredRecord[K]{From: from, To: to, ScopeID: scopeID})
	if len(s.chunk) >= s.config.ChunkSize {
		return s.spillChunk()
	}
	return nil
}

// Finish completes the sort and returns a sorted, disjoint, coalesced stream.
func (s *ExtSorter[K]) Finish() (DesiredStream[K], error) {
	s.finished = true

	// Spill any remaining records in the chunk buffer.
	if len(s.chunk) > 0 {
		if err := s.spillChunk(); err != nil {
			return nil, err
		}
	}

	if len(s.runPaths) == 0 {
		// No input at all.
		return &SortedStream[K]{}, nil
	}

	if len(s.runPaths) == 1 {
		// Single run: read back into memory (already sorted + normalized).
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

	// Multiple runs: k-way merge with coalescing.
	dir := s.config.TempDir
	if dir == "" {
		dir = os.TempDir()
	}
	_ = dir
	merge := &kWayMerge[K]{runs: make([]*runReader[K], 0, len(s.runPaths))}
	for _, p := range s.runPaths {
		rr, err := openRunReader[K](p)
		if err != nil {
			for _, r := range merge.runs {
				if r.file != nil {
					r.file.Close()
				}
			}
			s.abort()
			return nil, err
		}
		merge.runs = append(merge.runs, rr)
	}
	return &MergeStream[K]{merge: merge, runPaths: s.runPaths}, nil
}

// Abort cleans up temp files. Alternative to Finish.
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
