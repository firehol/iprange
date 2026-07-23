package exactv4

import (
	"encoding/binary"
	"errors"
	"testing"
)

func emptyDirectMeta(txnID uint64) Meta {
	tag, err := NewValueTag(nil)
	if err != nil {
		panic(err)
	}
	return Meta{
		AddressFamily: AddressFamilyIPv4,
		ValueKind:     ValueKindDirect,
		ValueTag:      tag,
		DatabaseID:    [16]byte{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1},
		TxnID:         txnID,
		CommitNonce:   [16]byte{2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2},
		PageCount:     2,
	}
}

func metaImage(meta0, meta1 Meta) []byte {
	page0 := meta0.EncodePage()
	page1 := meta1.EncodePage()
	data := make([]byte, 2*PageSize)
	copy(data[:PageSize], page0[:])
	copy(data[PageSize:], page1[:])
	return data
}

func requireBootstrapCode(t *testing.T, err error, want BootstrapErrorCode) *BootstrapError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected bootstrap error %d", want)
	}
	var got *BootstrapError
	if !errors.As(err, &got) {
		t.Fatalf("error type = %T, want *BootstrapError: %v", err, err)
	}
	if got.Code != want {
		t.Fatalf("bootstrap code = %d, want %d", got.Code, want)
	}
	return got
}

func TestIdenticalCreationMetasAreProvenCurrent(t *testing.T) {
	data := metaImage(emptyDirectMeta(1), emptyDirectMeta(1))
	opened, err := Open(data, OpenWriter)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Selection != SelectionProvenCurrent || opened.SelectedMetaPage != 1 {
		t.Fatalf("selection = %d page %d", opened.Selection, opened.SelectedMetaPage)
	}
	if opened.CommittedBytes != 2*PageSize || opened.UnpublishedTailBytes() != 0 {
		t.Fatalf("lengths = committed %d tail %d", opened.CommittedBytes, opened.UnpublishedTailBytes())
	}
}

func TestRetainedMetaPageBootstrapMatchesSliceBootstrap(t *testing.T) {
	data := append(metaImage(emptyDirectMeta(1), emptyDirectMeta(1)), make([]byte, PageSize)...)
	page0 := (*[PageSize]byte)(data[:PageSize])
	page1 := (*[PageSize]byte)(data[PageSize : 2*PageSize])

	retained, retainedErr := openMetaPages(page0, page1, uint64(len(data)), OpenLiveReader)
	sliced, slicedErr := Open(data, OpenLiveReader)
	if retainedErr != nil || slicedErr != nil || retained != sliced {
		t.Fatalf("retained/slice bootstrap = (%+v, %v) / (%+v, %v)", retained, retainedErr, sliced, slicedErr)
	}

	_, err := openMetaPages(page0, page1, 2*PageSize-1, OpenLiveReader)
	requireBootstrapCode(t, err, BootstrapErrFileTooShort)
	_, err = openMetaPages(page0, page1, 2*PageSize+1, OpenLiveReader)
	requireBootstrapCode(t, err, BootstrapErrFileUnaligned)
	_, err = openMetaPages(page0, page1, uint64(1)<<63, OpenLiveReader)
	requireBootstrapCode(t, err, BootstrapErrHostAddressability)
	_, err = openMetaPages(page0, page1, 0, 0)
	requireBootstrapCode(t, err, BootstrapErrInvalidOpenMode)
}

func TestAdjacentTransactionRequiresPhysicalParity(t *testing.T) {
	old := emptyDirectMeta(1)
	newer := old
	newer.TxnID = 2
	newer.CommitNonce = [16]byte{3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3}

	opened, err := Open(metaImage(newer, old), OpenWriter)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Meta.TxnID != 2 || opened.SelectedMetaPage != 0 {
		t.Fatalf("selected txn/page = %d/%d", opened.Meta.TxnID, opened.SelectedMetaPage)
	}
	requireBootstrapCode(t, openError(metaImage(old, newer), OpenWriter), BootstrapErrPhysicalParity)
}

func TestPairGapAndEqualDisagreementFailClosed(t *testing.T) {
	old := emptyDirectMeta(1)
	gap := old
	gap.TxnID = 3
	gap.CommitNonce[0] = 3
	requireBootstrapCode(t, openError(metaImage(old, gap), OpenImmutableReader), BootstrapErrTransactionGap)

	disagree := old
	disagree.CommitNonce[0] = 4
	requireBootstrapCode(t, openError(metaImage(old, disagree), OpenImmutableReader), BootstrapErrEqualTransactionDisagreement)
}

func TestSoleMetaIsFactualButNotMutableAuthority(t *testing.T) {
	data := metaImage(emptyDirectMeta(1), emptyDirectMeta(1))
	data[MetaCRCOffset] ^= 1
	opened, err := Open(data, OpenImmutableReader)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Selection != SelectionSoleMeta1 {
		t.Fatalf("selection = %d", opened.Selection)
	}
	requireBootstrapCode(t, openError(data, OpenLiveReader), BootstrapErrCurrentGenerationUnprovable)
	requireBootstrapCode(t, openError(data, OpenWriter), BootstrapErrCurrentGenerationUnprovable)
}

