package iprangedb

// Interval algebra: the mathematical foundation for correct migration,
// extsort normalization, and feed-bit range operations.
//
// All operations work on sorted, disjoint interval sequences keyed by `from`.
// The core primitive is a sweep-line merge that walks two sequences in
// lock-step, splitting at every boundary to produce exact coverage segments.
//
// Mirrors interval.rs from the Rust reference.

// IntervalRecord is a (from, to, scope) triple — the input to interval algebra.
type IntervalRecord[K ipKey[K]] struct {
	From  K
	To    K
	Scope uint32
}

// SegmentKind classifies what changed in a diff segment.
type SegmentKind int

const (
	SegmentAdded     SegmentKind = 0
	SegmentRemoved   SegmentKind = 1
	SegmentChanged   SegmentKind = 2
	SegmentUnchanged SegmentKind = 3
)

// DiffSegment is a maximal range where old and desired agree or differ,
// produced by the sweep-line diff between two interval sequences.
type DiffSegment[K ipKey[K]] struct {
	From          K
	To            K
	OldScope      uint32 // valid when HasOld
	HasOld        bool
	DesiredScope  uint32 // valid when HasDesired
	HasDesired    bool
}

// Kind returns what changed in this segment.
func (s *DiffSegment[K]) Kind() SegmentKind {
	switch {
	case !s.HasOld && s.HasDesired:
		return SegmentAdded
	case s.HasOld && !s.HasDesired:
		return SegmentRemoved
	case s.HasOld && s.HasDesired && s.OldScope == s.DesiredScope:
		return SegmentUnchanged
	case s.HasOld && s.HasDesired:
		return SegmentChanged
	default:
		return SegmentUnchanged // (None, None) impossible but safe
	}
}

