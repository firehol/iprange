package format

import (
	"bytes"
)

// Meta is the exact decoded meta page (binary-format-v4.md section 4). All
// fields are the raw wire values; invariant checks happen in ParseMeta.
type Meta struct {
	AddressFamily        uint8
	ValueKind            uint8
	StructureKind        uint8
	ValueTag             [16]byte
	DatabaseID           [16]byte
	TxnID                uint64
	CommitNonce          [16]byte
	PageCount            uint64
	RangeRecordCount     uint64
	ActiveFeedCount      uint64
	FeedIndexLimit       uint64
	MembershipEntryCount uint64
	MembershipIDLimit    uint64
	MetadataUncompressed uint64
	MetadataCompressed   uint64
	RetiredExtentCount   uint64
	RangeRoot            uint32
	CatalogNameRoot      uint32
	CatalogIndexRoot     uint32
	FeedUsedRoot         uint32
	MembershipIDRoot     uint32
	MembershipHashRoot   uint32
	MembershipUsedRoot   uint32
	MetadataRoot         uint32
	FreeBitmapRoot       uint32
	RetirementRoot       uint32
	AllocatorReserve     [4]uint32
	StructureEntryCount  uint64
	StructureIDLimit     uint64
	StructureIDRoot      uint32
	StructureHashRoot    uint32
	StructureUsedRoot    uint32
	MetaCRC32C           uint32
}

// ParseIdentity performs the static identity checks of section 4.1 on one
// complete meta page: magic, fixed constants, reserved bytes, value tag,
// nonzero database ID, and the meta CRC. It reports whether the page is
// identity-readable and, when it is, returns both the decoded scalars and the
// raw meta-page bytes for equality comparisons.
func ParseIdentity(page []byte) (Meta, bool) {
	var m Meta
	if len(page) != PageSize {
		return m, false
	}
	if !bytes.Equal(page[0:8], MainMagic[:]) {
		return m, false
	}
	if U16(page[8:10]) != MetaSize {
		return m, false
	}
	if page[10] != PageShift {
		return m, false
	}
	if U16(page[14:16]) != 0 {
		return m, false
	}
	tag := page[16:32]
	nul := bytes.IndexByte(tag, 0)
	if nul < 0 || nul > 15 {
		return m, false
	}
	for _, b := range tag[nul+1:] {
		if b != 0 {
			return m, false
		}
	}
	m.AddressFamily = page[11]
	if m.AddressFamily != AddressFamilyIPv4 && m.AddressFamily != AddressFamilyIPv6 {
		return m, false
	}
	m.ValueKind = page[12]
	if m.ValueKind != ValueKindDirect && m.ValueKind != ValueKindMembership && m.ValueKind != ValueKindStructured {
		return m, false
	}
	m.StructureKind = page[13]
	copy(m.ValueTag[:], tag)
	copy(m.DatabaseID[:], page[32:48])
	if bytes.Equal(m.DatabaseID[:], make([]byte, 16)) {
		return m, false
	}
	for i := 228; i < 252; i++ {
		if page[i] != 0 {
			return m, false
		}
	}
	m.MetaCRC32C = U32(page[252:256])
	if MetaCRC32C(page) != m.MetaCRC32C {
		return m, false
	}
	fillMetaScalars(&m, page)
	return m, true
}

