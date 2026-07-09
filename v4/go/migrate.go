package iprangedb

import "fmt"

// ChangeEvent represents a change detected during migration.
type ChangeEvent[K ipKey[K]] struct {
	Kind       ChangeKind
	From       K
	To         K
	ScopeID    uint32
	OldScopeID uint32 // valid for Changed (old scope)
	HasOld     bool   // true if OldScopeID is set
}

type ChangeKind int

const (
	ChangeAdded     ChangeKind = 0
	ChangeRemoved   ChangeKind = 1
	ChangeUnchanged ChangeKind = 2
	ChangeChanged   ChangeKind = 3
)

// MigrateCounters holds migration statistics.
type MigrateCounters struct {
	OldScanned     uint64
	DesiredScanned uint64
	Added          uint64
	Removed        uint64
	Changed        uint64
	Unchanged      uint64
}

// MigrateOptions configures the migration.
type MigrateOptions[K ipKey[K]] struct {
	EmitUnchanged bool
	OnChange      func(*ChangeEvent[K])
}

// DesiredRecord is one record from the desired stream.
type DesiredRecord[K ipKey[K]] struct {
	From    K
	To      K
	ScopeID uint32
}

// DesiredStream is a sorted, disjoint stream of desired records.
type DesiredStream[K ipKey[K]] interface {
	Peek() *DesiredRecord[K]
	Next() *DesiredRecord[K]
}

// Migrate updates the writer's pending tree to match the desired stream.
func Migrate[K ipKey[K]](w *Writer[K], desired DesiredStream[K], opts *MigrateOptions[K]) (*MigrateCounters, error) {
	if opts == nil {
		opts = &MigrateOptions[K]{}
	}
	counters := &MigrateCounters{}

	// Scan old records from the committed state.
	// For the in-memory VecPageStore path, we scan into a slice.
	// For mmap-backed, a cursor-based streaming approach would be used.
	img, ok := w.store.(*vecPageStore)
	if !ok {
		return nil, fmt.Errorf("migrate currently requires vecPageStore (streaming cursor TODO)")
	}
	r, err := Open(img.committedBytes())
	if err != nil {
		return nil, err
	}

	// Collect old records
	type rec struct{ from, to []byte; scope uint32 }
	var oldRecords []struct{ from, to []byte; scope uint32 }
	var zero K
	kw := zero.width()
	scanErr := r.scan(kw, func(fromLE, toLE []byte, scopeID uint32) {
		oldRecords = append(oldRecords, struct{ from, to []byte; scope uint32 }{
			from: append([]byte(nil), fromLE...),
			to:   append([]byte(nil), toLE...),
			scope: scopeID,
		})
	})
	if scanErr != nil {
		return nil, scanErr
	}
	counters.OldScanned = uint64(len(oldRecords))

	oldIdx := 0
	for oldIdx < len(oldRecords) || desired.Peek() != nil {
		var oldFrom, oldTo K
		var oldScope uint32
		hasOld := oldIdx < len(oldRecords)
		if hasOld {
			oldFrom = zero.readLE(oldRecords[oldIdx].from)
			oldTo = zero.readLE(oldRecords[oldIdx].to)
			oldScope = oldRecords[oldIdx].scope
		}

		des := desired.Peek()
		hasDes := des != nil

		if !hasOld && hasDes {
			emitChangeGo(opts, &ChangeEvent[K]{Kind: ChangeAdded, From: des.From, To: des.To, ScopeID: des.ScopeID})
			w.Set(des.From, des.To, des.ScopeID)
			desired.Next()
			counters.DesiredScanned++
			counters.Added++
		} else if hasOld && !hasDes {
			emitChangeGo(opts, &ChangeEvent[K]{Kind: ChangeRemoved, From: oldFrom, To: oldTo, OldScopeID: oldScope, HasOld: true})
			w.Delete(oldFrom, oldTo)
			oldIdx++
			counters.Removed++
		} else if hasOld && hasDes {
			if oldTo.cmp(des.From) < 0 {
				emitChangeGo(opts, &ChangeEvent[K]{Kind: ChangeRemoved, From: oldFrom, To: oldTo, OldScopeID: oldScope, HasOld: true})
				w.Delete(oldFrom, oldTo)
				oldIdx++
				counters.Removed++
			} else if des.To.cmp(oldFrom) < 0 {
				emitChangeGo(opts, &ChangeEvent[K]{Kind: ChangeAdded, From: des.From, To: des.To, ScopeID: des.ScopeID})
				w.Set(des.From, des.To, des.ScopeID)
				desired.Next()
				counters.DesiredScanned++
				counters.Added++
			} else {
				if oldFrom.cmp(des.From) == 0 && oldTo.cmp(des.To) == 0 && oldScope == des.ScopeID {
					if opts.EmitUnchanged {
						emitChangeGo(opts, &ChangeEvent[K]{Kind: ChangeUnchanged, From: oldFrom, To: oldTo, ScopeID: oldScope})
					}
					counters.Unchanged++
				} else {
					emitChangeGo(opts, &ChangeEvent[K]{Kind: ChangeChanged, From: des.From, To: des.To, ScopeID: des.ScopeID, OldScopeID: oldScope, HasOld: true})
					w.Set(des.From, des.To, des.ScopeID)
					counters.Changed++
				}
				oldIdx++
				desired.Next()
				counters.DesiredScanned++
			}
		} else {
			break
		}
	}

	return counters, nil
}

func emitChangeGo[K ipKey[K]](opts *MigrateOptions[K], ev *ChangeEvent[K]) {
	if opts.OnChange != nil {
		opts.OnChange(ev)
	}
}