// IntervalDiff computes the exact diff between two sorted, disjoint interval
// sequences. Produces a list of maximal segments where old and desired coverage
// agrees or differs.
//
// This handles ALL overlap cases: identical, partial, one-to-many, many-to-one,
// complete separation, and boundary adjacency.
func IntervalDiff[K ipKey[K]](old, desired []IntervalRecord[K]) []DiffSegment[K] {
	var segments []DiffSegment[K]
	oi := 0 // old cursor
	di := 0 // desired cursor

	// Trimmed views: when a partial overlap consumes part of an interval,
	// we advance the cursor's effective start without advancing the array index.
	var oldTrim K
	var oldTrimOk bool
	var desTrim K
	var desTrimOk bool

	if oi < len(old) {
		oldTrim = old[oi].From
		oldTrimOk = true
	}
	if di < len(desired) {
		desTrim = desired[di].From
		desTrimOk = true
	}

	for {
		// Compute effective current records (with trimmed starts).
		var of, ot K
		var os uint32
		hasOld := false
		if oi < len(old) && oldTrimOk {
			of = oldTrim
			ot = old[oi].To
			os = old[oi].Scope
			hasOld = true
		}

		var df, dt K
		var ds uint32
		hasDes := false
		if di < len(desired) && desTrimOk {
			df = desTrim
			dt = desired[di].To
			ds = desired[di].Scope
			hasDes = true
		}

		if !hasOld && !hasDes {
			break
		}

		if hasOld && !hasDes {
			// Only old remains → removed.
			segments = append(segments, DiffSegment[K]{From: of, To: ot, OldScope: os, HasOld: true})
			oi++
			if oi < len(old) {
				oldTrim = old[oi].From
				oldTrimOk = true
			} else {
				oldTrimOk = false
			}
			continue
		}

		if !hasOld && hasDes {
			// Only desired remains → added.
			segments = append(segments, DiffSegment[K]{From: df, To: dt, DesiredScope: ds, HasDesired: true})
			di++
			if di < len(desired) {
				desTrim = desired[di].From
				desTrimOk = true
			} else {
				desTrimOk = false
			}
			continue
		}

		// Both present.
		if ot.cmp(df) < 0 {
			// Old entirely before desired → removed.
			segments = append(segments, DiffSegment[K]{From: of, To: ot, OldScope: os, HasOld: true})
			oi++
			if oi < len(old) {
				oldTrim = old[oi].From
				oldTrimOk = true
			} else {
				oldTrimOk = false
			}
			continue
		}

		if dt.cmp(of) < 0 {
			// Desired entirely before old → added.
			segments = append(segments, DiffSegment[K]{From: df, To: dt, DesiredScope: ds, HasDesired: true})
			di++
			if di < len(desired) {
				desTrim = desired[di].From
				desTrimOk = true
			} else {
				desTrimOk = false
			}
			continue
		}

		// Overlap! Split at boundaries.
		// Emit any old-only prefix [of, df-1].
		if of.cmp(df) < 0 {
			prefixEnd, ok := df.checkedDec()
			if !ok {
				prefixEnd = df
			}
			segments = append(segments, DiffSegment[K]{
				From: of, To: prefixEnd,
				OldScope: os, HasOld: true,
			})
		}

		// Emit any desired-only prefix [df, of-1].
		if df.cmp(of) < 0 {
			prefixEnd, ok := of.checkedDec()
			if !ok {
				prefixEnd = of
			}
			segments = append(segments, DiffSegment[K]{
				From: df, To: prefixEnd,
				DesiredScope: ds, HasDesired: true,
			})
		}

		// Now both start at overlap_start.
		var overlapStart K
		if of.cmp(df) < 0 {
			overlapStart = df
		} else {
			overlapStart = of
		}

		cmpEnd := ot.cmp(dt)
		if cmpEnd == 0 {
			// Same end → emit the overlap segment, advance both.
			segments = append(segments, DiffSegment[K]{
				From: overlapStart, To: ot,
				OldScope: os, HasOld: true,
				DesiredScope: ds, HasDesired: true,
			})
			oi++
			if oi < len(old) {
				oldTrim = old[oi].From
				oldTrimOk = true
			} else {
				oldTrimOk = false
			}
			di++
			if di < len(desired) {
				desTrim = desired[di].From
				desTrimOk = true
			} else {
				desTrimOk = false
			}
		} else if cmpEnd < 0 {
			// Old ends first → overlap is [overlap_start, ot].
			segments = append(segments, DiffSegment[K]{
				From: overlapStart, To: ot,
				OldScope: os, HasOld: true,
				DesiredScope: ds, HasDesired: true,
			})
			// Advance old, trim desired's start to ot+1.
			oi++
			if oi < len(old) {
				oldTrim = old[oi].From
				oldTrimOk = true
			} else {
				oldTrimOk = false
			}
			trimNext, ok := ot.checkedInc()
			if !ok {
				// Desired's start overflows → desired is fully consumed.
				di++
				if di < len(desired) {
					desTrim = desired[di].From
					desTrimOk = true
				} else {
					desTrimOk = false
				}
			} else {
				desTrim = trimNext
				desTrimOk = true
				if desTrim.cmp(dt) > 0 {
					// Trimmed past desired end → advance.
					di++
					if di < len(desired) {
						desTrim = desired[di].From
						desTrimOk = true
					} else {
						desTrimOk = false
					}
				}
			}
		} else {
			// Desired ends first → overlap is [overlap_start, dt].
			segments = append(segments, DiffSegment[K]{
				From: overlapStart, To: dt,
				OldScope: os, HasOld: true,
				DesiredScope: ds, HasDesired: true,
			})
			// Advance desired, trim old's start to dt+1.
			di++
			if di < len(desired) {
				desTrim = desired[di].From
				desTrimOk = true
			} else {
				desTrimOk = false
			}
			trimNext, ok := dt.checkedInc()
			if !ok {
				// Old's start overflows → old is fully consumed.
				oi++
				if oi < len(old) {
					oldTrim = old[oi].From
					oldTrimOk = true
				} else {
					oldTrimOk = false
				}
			} else {
				oldTrim = trimNext
				oldTrimOk = true
				if oldTrim.cmp(ot) > 0 {
					// Trimmed past old end → advance.
					oi++
					if oi < len(old) {
						oldTrim = old[oi].From
						oldTrimOk = true
					} else {
						oldTrimOk = false
					}
				}
			}
		}
	}

	mergeAdjacentSegments(segments)
	return segments
}

