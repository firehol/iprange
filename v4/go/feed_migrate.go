package iprangedb

import "fmt"

// Streaming feed migration: update a single feed's membership in a multi-feed
// file to match a desired stream.
//
// This is the update-ipsets operation: "set feed X to this new IP set."
// Other feeds' memberships are preserved. Uses the interval algebra for
// correct boundary splitting and walks the old committed tree one record at a
// time via treeWalker (same fixed-size path stack as migrate.go).
//
// Mirrors the Rust feed_migrate.rs.

// MigrateFeed updates a single feed's membership to match the desired stream.
//
// For each IP range:
//   - In desired AND in old with feed_bit set   → unchanged
//   - In desired AND in old WITHOUT feed_bit     → add feed_bit (intern new bitmap)
//   - NOT in desired AND in old with feed_bit    → clear feed_bit (delete if empty)
//   - NOT in desired AND in old WITHOUT feed_bit → unchanged (other feeds only)
//   - In desired AND NOT in old                  → add feed_bit (new record)
//
// feedBit is the feed index (0-31 for mode 1, 0+ for mode 2). Other feeds'
// memberships are always preserved — only feedBit is added or cleared.
func MigrateFeed[K ipKey[K]](w *Writer[K], feedBit uint32, desired DesiredStream[K], opts *MigrateOptions[K]) (*MigrateCounters, error) {
	_ = opts
	counters := &MigrateCounters{}

	// Migration mode: the treeWalker reads committed pages directly from the
	// store. Same-txn recycling would zero COW victims the walker still needs
	// to traverse, so disable it for the duration of the migration (mirrors the
	// Rust feed_migrate.rs "migration mode" guard against the COW-reuse hazard).
	prevRecycle := w.canRecycle
	w.canRecycle = false
	defer func() { w.canRecycle = prevRecycle }()

	var zero K
	kw := zero.width()
	walker := newTreeWalker[K](w.store, kw, w.committedRoot, w.committedHeight)

	oldFrom, oldTo, oldScope, hasOld := walker.peek()
	desRec := desired.Peek()
	hasDes := desRec != nil

	// Trimmed starts for partial-overlap boundary splitting.
	var oldTrim K
	oldTrimOk := false
	if hasOld {
		oldTrim = oldFrom
		oldTrimOk = true
	}
	var desTrim K
	desTrimOk := false
	if hasDes {
		desTrim = desRec.From
		desTrimOk = true
	}

	for {
		// Compute effective current records (with trimmed starts).
		var oEffFrom, oEffTo K
		var oEffScope uint32
		oEff := false
		if hasOld && oldTrimOk {
			oEffFrom = oldTrim
			oEffTo = oldTo
			oEffScope = oldScope
			oEff = true
		}

		var dEffFrom, dEffTo K
		dEff := false
		if hasDes && desTrimOk {
			dEffFrom = desTrim
			dEffTo = desRec.To
			dEff = true
		}

		if !oEff && !dEff {
			break
		}

		if oEff && !dEff {
			// Old only: clear feed_bit from this range.
			if err := clearFeedRange[K](w, oEffFrom, oEffTo, oEffScope, feedBit, counters); err != nil {
				return nil, err
			}
			counters.OldScanned++
			oldFrom, oldTo, oldScope, hasOld = walker.advance()
			if hasOld {
				oldTrim = oldFrom
				oldTrimOk = true
			} else {
				oldTrimOk = false
			}
			continue
		}

		if !oEff && dEff {
			// Desired only: add feed_bit to this range.
			newScope, err := w.freshFeedScope(feedBit)
			if err != nil {
				return nil, err
			}
			if err := w.Append(dEffFrom, dEffTo, newScope); err != nil {
				return nil, err
			}
			counters.Added++
			counters.DesiredScanned++
			desired.Next()
			desRec = desired.Peek()
			hasDes = desRec != nil
			if hasDes {
				desTrim = desRec.From
				desTrimOk = true
			} else {
				desTrimOk = false
			}
			continue
		}

		// Both present.
		if oEffTo.cmp(dEffFrom) < 0 {
			// Old entirely before desired → clear feed_bit from old.
			if err := clearFeedRange[K](w, oEffFrom, oEffTo, oEffScope, feedBit, counters); err != nil {
				return nil, err
			}
			counters.OldScanned++
			oldFrom, oldTo, oldScope, hasOld = walker.advance()
			if hasOld {
				oldTrim = oldFrom
				oldTrimOk = true
			} else {
				oldTrimOk = false
			}
			continue
		}

		if dEffTo.cmp(oEffFrom) < 0 {
			// Desired entirely before old → add feed_bit.
			newScope, err := w.freshFeedScope(feedBit)
			if err != nil {
				return nil, err
			}
			if err := w.Append(dEffFrom, dEffTo, newScope); err != nil {
				return nil, err
			}
			counters.Added++
			counters.DesiredScanned++
			desired.Next()
			desRec = desired.Peek()
			hasDes = desRec != nil
			if hasDes {
				desTrim = desRec.From
				desTrimOk = true
			} else {
				desTrimOk = false
			}
			continue
		}

		// Overlap! Split at boundaries.
		// Step 1: old-only prefix [oEffFrom, dEffFrom-1] → clear feed_bit.
		if oEffFrom.cmp(dEffFrom) < 0 {
			prefixEnd, ok := dEffFrom.checkedDec()
			if !ok {
				prefixEnd = dEffFrom
			}
			if err := clearFeedRange[K](w, oEffFrom, prefixEnd, oEffScope, feedBit, counters); err != nil {
				return nil, err
			}
		}

		// Step 2: desired-only prefix [dEffFrom, oEffFrom-1] → add feed_bit.
		if dEffFrom.cmp(oEffFrom) < 0 {
			prefixEnd, ok := oEffFrom.checkedDec()
			if !ok {
				prefixEnd = oEffFrom
			}
			newScope, err := w.freshFeedScope(feedBit)
			if err != nil {
				return nil, err
			}
			if err := w.Append(dEffFrom, prefixEnd, newScope); err != nil {
				return nil, err
			}
			counters.Added++
		}

		// Step 3: overlap region starting at overlapStart.
		var overlapStart K
		if oEffFrom.cmp(dEffFrom) < 0 {
			overlapStart = dEffFrom
		} else {
			overlapStart = oEffFrom
		}

		cmpEnd := oEffTo.cmp(dEffTo)
		if cmpEnd == 0 {
			// Same end → apply feed_bit in overlap, advance both.
			if err := applyFeedOverlap[K](w, overlapStart, oEffTo, oEffScope, feedBit, counters); err != nil {
				return nil, err
			}
			counters.OldScanned++
			counters.DesiredScanned++
			oldFrom, oldTo, oldScope, hasOld = walker.advance()
			if hasOld {
				oldTrim = oldFrom
				oldTrimOk = true
			} else {
				oldTrimOk = false
			}
			desired.Next()
			desRec = desired.Peek()
			hasDes = desRec != nil
			if hasDes {
				desTrim = desRec.From
				desTrimOk = true
			} else {
				desTrimOk = false
			}
		} else if cmpEnd < 0 {
			// Old ends first → overlap [overlapStart, oEffTo], then desired continues.
			if err := applyFeedOverlap[K](w, overlapStart, oEffTo, oEffScope, feedBit, counters); err != nil {
				return nil, err
			}
			counters.OldScanned++
			oldFrom, oldTo, oldScope, hasOld = walker.advance()
			if hasOld {
				oldTrim = oldFrom
				oldTrimOk = true
			} else {
				oldTrimOk = false
			}
			// Trim desired start to oEffTo+1.
			trimNext, ok := oEffTo.checkedInc()
			if !ok || trimNext.cmp(dEffTo) > 0 {
				desired.Next()
				desRec = desired.Peek()
				hasDes = desRec != nil
				if hasDes {
					desTrim = desRec.From
					desTrimOk = true
				} else {
					desTrimOk = false
				}
			} else {
				desTrim = trimNext
				desTrimOk = true
			}
		} else {
			// Desired ends first → overlap [overlapStart, dEffTo], then old continues.
			if err := applyFeedOverlap[K](w, overlapStart, dEffTo, oEffScope, feedBit, counters); err != nil {
				return nil, err
			}
			counters.DesiredScanned++
			desired.Next()
			desRec = desired.Peek()
			hasDes = desRec != nil
			if hasDes {
				desTrim = desRec.From
				desTrimOk = true
			} else {
				desTrimOk = false
			}
			// Trim old start to dEffTo+1.
			trimNext, ok := dEffTo.checkedInc()
			if !ok || trimNext.cmp(oEffTo) > 0 {
				oldFrom, oldTo, oldScope, hasOld = walker.advance()
				if hasOld {
					oldTrim = oldFrom
					oldTrimOk = true
				} else {
					oldTrimOk = false
				}
			} else {
				oldTrim = trimNext
				oldTrimOk = true
			}
		}
	}

	// Same truncated-spill guard as Migrate: a partial record in a spill file
	// ends the desired stream early. Without this check the feed would be
	// updated against an incomplete desired set. See migrate.go for details.
	if err := desired.Err(); err != nil {
		return nil, fmt.Errorf("desired stream ended with a read error (truncated spill): %w", err)
	}

	return counters, nil
}