func TestIdentityMismatchPrecedesDynamicInvalidity(t *testing.T) {
	left := emptyDirectMeta(1)
	right := left
	right.DatabaseID[0] = 9
	right.CommitNonce = [16]byte{}
	err := openError(metaImage(left, right), OpenImmutableReader)
	requireBootstrapCode(t, err, BootstrapErrStaticIdentityMismatch)
}

func TestOpenLengthSemantics(t *testing.T) {
	base := metaImage(emptyDirectMeta(1), emptyDirectMeta(1))
	requireBootstrapCode(t, openError(base[:PageSize], OpenImmutableReader), BootstrapErrFileTooShort)

	unaligned := append(append([]byte(nil), base...), 0)
	requireBootstrapCode(t, openError(unaligned, OpenImmutableReader), BootstrapErrFileUnaligned)

	alignedTail := append(append([]byte(nil), base...), make([]byte, PageSize)...)
	requireBootstrapCode(t, openError(alignedTail, OpenImmutableReader), BootstrapErrImmutableLengthMismatch)
	for _, mode := range []OpenMode{OpenLiveReader, OpenWriter} {
		opened, err := Open(alignedTail, mode)
		if err != nil {
			t.Fatalf("mode %d: %v", mode, err)
		}
		if opened.CommittedBytes != 2*PageSize || opened.PhysicalBytes != 3*PageSize || opened.UnpublishedTailBytes() != PageSize {
			t.Fatalf("mode %d lengths = %+v", mode, opened)
		}
	}
}

func TestImpossibleCountsAndMetadataAreNotBootstrapValid(t *testing.T) {
	badCount := emptyDirectMeta(1)
	badCount.RangeRecordCount = 1
	problem := requireBootstrapCode(t, openError(metaImage(badCount, badCount), OpenImmutableReader), BootstrapErrNoBootstrapMeta)
	if problem.Meta0 != MetaProblemCountInvariant || problem.Meta1 != MetaProblemCountInvariant {
		t.Fatalf("count findings = %d/%d", problem.Meta0, problem.Meta1)
	}

	badMetadata := emptyDirectMeta(1)
	badMetadata.PageCount = 3
	badMetadata.MetadataRoot = 2
	badMetadata.MetadataCompressedLen = 4049
	data := metaImage(badMetadata, badMetadata)
	data = append(data, make([]byte, PageSize)...)
	problem = requireBootstrapCode(t, openError(data, OpenImmutableReader), BootstrapErrNoBootstrapMeta)
	if problem.Meta0 != MetaProblemMetadataInvariant || problem.Meta1 != MetaProblemMetadataInvariant {
		t.Fatalf("metadata findings = %d/%d", problem.Meta0, problem.Meta1)
	}
}

func TestCanonicalEmptyMembershipMetaIsValid(t *testing.T) {
	membership := emptyDirectMeta(1)
	membership.ValueKind = ValueKindMembership
	membership.MembershipIDLimit = 1
	opened, err := Open(metaImage(membership, membership), OpenWriter)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Selection != SelectionProvenCurrent {
		t.Fatalf("selection = %d", opened.Selection)
	}
}

func openError(data []byte, mode OpenMode) error {
	_, err := Open(data, mode)
	return err
}

func rewriteMetaCRC(page []byte) {
	binary.LittleEndian.PutUint32(page[MetaCRCOffset:MetaCRCOffset+4], metaCRC(page))
}

func assertIdentityProblem(t *testing.T, offset int, value byte, expected MetaProblem) {
	t.Helper()
	data := metaImage(emptyDirectMeta(1), emptyDirectMeta(1))
	for _, base := range []int{0, PageSize} {
		data[base+offset] = value
		rewriteMetaCRC(data[base : base+PageSize])
	}
	problem := requireBootstrapCode(t, openError(data, OpenImmutableReader), BootstrapErrNoBootstrapMeta)
	if problem.Meta0 != expected || problem.Meta1 != expected {
		t.Fatalf("identity findings = %d/%d, want %d", problem.Meta0, problem.Meta1, expected)
	}
}

