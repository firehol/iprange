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

// FromUnsorted builds a sorted, coalesced stream from unsorted records.
func FromUnsorted[K ipKey[K]](records []DesiredRecord[K]) *SortedStream[K] {
	sort.Slice(records, func(i, j int) bool {
		return records[i].From.cmp(records[j].From) < 0
	})
	coalesced := coalesceAdjacent(records)
	return &SortedStream[K]{records: coalesced}
}

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

// spillRun sorts + coalesces a chunk and writes it to a temp file. Returns the
// path. On error any partially-written file is removed.
func spillRun[K ipKey[K]](records []DesiredRecord[K], dir string) (string, error) {
	var zero K
	kw := zero.width()
	sort.Slice(records, func(i, j int) bool {
		return records[i].From.cmp(records[j].From) < 0
	})
	coalesced := coalesceAdjacent[K](records)

	f, err := os.CreateTemp(dir, "iprange_extsort_*")
	if err != nil {
		return "", err
	}
	path := f.Name()
	buf := make([]byte, spillRecordSize(kw))
	for i := range coalesced {
		writeSpillRecord(buf, &coalesced[i], kw)
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