// clearFeedRange clears feedBit from [from, to] which currently has oldScope.
// If the scope changes, the old record is deleted and (if non-empty) re-appended
// with the new scope. Counters.Changed is incremented when a rewrite occurs.
func clearFeedRange[K ipKey[K]](w *Writer[K], from, to K, oldScope, feedBit uint32, counters *MigrateCounters) error {
	newScope, err := w.clearFeedBit(oldScope, feedBit)
	if err != nil {
		return err
	}
	if newScope == oldScope {
		return nil
	}
	if _, err := w.Delete(from, to); err != nil {
		return err
	}
	if newScope != 0 {
		if err := w.Append(from, to, newScope); err != nil {
			return err
		}
	}
	counters.Changed++
	return nil
}

// applyFeedOverlap applies feedBit to [from, to] which currently has oldScope.
// If the scope changes, the old record is deleted and re-appended with the new
// scope. Counters.Changed or Counters.Unchanged is incremented.
func applyFeedOverlap[K ipKey[K]](w *Writer[K], from, to K, oldScope, feedBit uint32, counters *MigrateCounters) error {
	newScope, err := w.applyFeedBit(oldScope, feedBit)
	if err != nil {
		return err
	}
	if newScope == oldScope {
		counters.Unchanged++
		return nil
	}
	if _, err := w.Delete(from, to); err != nil {
		return err
	}
	if err := w.Append(from, to, newScope); err != nil {
		return err
	}
	counters.Changed++
	return nil
}
