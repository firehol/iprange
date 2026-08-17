// Fixed-depth postorder release of one detached tree (Rust
// fixed_tree/walk.rs).

package tree

type walkFrame struct {
	pageNumber uint32
	nextChild  int
	childCount int
	level      uint16
}

// RetireTree walks one detached tree in postorder and retires every page
// (Rust retire_tree). The checkpoint runs before every page visit.
func RetireTree(codec Codec, store RetiringStore, root uint32, checkpoint func() error) error {
	return walk(codec, store, root, checkpoint, func(page uint32) error {
		return store.RetirePages([]uint32{page})
	})
}

// DiscardPrivateTree walks one detached tree in postorder and discards
// every private page (Rust discard_private_tree).
func DiscardPrivateTree(codec Codec, store Store, root uint32, checkpoint func() error) error {
	return walk(codec, store, root, checkpoint, func(page uint32) error {
		return store.DiscardPrivate(page)
	})
}

func walk(codec Codec, store Store, root uint32, checkpoint func() error, release func(page uint32) error) error {
	if root == 0 {
		return nil
	}
	var stack [maxPath]walkFrame
	depth := 0
	current := root
	var expectedLevel *uint16

	var advance func() (bool, error)
	visit := func() (bool, error) {
		targetTxn := store.TargetTxn()
		isLeaf := false
		firstChild := uint32(0)
		var header *Header
		if err := store.Inspect(current, func(page []byte) error {
			h, err := parse(codec, page, targetTxn, expectedLevel)
			if err != nil {
				return err
			}
			header = h
			if h.Level == 0 {
				isLeaf = true
				return nil
			}
			child, err := branchChild(codec, page, h, 0, store.PageLimit())
			if err != nil {
				return err
			}
			firstChild = child
			return nil
		}); err != nil {
			return false, err
		}
		if !isLeaf {
			if depth >= maxPath {
				return false, corrupt("B+tree exceeds its maximum height")
			}
			stack[depth] = walkFrame{pageNumber: current, nextChild: 1, childCount: int(header.ItemCount), level: header.Level}
			depth++
			current = firstChild
			level := header.Level - 1
			expectedLevel = &level
			return false, nil
		}
		if err := release(current); err != nil {
			return false, err
		}
		advanced, err := advance()
		return !advanced, err
	}

	advance = func() (bool, error) {
		for {
			if depth == 0 {
				return false, nil
			}
			frame := stack[depth-1]
			if frame.nextChild < frame.childCount {
				targetTxn := store.TargetTxn()
				child := uint32(0)
				if err := store.Inspect(frame.pageNumber, func(page []byte) error {
					header, err := parse(codec, page, targetTxn, &frame.level)
					if err != nil {
						return err
					}
					if int(header.ItemCount) != frame.childCount {
						return corrupt("B+tree changed during postorder release")
					}
					c, err := branchChild(codec, page, header, frame.nextChild, store.PageLimit())
					if err != nil {
						return err
					}
					child = c
					return nil
				}); err != nil {
					return false, err
				}
				stack[depth-1].nextChild = frame.nextChild + 1
				current = child
				level := frame.level - 1
				expectedLevel = &level
				return true, nil
			}
			depth--
			if err := release(frame.pageNumber); err != nil {
				return false, err
			}
		}
	}

	for {
		if err := checkpoint(); err != nil {
			return err
		}
		done, err := visit()
		if err != nil {
			return err
		}
		if done {
			return nil
		}
	}
}
