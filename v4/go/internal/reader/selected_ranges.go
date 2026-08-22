package reader

// Canonical selected-feed runs over one physical membership cursor (Rust
// membership_query/selected.rs parity). Adjacent physical ranges whose
// selected-feed set is identical are merged into one selected range, so
// the join sweeps and the aggregation scan see the minimal run set. The
// lookahead scratch is only present for named scopes: the all-catalog
// scope disables merging exactly like Rust, because its selected sets
// are decoded from the whole bitmap and adjacent equal sets are resolved
// by the range edit invariants.

// selectedRange is one merged selected interval.
type selectedRange struct {
	from, to addrKey
}

// selectedRanges yields the selected runs of one scope over one physical
// membership cursor, decoding each distinct membership bitmap once.
type selectedRanges struct {
	r             *ImmutableReader
	scope         *ScopeData
	stream        *membershipIterator
	ops           rangeOps
	active        *scratch
	lookahead     *scratch
	pending       *membershipRange
	physicalCount uint64
}

func newSelectedRanges(r *ImmutableReader, scope *ScopeData, stream *membershipIterator, ops rangeOps, heap *operationHeap) (*selectedRanges, error) {
	active, err := newScratch(len(scope.entries), heap)
	if err != nil {
		return nil, err
	}
	var lookahead *scratch
	if !scope.allCatalog {
		lookahead, err = newScratch(len(scope.entries), heap)
		if err != nil {
			return nil, err
		}
	}
	return &selectedRanges{
		r:         r,
		scope:     scope,
		stream:    stream,
		ops:       ops,
		active:    active,
		lookahead: lookahead,
	}, nil
}

// enableCache splits the given byte budget over the active and lookahead
// decode caches (Rust SelectedRanges::enable_cache).
func (s *selectedRanges) enableCache(heap *operationHeap, maxBytes uint64) error {
	count := uint64(1)
	if s.lookahead != nil {
		count = 2
	}
	share := maxBytes / count
	if err := s.active.cache.enable(heap, share); err != nil {
		return err
	}
	if s.lookahead != nil {
		if err := s.lookahead.cache.enable(heap, share); err != nil {
			return err
		}
	}
	return nil
}

// next returns the next selected run, or none when the physical cursor
// is exhausted (the active scratch is cleared exactly like Rust).
func (s *selectedRanges) next(check checkpoint) (*selectedRange, error) {
	var current *membershipRange
	if s.pending != nil {
		p := s.pending
		s.pending = nil
		s.active, s.lookahead = s.lookahead, s.active
		current = p
	} else {
		for {
			first, ok, err := s.nextPhysical(check)
			if err != nil {
				return nil, err
			}
			if !ok {
				if err := s.active.clear(check); err != nil {
					return nil, err
				}
				return nil, nil
			}
			if err := s.active.load(s.r, first.membershipID, s.scope, check); err != nil {
				return nil, err
			}
			if len(s.active.presentList()) != 0 {
				current = &first
				break
			}
		}
	}

	if s.lookahead == nil {
		return &selectedRange{from: current.from, to: current.to}, nil
	}
	for {
		next, ok, err := s.nextPhysical(check)
		if err != nil {
			return nil, err
		}
		if !ok {
			return &selectedRange{from: current.from, to: current.to}, nil
		}
		if err := s.lookahead.load(s.r, next.membershipID, s.scope, check); err != nil {
			return nil, err
		}
		if len(s.lookahead.presentList()) == 0 {
			return &selectedRange{from: current.from, to: current.to}, nil
		}
		nextFrom, err := s.ops.next(current.to)
		if err == nil && nextFrom.Equal(next.from) && samePresent(s.active.presentList(), s.lookahead.presentList()) {
			current.to = next.to
			continue
		}
		p := next
		s.pending = &p
		return &selectedRange{from: current.from, to: current.to}, nil
	}
}

// present returns the selected feeds of the most recent run.
func (s *selectedRanges) present() []uint32 {
	return s.active.presentList()
}

// count returns the number of physical ranges consumed so far.
func (s *selectedRanges) count() uint64 {
	return s.physicalCount
}

func (s *selectedRanges) nextPhysical(check checkpoint) (membershipRange, bool, error) {
	if err := checkEvery(s.physicalCount, check); err != nil {
		return membershipRange{}, false, err
	}
	rangeRecord, ok, err := s.stream.next()
	if err != nil {
		return membershipRange{}, false, err
	}
	if ok {
		count, err := increment64(s.physicalCount, "membership scan range count")
		if err != nil {
			return membershipRange{}, false, err
		}
		s.physicalCount = count
	}
	return rangeRecord, ok, nil
}

// samePresent compares two ascending position lists elementwise (Rust
// Vec equality).
func samePresent(left, right []uint32) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