func fillMetaScalars(m *Meta, page []byte) {
	m.TxnID = U64(page[48:56])
	copy(m.CommitNonce[:], page[56:72])
	m.PageCount = U64(page[72:80])
	m.RangeRecordCount = U64(page[80:88])
	m.ActiveFeedCount = U64(page[88:96])
	m.FeedIndexLimit = U64(page[96:104])
	m.MembershipEntryCount = U64(page[104:112])
	m.MembershipIDLimit = U64(page[112:120])
	m.MetadataUncompressed = U64(page[120:128])
	m.MetadataCompressed = U64(page[128:136])
	m.RetiredExtentCount = U64(page[136:144])
	m.RangeRoot = U32(page[144:148])
	m.CatalogNameRoot = U32(page[148:152])
	m.CatalogIndexRoot = U32(page[152:156])
	m.FeedUsedRoot = U32(page[156:160])
	m.MembershipIDRoot = U32(page[160:164])
	m.MembershipHashRoot = U32(page[164:168])
	m.MembershipUsedRoot = U32(page[168:172])
	m.MetadataRoot = U32(page[172:176])
	m.FreeBitmapRoot = U32(page[176:180])
	m.RetirementRoot = U32(page[180:184])
	for i := 0; i < 4; i++ {
		m.AllocatorReserve[i] = U32(page[184+4*i : 188+4*i])
	}
	m.StructureEntryCount = U64(page[200:208])
	m.StructureIDLimit = U64(page[208:216])
	m.StructureIDRoot = U32(page[216:220])
	m.StructureHashRoot = U32(page[220:224])
	m.StructureUsedRoot = U32(page[224:228])
}

// errMeta keeps one typed reason for bootstrap-validity failure.
type errMeta struct {
	reason string
	code   error
}

func (e *errMeta) Error() string { return "v4 meta: " + e.reason }

// ErrUnsupportedStructureKind reports a recognizable v4 identity whose
// structure kind this SDK does not implement.
var ErrUnsupportedStructureKind = &errMeta{reason: "unsupported structure kind", code: ErrUnsupportedStructure}

// ValidateKindInvariants applies the dynamic bootstrap checks of sections
// 4.2/4.3 that depend only on this one meta page (counts, limits, roots,
// geometry, checked host-addressability). It must be called only on an
// identity-readable meta. A nonzero structure kind other than the supported
// registry value yields ErrUnsupportedStructureKind so the reader can return
// the typed UnsupportedStructure after selection.
func (m *Meta) ValidateKindInvariants() error {
	if m.TxnID == 0 {
		return &errMeta{reason: "zero transaction id", code: ErrFormat}
	}
	if bytes.Equal(m.CommitNonce[:], make([]byte, 16)) {
		return &errMeta{reason: "zero commit nonce", code: ErrFormat}
	}
	if m.PageCount < 2 || m.PageCount > MaxPageCount {
		return &errMeta{reason: "page count out of range", code: ErrFormat}
	}
	roots := []uint32{
		m.RangeRoot, m.CatalogNameRoot, m.CatalogIndexRoot, m.FeedUsedRoot,
		m.MembershipIDRoot, m.MembershipHashRoot, m.MembershipUsedRoot,
		m.MetadataRoot, m.FreeBitmapRoot, m.RetirementRoot,
		m.StructureIDRoot, m.StructureHashRoot, m.StructureUsedRoot,
	}
	for _, r := range roots {
		if r != 0 && (uint64(r) < 2 || uint64(r) >= m.PageCount) {
			return &errMeta{reason: "root page out of range", code: ErrFormat}
		}
	}
	for i, r := range m.AllocatorReserve {
		if r == 0 {
			continue
		}
		if uint64(r) < 2 || uint64(r) >= m.PageCount {
			return &errMeta{reason: "allocator reserve out of range", code: ErrFormat}
		}
		for j, other := range m.AllocatorReserve {
			if i != j && other == r {
				return &errMeta{reason: "duplicate allocator reserve", code: ErrFormat}
			}
		}
		for _, root := range roots {
			if root == r {
				return &errMeta{reason: "allocator reserve aliases a root", code: ErrFormat}
			}
		}
	}
	if m.MetadataRoot == 0 {
		if m.MetadataUncompressed != 0 || m.MetadataCompressed != 0 {
			return &errMeta{reason: "metadata lengths without root", code: ErrFormat}
		}
	} else {
		if m.MetadataCompressed == 0 {
			return &errMeta{reason: "metadata root without compressed length", code: ErrFormat}
		}
		if m.MetadataUncompressed > MaxMetadataUncompressed {
			return &errMeta{reason: "metadata uncompressed length over limit", code: ErrFormat}
		}
		if m.MetadataCompressed > MetadataCompressedBound(m.MetadataUncompressed) {
			return &errMeta{reason: "metadata compressed length beyond bound", code: ErrFormat}
		}
	}
	if m.RetiredExtentCount == 0 {
		if m.RetirementRoot != 0 {
			return &errMeta{reason: "retirement root without extents", code: ErrFormat}
		}
	} else if m.RetirementRoot == 0 {
		return &errMeta{reason: "retirement extents without root", code: ErrFormat}
	}
	if m.RangeRecordCount == 0 {
		if m.RangeRoot != 0 {
			return &errMeta{reason: "range root without records", code: ErrFormat}
		}
	} else if m.RangeRoot == 0 {
		return &errMeta{reason: "range records without root", code: ErrFormat}
	}

	switch m.ValueKind {
	case ValueKindDirect:
		return validateDirectMeta(m)
	case ValueKindMembership:
		return validateMembershipMeta(m)
	case ValueKindStructured:
		if m.StructureKind != StructureKindNetworkEnrichmentV1 {
			return ErrUnsupportedStructureKind
		}
		return validateStructuredMeta(m)
	default:
		return &errMeta{reason: "invalid value kind", code: ErrNotV4}
	}
}

