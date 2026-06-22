package iprangedb

// Zero-copy views over branch (§5.2) and leaf (§5.3) page bodies.
//
// The reader and writer share these. They do offset arithmetic only — no bounds
// validation (that is the reader's §9 walk) beyond what slice indexing guarantees.

// leafView is a leaf page (§5.3): count records of record_size bytes after the 16-byte
// header.
type leafView[K ipKey[K]] struct {
	page       []byte
	count      int
	recordSize int
}

func newLeafView[K ipKey[K]](page []byte, count, recordSize int) leafView[K] {
	return leafView[K]{page: page, count: count, recordSize: recordSize}
}

// len returns the number of records.
func (l leafView[K]) len() int { return l.count }

// record returns the i-th record (i < len). Zero-copy; scope is borrowed (D11).
func (l leafView[K]) record(i int) recordRef[K] {
	off := pageHeaderSize + i*l.recordSize
	return newRecordRef[K](l.page[off : off+l.recordSize])
}

// bodyLen returns the byte length of the populated body (for the §9 tail-zero check).
func (l leafView[K]) bodyLen() int { return l.count * l.recordSize }

// branchView is a branch page (§5.2): child_pgno[0], then sep_count × (sep_key,
// child_pgno) — s separators and s+1 children. Keys are K.width() bytes.
type branchView[K ipKey[K]] struct {
	page     []byte
	sepCount int
}

func newBranchView[K ipKey[K]](page []byte, sepCount int) branchView[K] {
	return branchView[K]{page: page, sepCount: sepCount}
}

// sepCountOf returns the number of separators s.
func (b branchView[K]) sepCountOf() int { return b.sepCount }

// childCount returns the number of children s+1.
func (b branchView[K]) childCount() int { return b.sepCount + 1 }

// sep returns separator i (i < sep_count): a routing key (§5.2).
func (b branchView[K]) sep(i int) K {
	var zero K
	w := zero.width()
	off := pageHeaderSize + 4 + i*(w+4)
	return zero.readLE(b.page[off : off+w])
}

// child returns child pgno j (j <= sep_count). child[0] precedes all separators;
// child[j] (j >= 1) follows sep[j-1].
func (b branchView[K]) child(j int) uint32 {
	var zero K
	w := zero.width()
	var off int
	if j == 0 {
		off = pageHeaderSize
	} else {
		off = pageHeaderSize + 4 + (j-1)*(w+4) + w
	}
	return le.Uint32(b.page[off:])
}

// bodyLen returns the byte length of the populated body (for the §9 tail-zero check):
// 4 + s*(W+4).
func (b branchView[K]) bodyLen() int {
	var zero K
	return 4 + b.sepCount*(zero.width()+4)
}
