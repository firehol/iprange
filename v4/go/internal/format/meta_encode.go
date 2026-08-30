// Meta page encoding (Rust contract.rs MetaV4::encode_mapped). The meta
// page is written directly into a mapped view at its final offset: every
// field at its wire offset, the reserved bytes zero, then the exact
// meta CRC over the zeroed checksum field. No owned page buffer exists.

package format

import "github.com/firehol/iprange/v4/go/internal/work"

// EncodeMapped writes this meta into one complete mapped page and seals
// the meta CRC (Rust MetaV4::encode_mapped: encode_fields then
// crc32c_page_mut_with_zeroed at META_CRC_OFFSET). The caller provides
// the mapped page of the alternate meta slot.
func (m *Meta) EncodeMapped(page []byte) error {
	if len(page) != PageSize {
		return headerErr("meta page not a complete page")
	}
	clear(page) // Rust encode_fields page.fill(0): zero the mapped page in place
	work.BytesZeroed(PageSize)
	copy(page[0:8], MainMagic[:])
	PutU16(page[8:10], MetaSize)
	page[10] = PageShift
	page[11] = m.AddressFamily
	page[12] = m.ValueKind
	page[13] = m.StructureKind
	copy(page[16:32], m.ValueTag[:])
	copy(page[32:48], m.DatabaseID[:])
	PutU64(page[48:56], m.TxnID)
	copy(page[56:72], m.CommitNonce[:])
	PutU64(page[72:80], m.PageCount)
	PutU64(page[80:88], m.RangeRecordCount)
	PutU64(page[88:96], m.ActiveFeedCount)
	PutU64(page[96:104], m.FeedIndexLimit)
	PutU64(page[104:112], m.MembershipEntryCount)
	PutU64(page[112:120], m.MembershipIDLimit)
	PutU64(page[120:128], m.MetadataUncompressed)
	PutU64(page[128:136], m.MetadataCompressed)
	PutU64(page[136:144], m.RetiredExtentCount)
	PutU32(page[144:148], m.RangeRoot)
	PutU32(page[148:152], m.CatalogNameRoot)
	PutU32(page[152:156], m.CatalogIndexRoot)
	PutU32(page[156:160], m.FeedUsedRoot)
	PutU32(page[160:164], m.MembershipIDRoot)
	PutU32(page[164:168], m.MembershipHashRoot)
	PutU32(page[168:172], m.MembershipUsedRoot)
	PutU32(page[172:176], m.MetadataRoot)
	PutU32(page[176:180], m.FreeBitmapRoot)
	PutU32(page[180:184], m.RetirementRoot)
	for i := 0; i < 4; i++ {
		PutU32(page[184+4*i:188+4*i], m.AllocatorReserve[i])
	}
	PutU64(page[200:208], m.StructureEntryCount)
	PutU64(page[208:216], m.StructureIDLimit)
	PutU32(page[216:220], m.StructureIDRoot)
	PutU32(page[220:224], m.StructureHashRoot)
	PutU32(page[224:228], m.StructureUsedRoot)
	// Bytes [228,256) are the reserved tail plus the checksum field: the
	// reserved bytes stay zero and the checksum is computed with the
	// field itself zeroed (MetaCRC32C), then stored.
	PutU32(page[252:256], MetaCRC32C(page))
	// Rust counts every field write through PageMut (contract.rs
	// MetaV4::encode_fields): 230 moved bytes per encode - magic 8 +
	// size 2 + 4 tag/kind bytes + value tag 16 + database id 16 + txn 8 +
	// nonce 16 + 9x u64 (72) + 10x u32 roots (40) + allocator reserve 16
	// + structure 2x u64 (16) + 3x u32 (12) + CRC 4 - and the page fill
	// above counts 4096 zeroed (Rust page.fill(0)).
	work.BytesMoved(230)
	return nil
}
