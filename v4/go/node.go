package iprangedb

// leafView is a zero-copy view over a leaf page's records.
type leafView struct {
	page  []byte
	count int
	kw    int // key width (4 or 16)
}

func newLeafView(page []byte, count int, keyWidth int) leafView {
	return leafView{page: page, count: count, kw: keyWidth}
}

func (l *leafView) len() int { return l.count }

// recordFrom returns the `from` key (LE bytes) of record i.
func (l *leafView) recordFrom(i int) []byte {
	rs := recordSizeBytes(l.kw)
	off := PageHeaderSize + i*rs
	return l.page[off : off+l.kw]
}

// recordTo returns the `to` key (LE bytes) of record i.
func (l *leafView) recordTo(i int) []byte {
	rs := recordSizeBytes(l.kw)
	off := PageHeaderSize + i*rs + l.kw
	return l.page[off : off+l.kw]
}

// recordScopeID returns the scope_id of record i.
func (l *leafView) recordScopeID(i int) uint32 {
	rs := recordSizeBytes(l.kw)
	off := PageHeaderSize + i*rs + 2*l.kw
	return u32le(l.page, off)
}

func (l *leafView) bodyLen() int {
	return l.count * recordSizeBytes(l.kw)
}

// branchView is a zero-copy view over a branch page.
type branchView struct {
	page     []byte
	sepCount int
	kw       int
}

func newBranchView(page []byte, sepCount int, keyWidth int) branchView {
	return branchView{page: page, sepCount: sepCount, kw: keyWidth}
}

func (b *branchView) sepCount_() int  { return b.sepCount }
func (b *branchView) childCount() int { return b.sepCount + 1 }

func (b *branchView) sep(i int) []byte {
	off := PageHeaderSize + 4 + i*(b.kw+4)
	return b.page[off : off+b.kw]
}

func (b *branchView) child(j int) uint32 {
	var off int
	if j == 0 {
		off = PageHeaderSize
	} else {
		off = PageHeaderSize + 4 + (j-1)*(b.kw+4) + b.kw
	}
	return u32le(b.page, off)
}

func (b *branchView) bodyLen() int {
	return 4 + b.sepCount*(b.kw+4)
}