func TestBootstrapIdentityFieldMatrixFailsClosed(t *testing.T) {
	assertIdentityProblem(t, 0, 'X', MetaProblemMagic)
	assertIdentityProblem(t, 8, 1, MetaProblemFixedValue)
	assertIdentityProblem(t, 10, 11, MetaProblemFixedValue)
	assertIdentityProblem(t, 11, 5, MetaProblemFixedValue)
	assertIdentityProblem(t, 12, 3, MetaProblemFixedValue)
	assertIdentityProblem(t, 13, 1, MetaProblemReserved)
	assertIdentityProblem(t, 184, 1, MetaProblemReserved)
	assertIdentityProblem(t, 256, 1, MetaProblemReserved)

	malformedTag := metaImage(emptyDirectMeta(1), emptyDirectMeta(1))
	for _, base := range []int{0, PageSize} {
		malformedTag[base+16] = 'a'
		malformedTag[base+17] = 0
		malformedTag[base+18] = 'x'
		rewriteMetaCRC(malformedTag[base : base+PageSize])
	}
	problem := requireBootstrapCode(t, openError(malformedTag, OpenImmutableReader), BootstrapErrNoBootstrapMeta)
	if problem.Meta0 != MetaProblemTag || problem.Meta1 != MetaProblemTag {
		t.Fatalf("tag findings = %d/%d", problem.Meta0, problem.Meta1)
	}

	zeroDatabaseID := emptyDirectMeta(1)
	zeroDatabaseID.DatabaseID = [16]byte{}
	problem = requireBootstrapCode(t, openError(metaImage(zeroDatabaseID, zeroDatabaseID), OpenImmutableReader), BootstrapErrNoBootstrapMeta)
	if problem.Meta0 != MetaProblemDatabaseID || problem.Meta1 != MetaProblemDatabaseID {
		t.Fatalf("database ID findings = %d/%d", problem.Meta0, problem.Meta1)
	}

	badCRC := metaImage(emptyDirectMeta(1), emptyDirectMeta(1))
	badCRC[MetaCRCOffset] ^= 1
	badCRC[PageSize+MetaCRCOffset] ^= 1
	problem = requireBootstrapCode(t, openError(badCRC, OpenImmutableReader), BootstrapErrNoBootstrapMeta)
	if problem.Meta0 != MetaProblemChecksum || problem.Meta1 != MetaProblemChecksum {
		t.Fatalf("CRC findings = %d/%d", problem.Meta0, problem.Meta1)
	}
}

func TestDynamicBootstrapFieldMatrixFailsClosed(t *testing.T) {
	cases := []struct {
		name     string
		meta     Meta
		expected MetaProblem
	}{
		{
			name: "zero transaction",
			meta: func() Meta {
				meta := emptyDirectMeta(1)
				meta.TxnID = 0
				return meta
			}(),
			expected: MetaProblemTransaction,
		},
		{
			name: "zero commit nonce",
			meta: func() Meta {
				meta := emptyDirectMeta(1)
				meta.CommitNonce = [16]byte{}
				return meta
			}(),
			expected: MetaProblemCommitNonce,
		},
		{
			name: "page count below minimum",
			meta: func() Meta {
				meta := emptyDirectMeta(1)
				meta.PageCount = 1
				return meta
			}(),
			expected: MetaProblemPageCount,
		},
		{
			name: "meta page as root",
			meta: func() Meta {
				meta := emptyDirectMeta(1)
				meta.RangeRoot = 1
				return meta
			}(),
			expected: MetaProblemRootBounds,
		},
		{
			name: "direct membership field",
			meta: func() Meta {
				meta := emptyDirectMeta(1)
				meta.ActiveFeedCount = 1
				return meta
			}(),
			expected: MetaProblemKindInvariant,
		},
		{
			name: "impossible retirement count",
			meta: func() Meta {
				meta := emptyDirectMeta(1)
				meta.RetirementBatchCount = 1
				return meta
			}(),
			expected: MetaProblemRetirementInvariant,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			problem := requireBootstrapCode(t, openError(metaImage(tc.meta, tc.meta), OpenImmutableReader), BootstrapErrNoBootstrapMeta)
			if problem.Meta0 != tc.expected || problem.Meta1 != tc.expected {
				t.Fatalf("findings = %d/%d, want %d", problem.Meta0, problem.Meta1, tc.expected)
			}
		})
	}

	truncated := emptyDirectMeta(1)
	truncated.PageCount = 3
	problem := requireBootstrapCode(t, openError(metaImage(truncated, truncated), OpenImmutableReader), BootstrapErrNoBootstrapMeta)
	if problem.Meta0 != MetaProblemPhysicalLength || problem.Meta1 != MetaProblemPhysicalLength {
		t.Fatalf("physical findings = %d/%d", problem.Meta0, problem.Meta1)
	}
}

func TestMetadataBootstrapEnforcesZlibWorstCaseBound(t *testing.T) {
	meta := emptyDirectMeta(1)
	meta.PageCount = 3
	meta.MetadataRoot = 2
	meta.MetadataCompressedLen = 12
	data := metaImage(meta, meta)
	data = append(data, make([]byte, PageSize)...)
	problem := requireBootstrapCode(t, openError(data, OpenImmutableReader), BootstrapErrNoBootstrapMeta)
	if problem.Meta0 != MetaProblemMetadataInvariant || problem.Meta1 != MetaProblemMetadataInvariant {
		t.Fatalf("metadata findings = %d/%d", problem.Meta0, problem.Meta1)
	}

	meta.MetadataCompressedLen = 11
	boundary := metaImage(meta, meta)
	boundary = append(boundary, make([]byte, PageSize)...)
	if _, err := Open(boundary, OpenImmutableReader); err != nil {
		t.Fatalf("maximum empty zlib stream bound rejected: %v", err)
	}
}