// validateDirectMeta: catalog, membership, and structure state are all
// required to be zero; no dictionary relationship checks apply.
func validateDirectMeta(m *Meta) error {
	if m.StructureKind != 0 {
		return ErrUnsupportedStructureKind
	}
	if m.StructureEntryCount != 0 || m.StructureIDLimit != 0 ||
		m.StructureIDRoot != 0 || m.StructureHashRoot != 0 || m.StructureUsedRoot != 0 {
		return &errMeta{reason: "direct file with structure state", code: ErrFormat}
	}
	if m.ActiveFeedCount != 0 || m.FeedIndexLimit != 0 ||
		m.MembershipEntryCount != 0 || m.MembershipIDLimit != 0 ||
		m.CatalogNameRoot != 0 || m.CatalogIndexRoot != 0 || m.FeedUsedRoot != 0 ||
		m.MembershipIDRoot != 0 || m.MembershipHashRoot != 0 || m.MembershipUsedRoot != 0 {
		return &errMeta{reason: "direct file with catalog or membership state", code: ErrFormat}
	}
	return nil
}

func validateMembershipMeta(m *Meta) error {
	if m.StructureKind != 0 {
		return ErrUnsupportedStructureKind
	}
	if m.StructureEntryCount != 0 || m.StructureIDLimit != 0 ||
		m.StructureIDRoot != 0 || m.StructureHashRoot != 0 || m.StructureUsedRoot != 0 {
		return &errMeta{reason: "membership file with structure state", code: ErrFormat}
	}
	if m.FeedIndexLimit > MaxPageCount {
		return &errMeta{reason: "feed index limit out of range", code: ErrFormat}
	}
	if m.ActiveFeedCount > m.FeedIndexLimit {
		return &errMeta{reason: "active feeds above limit", code: ErrFormat}
	}
	if m.MembershipEntryCount >= MaxPageCount {
		return &errMeta{reason: "membership entry count out of range", code: ErrFormat}
	}
	if m.MembershipIDLimit < 1 || m.MembershipIDLimit > MaxPageCount {
		return &errMeta{reason: "membership id limit out of range", code: ErrFormat}
	}
	if m.MembershipEntryCount > m.RangeRecordCount {
		return &errMeta{reason: "membership entries above range records", code: ErrFormat}
	}
	if m.ActiveFeedCount == 0 {
		if m.MembershipEntryCount != 0 || m.RangeRecordCount != 0 {
			return &errMeta{reason: "zero feeds with entries or ranges", code: ErrFormat}
		}
	}
	if m.MembershipEntryCount == 0 && m.RangeRecordCount != 0 {
		return &errMeta{reason: "zero membership entries with ranges", code: ErrFormat}
	}
	return zeroRelations(m, true, true)
}

