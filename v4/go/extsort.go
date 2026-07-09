package iprangedb

// SortedStream is an in-memory sorted, coalesced stream of desired records.
type SortedStream[K ipKey[K]] struct {
	records []DesiredRecord[K]
	pos     int
}

// FromUnsorted builds a sorted, coalesced stream from unsorted records.
func FromUnsorted[K ipKey[K]](records []DesiredRecord[K]) *SortedStream[K] {
	// Sort by from ascending (simple insertion sort for small sets; use sort.Slice for large)
	// For correctness we use a simple sort here.
	for i := 1; i < len(records); i++ {
		j := i
		for j > 0 && records[j-1].From.cmp(records[j].From) > 0 {
			records[j-1], records[j] = records[j], records[j-1]
			j--
		}
	}
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

// ExtSortConfig configures the external sort.
type ExtSortConfig struct {
	ChunkSize int
}

// DefaultExtSortConfig returns sensible defaults.
func DefaultExtSortConfig() *ExtSortConfig {
	return &ExtSortConfig{ChunkSize: 100000}
}

// ExtSort sorts unsorted records with bounded memory.
// Currently in-memory only; spill path TODO.
func ExtSort[K ipKey[K]](records []DesiredRecord[K], config *ExtSortConfig) (*SortedStream[K], error) {
	if config == nil {
		config = DefaultExtSortConfig()
	}
	if len(records) > config.ChunkSize {
		return nil, errf("State", "external sort spill not yet implemented")
	}
	return FromUnsorted[K](records), nil
}