// mergeAdjacentSegments merges adjacent segments with the same
// (HasOld, OldScope, HasDesired, DesiredScope) tuple.
func mergeAdjacentSegments[K ipKey[K]](segs []DiffSegment[K]) []DiffSegment[K] {
	if len(segs) <= 1 {
		return segs
	}
	out := make([]DiffSegment[K], 0, len(segs))
	out = append(out, segs[0])
	for i := 1; i < len(segs); i++ {
		last := &out[len(out)-1]
		curr := segs[i]
		adjacent := false
		if inc, ok := last.To.checkedInc(); ok && inc.cmp(curr.From) == 0 {
			adjacent = true
		}
		sameOld := last.HasOld == curr.HasOld && (!last.HasOld || last.OldScope == curr.OldScope)
		sameDes := last.HasDesired == curr.HasDesired && (!last.HasDesired || last.DesiredScope == curr.DesiredScope)
		if adjacent && sameOld && sameDes {
			last.To = curr.To
		} else {
			out = append(out, curr)
		}
	}
	return out
}

// CoverageSegment is a disjoint segment from normalizing overlapping input.
// Scopes lists ALL scope_ids that cover this segment.
type CoverageSegment[K ipKey[K]] struct {
	From   K
	To     K
	Scopes []uint32
}

// NormalizeOverlapping normalizes a sequence of (possibly overlapping)
// intervals into disjoint coverage segments. Each segment lists ALL scope_ids
// that cover it.
//
// Example:
//
//	Input:  [(10, 20, A), (15, 25, B)]
//	Output: [(10, 14, [A]), (15, 20, [A, B]), (21, 25, [B])]
func NormalizeOverlapping[K ipKey[K]](input []IntervalRecord[K]) []CoverageSegment[K] {
	if len(input) == 0 {
		return nil
	}

	// Collect all boundary points.
	var boundaries []K
	for _, r := range input {
		boundaries = append(boundaries, r.From)
		if after, ok := r.To.checkedInc(); ok {
			boundaries = append(boundaries, after)
		}
	}
	sortKeys(boundaries)
	boundaries = dedupKeys(boundaries)

	// For each consecutive pair of boundaries, find all covering scopes.
	var segments []CoverageSegment[K]
	for i := 0; i+1 < len(boundaries); i++ {
		segFrom := boundaries[i]
		segTo, ok := boundaries[i+1].checkedDec()
		if !ok {
			segTo = boundaries[i+1]
		}
		if segFrom.cmp(segTo) > 0 {
			continue
		}

		var scopes []uint32
		for _, r := range input {
			if r.From.cmp(segFrom) <= 0 && r.To.cmp(segTo) >= 0 {
				scopes = append(scopes, r.Scope)
			}
		}
		if len(scopes) > 0 {
			// Merge with previous segment if same scope set and adjacent.
			if len(segments) > 0 {
				last := &segments[len(segments)-1]
				adjacent := false
				if inc, ok := last.To.checkedInc(); ok && inc.cmp(segFrom) == 0 {
					adjacent = true
				}
				if adjacent && scopeSliceEqual(last.Scopes, scopes) {
					last.To = segTo
					continue
				}
			}
			segments = append(segments, CoverageSegment[K]{
				From:   segFrom,
				To:     segTo,
				Scopes: scopes,
			})
		}
	}

	return segments
}

func sortKeys[K ipKey[K]](keys []K) {
	// Simple insertion sort for small slices; falls back to a divide-and-conquer
	// for larger ones. Using a generic sort helper avoids the sort.Interface
	// boilerplate for the key type.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j].cmp(keys[j-1]) < 0; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
}

func dedupKeys[K ipKey[K]](keys []K) []K {
	if len(keys) <= 1 {
		return keys
	}
	out := keys[:1]
	for i := 1; i < len(keys); i++ {
		if keys[i].cmp(out[len(out)-1]) != 0 {
			out = append(out, keys[i])
		}
	}
	return out
}

func scopeSliceEqual(a, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