func validateStructuredMeta(m *Meta) error {
	if m.FeedIndexLimit > MaxPageCount {
		return &errMeta{reason: "feed index limit out of range", code: ErrFormat}
	}
	if m.ActiveFeedCount > m.FeedIndexLimit {
		return &errMeta{reason: "active feeds above limit", code: ErrFormat}
	}
	if m.MembershipEntryCount >= MaxPageCount {
		return &errMeta{reason: "membership entry count out of range", code: ErrFormat}
	}
	if m.MembershipIDLimit < 1 || m.MembershipIDLimit > MaxPageCount {
		return &errMeta{reason: "membership id limit out of range", code: ErrFormat}
	}
	if m.StructureEntryCount >= MaxPageCount {
		return &errMeta{reason: "structure entry count out of range", code: ErrFormat}
	}
	if m.StructureIDLimit < 1 || m.StructureIDLimit > MaxPageCount {
		return &errMeta{reason: "structure id limit out of range", code: ErrFormat}
	}
	if m.MembershipEntryCount > m.StructureEntryCount {
		return &errMeta{reason: "membership entries above structure entries", code: ErrFormat}
	}
	if m.StructureEntryCount > m.RangeRecordCount {
		return &errMeta{reason: "structure entries above range records", code: ErrFormat}
	}
	if m.StructureEntryCount == 0 && m.RangeRecordCount != 0 {
		return &errMeta{reason: "zero structure entries with ranges", code: ErrFormat}
	}
	if m.ActiveFeedCount == 0 && m.MembershipEntryCount != 0 {
		return &errMeta{reason: "zero feeds with membership entries", code: ErrFormat}
	}
	if m.StructureEntryCount == 0 {
		if m.StructureIDRoot != 0 || m.StructureHashRoot != 0 || m.StructureUsedRoot != 0 {
			return &errMeta{reason: "nonzero structure roots with zero entries", code: ErrFormat}
		}
		if m.StructureIDLimit != 1 {
			return &errMeta{reason: "empty structure dictionary with limit != 1", code: ErrFormat}
		}
	} else if m.StructureIDRoot == 0 || m.StructureHashRoot == 0 || m.StructureUsedRoot == 0 {
		return &errMeta{reason: "zero structure root with nonzero entries", code: ErrFormat}
	}
	return zeroRelations(m, true, true)
}

// zeroRelations enforces the shared root/count zero relations of section
// 4.3 for membership-capable files (membership and structured kinds).
func zeroRelations(m *Meta, checkMembership, checkCatalog bool) error {
	requireAllZero := func(roots ...uint32) error {
		for _, r := range roots {
			if r != 0 {
				return &errMeta{reason: "nonzero root with zero count", code: ErrFormat}
			}
		}
		return nil
	}
	requireAllNonzero := func(roots ...uint32) error {
		for _, r := range roots {
			if r == 0 {
				return &errMeta{reason: "zero root with nonzero count", code: ErrFormat}
			}
		}
		return nil
	}
	if checkMembership {
		if m.MembershipEntryCount == 0 {
			if err := requireAllZero(m.MembershipIDRoot, m.MembershipHashRoot, m.MembershipUsedRoot); err != nil {
				return err
			}
			if m.MembershipIDLimit != 1 {
				return &errMeta{reason: "empty membership dictionary with limit != 1", code: ErrFormat}
			}
		} else if err := requireAllNonzero(m.MembershipIDRoot, m.MembershipHashRoot, m.MembershipUsedRoot); err != nil {
			return err
		}
	}
	if checkCatalog {
		if m.ActiveFeedCount == 0 {
			if err := requireAllZero(m.CatalogNameRoot, m.CatalogIndexRoot, m.FeedUsedRoot); err != nil {
				return err
			}
		} else if err := requireAllNonzero(m.CatalogNameRoot, m.CatalogIndexRoot, m.FeedUsedRoot); err != nil {
			return err
		}
	}
	return nil
}

// UnsupportedKind reports whether ValidateKindInvariants failed because of an
// unknown nonzero structure kind.
func UnsupportedKind(err error) bool {
	return err == ErrUnsupportedStructureKind
}
