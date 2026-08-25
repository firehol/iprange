// The FreeBSD ACL algorithm tests pin the exact libc acl(3) and kernel
// NFSv4-ACL semantics translated in acl_freebsd_algo.go. The NFSv4
// vectors were measured on FreeBSD 14.1-RELEASE (amd64, ZFS
// acltype=nfsv4) with libc acl_get_fd + acl_strip_np + acl_is_trivial_np
// for the eight modes below; the draft vector is the draft-ietf-nfsv4
// minorversion1 canonical-six form that libc acl_is_trivial_np
// accepted as trivial on the same host. The POSIX.1e vectors follow
// libc acl_strip.c / acl_calc_mask.c directly (the host root is ZFS,
// so the POSIX.1e brand is unit-tested only). These tests run on every
// host, including the linux development machine.

package security

import "testing"

// entry is one expected fbsdACLEntry in a vector.
func entry(tag, perm uint32, entryType uint16) fbsdACLEntry {
	return fbsdACLEntry{Tag: tag, ID: fbsdUndefinedID, Perm: perm, EntryType: entryType}
}

// wantACL builds the expected ACL from entries.
func wantACL(entries ...fbsdACLEntry) fbsdACL {
	var acl fbsdACL
	acl.MaxCnt = fbsdMaxEntries
	for _, e := range entries {
		acl.Entries[acl.Cnt] = e
		acl.Cnt++
	}
	return acl
}

// TestFreeBSDPSARCVectors pins the PSARC/2010/029 trivial NFSv4 ACLs
// for eight modes, measured with libc acl_strip_np on FreeBSD 14.1
// (permission values: 0x9240 is the base read set
// READ_ACL|READ_ATTRIBUTES|READ_NAMED_ATTRS|SYNCHRONIZE, 0xf6c0 adds
// WRITE_ACL|WRITE_OWNER|WRITE_ATTRIBUTES|WRITE_NAMED_ATTRS, and the
// mode bits add READ_DATA 0x8, WRITE_DATA|APPEND_DATA 0x30 and
// EXECUTE 0x1).
func TestFreeBSDPSARCVectors(t *testing.T) {
	const (
		base      = uint32(fbsdPermReadACL | fbsdPermReadAttrs | fbsdPermReadNamed | fbsdPermSync)
		userBase  = uint32(base | fbsdPermWriteACL | fbsdPermWriteOwner | fbsdPermWriteAttrs | fbsdPermWriteNamed)
		readWrite = uint32(fbsdPermReadData | fbsdPermWriteData | fbsdPermAppendData)
	)
	all := uint32(fbsdPermReadData | fbsdPermWriteData | fbsdPermAppendData | fbsdPermExecute)
	cases := []struct {
		mode uint32
		want fbsdACL
	}{
		{0o000, wantACL(entry(fbsdTagUserObj, userBase, fbsdEntryTypeAllow), entry(fbsdTagGroupObj, base, fbsdEntryTypeAllow), entry(fbsdTagEveryone, base, fbsdEntryTypeAllow))},
		{0o001, wantACL(entry(fbsdTagUserObj, fbsdPermExecute, fbsdEntryTypeDeny), entry(fbsdTagGroupObj, fbsdPermExecute, fbsdEntryTypeDeny), entry(fbsdTagUserObj, userBase, fbsdEntryTypeAllow), entry(fbsdTagGroupObj, base, fbsdEntryTypeAllow), entry(fbsdTagEveryone, base|fbsdPermExecute, fbsdEntryTypeAllow))},
		{0o600, wantACL(entry(fbsdTagUserObj, userBase|readWrite, fbsdEntryTypeAllow), entry(fbsdTagGroupObj, base, fbsdEntryTypeAllow), entry(fbsdTagEveryone, base, fbsdEntryTypeAllow))},
		{0o644, wantACL(entry(fbsdTagUserObj, userBase|readWrite, fbsdEntryTypeAllow), entry(fbsdTagGroupObj, base|fbsdPermReadData, fbsdEntryTypeAllow), entry(fbsdTagEveryone, base|fbsdPermReadData, fbsdEntryTypeAllow))},
		{0o755, wantACL(entry(fbsdTagUserObj, userBase|readWrite|fbsdPermExecute, fbsdEntryTypeAllow), entry(fbsdTagGroupObj, base|fbsdPermReadData|fbsdPermExecute, fbsdEntryTypeAllow), entry(fbsdTagEveryone, base|fbsdPermReadData|fbsdPermExecute, fbsdEntryTypeAllow))},
		{0o777, wantACL(entry(fbsdTagUserObj, userBase|readWrite|fbsdPermExecute, fbsdEntryTypeAllow), entry(fbsdTagGroupObj, base|all, fbsdEntryTypeAllow), entry(fbsdTagEveryone, base|all, fbsdEntryTypeAllow))},
		{0o140, wantACL(entry(fbsdTagUserObj, fbsdPermReadData, fbsdEntryTypeDeny), entry(fbsdTagUserObj, userBase|fbsdPermExecute, fbsdEntryTypeAllow), entry(fbsdTagGroupObj, base|fbsdPermReadData, fbsdEntryTypeAllow), entry(fbsdTagEveryone, base, fbsdEntryTypeAllow))},
		{0o601, wantACL(entry(fbsdTagUserObj, fbsdPermExecute, fbsdEntryTypeDeny), entry(fbsdTagGroupObj, fbsdPermExecute, fbsdEntryTypeDeny), entry(fbsdTagUserObj, userBase|readWrite, fbsdEntryTypeAllow), entry(fbsdTagGroupObj, base, fbsdEntryTypeAllow), entry(fbsdTagEveryone, base|fbsdPermExecute, fbsdEntryTypeAllow))},
	}
	for _, c := range cases {
		var acl fbsdACL
		acl.MaxCnt = fbsdMaxEntries
		fbsdNFS4TrivialPSARC(&acl, c.mode)
		if !fbsdACLsEqual(&acl, &c.want) {
			t.Fatalf("psarc(%04o) = %+v, want %+v", c.mode, acl, c.want)
		}
	}
}

// TestFreeBSDDraftVector pins the canonical-six draft NFSv4 ACL of mode
// 0600 (acl_nfs4_sync_acl_from_mode_draft with an empty ACL), measured
// on FreeBSD 14.1: libc acl_is_trivial_np accepted exactly this entry
// list as trivial. The write set is WRITE_ACL|WRITE_OWNER|
// WRITE_ATTRIBUTES|WRITE_NAMED_ATTRS (0x6480) and the read set is the
// 0x9240 base.
func TestFreeBSDDraftVector(t *testing.T) {
	const (
		writeSet = uint32(fbsdPermWriteACL | fbsdPermWriteOwner | fbsdPermWriteAttrs | fbsdPermWriteNamed)
		readSet  = uint32(fbsdPermReadACL | fbsdPermReadAttrs | fbsdPermReadNamed | fbsdPermSync)
		all      = uint32(fbsdPermReadData | fbsdPermWriteData | fbsdPermAppendData | fbsdPermExecute)
	)
	var got fbsdACL
	fbsdNFS4TrivialFromMode(&got, 0o600, true)
	want := wantACL(
		entry(fbsdTagUserObj, fbsdPermExecute, fbsdEntryTypeDeny),
		entry(fbsdTagUserObj, writeSet|fbsdPermReadData|fbsdPermWriteData|fbsdPermAppendData, fbsdEntryTypeAllow),
		entry(fbsdTagGroupObj, all, fbsdEntryTypeDeny),
		entry(fbsdTagGroupObj, 0, fbsdEntryTypeAllow),
		entry(fbsdTagEveryone, writeSet|all, fbsdEntryTypeDeny),
		entry(fbsdTagEveryone, readSet, fbsdEntryTypeAllow),
	)
	if !fbsdACLsEqual(&got, &want) {
		t.Fatalf("draft(0600) = %+v, want %+v", got, want)
	}
}

// TestFreeBSDNFS4SyncModeRoundTrip pins acl_nfs4_sync_mode_from_acl
// over the trivial forms: rebuilding the mode from the PSARC or draft
// form recovers the original mode for every measured vector.
func TestFreeBSDNFS4SyncModeRoundTrip(t *testing.T) {
	modes := []uint32{0o000, 0o001, 0o600, 0o644, 0o755, 0o777, 0o140, 0o601}
	for _, mode := range modes {
		var psarc fbsdACL
		fbsdNFS4TrivialFromMode(&psarc, mode, false)
		if got := fbsdNFS4SyncMode(&psarc); got != mode {
			t.Fatalf("sync(psarc(%04o)) = %04o", mode, got)
		}
		var draft fbsdACL
		fbsdNFS4TrivialFromMode(&draft, mode, true)
		if got := fbsdNFS4SyncMode(&draft); got != mode {
			t.Fatalf("sync(draft(%04o)) = %04o", mode, got)
		}
	}
}

// TestFreeBSDNFS4Triviality pins the libc acl_is_trivial_np decisions:
// every PSARC and draft form is trivial, a nontrivial ACL (an extra
// named-user deny entry) is not, and a seven-entry ACL is never
// trivial even when seven entries happen to express the mode.
func TestFreeBSDNFS4Triviality(t *testing.T) {
	for _, mode := range []uint32{0o000, 0o001, 0o600, 0o644, 0o777} {
		var psarc fbsdACL
		fbsdNFS4TrivialFromMode(&psarc, mode, false)
		if !fbsdNFS4Trivial(&psarc) {
			t.Fatalf("psarc(%04o) must be trivial", mode)
		}
		var draft fbsdACL
		fbsdNFS4TrivialFromMode(&draft, mode, true)
		if !fbsdNFS4Trivial(&draft) {
			t.Fatalf("draft(%04o) must be trivial", mode)
		}
	}

	base := wantACL(entry(fbsdTagUserObj, fbsdPermReadACL|fbsdPermWriteACL|fbsdPermReadAttrs|fbsdPermWriteAttrs|fbsdPermReadNamed|fbsdPermWriteNamed|fbsdPermSync|fbsdPermReadData|fbsdPermWriteData|fbsdPermAppendData, fbsdEntryTypeAllow))
	nontrivial := base
	nontrivial.Entries[nontrivial.Cnt] = fbsdACLEntry{Tag: fbsdTagUser, ID: 1337, Perm: fbsdPermReadData, EntryType: fbsdEntryTypeDeny}
	nontrivial.Cnt++
	if fbsdNFS4Trivial(&nontrivial) {
		t.Fatalf("nontrivial ACL must not be trivial")
	}
	var got fbsdACL
	fbsdStrip(&nontrivial, fbsdBrandNFS4, &got)
	if !fbsdNFS4Trivial(&got) {
		t.Fatalf("stripped nontrivial ACL must be trivial: %+v", got)
	}

	seven := base
	for i := 0; i < 4; i++ {
		seven.Entries[seven.Cnt] = fbsdACLEntry{Tag: fbsdTagGroup, ID: uint32(2000 + i), Perm: fbsdPermReadData, EntryType: fbsdEntryTypeDeny}
		seven.Cnt++
	}
	if fbsdNFS4Trivial(&seven) {
		t.Fatalf("seven-entry ACL must not be trivial")
	}
}

// TestFreeBSDPOSIXStripMaskRecalc pins libc acl_strip_np for the
// POSIX.1e brand: base user/group/other entries survive, named and
// mask entries drop, and the mask is recalculated as the union of the
// group-class permissions (acl_calc_mask) then appended.
func TestFreeBSDPOSIXStripMaskRecalc(t *testing.T) {
	acl := wantACL(
		entry(fbsdTagUserObj, fbsdPermRead|fbsdPermWrite, fbsdEntryTypeAllow),
		fbsdACLEntry{Tag: fbsdTagUser, ID: 1000, Perm: fbsdPermRead | fbsdPermWrite, EntryType: fbsdEntryTypeAllow},
		entry(fbsdTagGroupObj, fbsdPermRead, fbsdEntryTypeAllow),
		fbsdACLEntry{Tag: fbsdTagGroup, ID: 2000, Perm: fbsdPermRead, EntryType: fbsdEntryTypeAllow},
		entry(fbsdTagMask, fbsdPermRead|fbsdPermWrite|fbsdPermExecute, fbsdEntryTypeAllow),
		entry(fbsdTagOther, 0, fbsdEntryTypeAllow),
	)
	var got fbsdACL
	fbsdPOSIXStrip(&acl, &got)
	want := wantACL(
		entry(fbsdTagUserObj, fbsdPermRead|fbsdPermWrite, fbsdEntryTypeAllow),
		entry(fbsdTagGroupObj, fbsdPermRead, fbsdEntryTypeAllow),
		entry(fbsdTagOther, 0, fbsdEntryTypeAllow),
		fbsdACLEntry{Tag: fbsdTagMask, ID: fbsdUndefinedID, Perm: fbsdPermRead},
	)
	if !fbsdACLsEqual(&got, &want) {
		t.Fatalf("posix strip = %+v, want %+v", got, want)
	}
}

// TestFreeBSDPOSIXStripWithoutMask keeps the base entries without
// appending a mask when the original had none (libc acl_calc_mask is
// skipped when have_mask_entry is false).
func TestFreeBSDPOSIXStripWithoutMask(t *testing.T) {
	acl := wantACL(
		entry(fbsdTagUserObj, fbsdPermRead|fbsdPermWrite, fbsdEntryTypeAllow),
		entry(fbsdTagGroupObj, fbsdPermRead, fbsdEntryTypeAllow),
		entry(fbsdTagOther, 0, fbsdEntryTypeAllow),
	)
	var got fbsdACL
	fbsdPOSIXStrip(&acl, &got)
	if !fbsdACLsEqual(&got, &acl) {
		t.Fatalf("strip without mask changed the ACL: %+v", got)
	}
}

// TestFreeBSDPOSIXSort pins the libc _posix1e_acl_sort canonical order:
// tag ascending (user_obj, group_obj, mask, other), id ascending for
// the named user and group entries.
func TestFreeBSDPOSIXSort(t *testing.T) {
	acl := wantACL(
		entry(fbsdTagUserObj, fbsdPermRead, fbsdEntryTypeAllow),
		entry(fbsdTagOther, 0, fbsdEntryTypeAllow),
		entry(fbsdTagMask, fbsdPermRead, fbsdEntryTypeAllow),
		entry(fbsdTagGroupObj, fbsdPermRead, fbsdEntryTypeAllow),
		fbsdACLEntry{Tag: fbsdTagGroup, ID: 2000, Perm: fbsdPermRead, EntryType: fbsdEntryTypeAllow},
		fbsdACLEntry{Tag: fbsdTagGroup, ID: 1000, Perm: fbsdPermRead, EntryType: fbsdEntryTypeAllow},
	)
	fbsdPOSIXSort(&acl)
	want := wantACL(
		entry(fbsdTagUserObj, fbsdPermRead, fbsdEntryTypeAllow),
		entry(fbsdTagGroupObj, fbsdPermRead, fbsdEntryTypeAllow),
		fbsdACLEntry{Tag: fbsdTagGroup, ID: 1000, Perm: fbsdPermRead, EntryType: fbsdEntryTypeAllow},
		fbsdACLEntry{Tag: fbsdTagGroup, ID: 2000, Perm: fbsdPermRead, EntryType: fbsdEntryTypeAllow},
		entry(fbsdTagMask, fbsdPermRead, fbsdEntryTypeAllow),
		entry(fbsdTagOther, 0, fbsdEntryTypeAllow),
	)
	if !fbsdACLsEqual(&acl, &want) {
		t.Fatalf("sorted = %+v, want %+v", acl, want)
	}
}

// TestFreeBSDPOSIXTrivial pins the libc acl_is_trivial_np POSIX.1e
// decision: exactly three base entries are trivial, a mask entry is
// not.
func TestFreeBSDPOSIXTrivial(t *testing.T) {
	base := wantACL(
		entry(fbsdTagUserObj, fbsdPermRead|fbsdPermWrite, fbsdEntryTypeAllow),
		entry(fbsdTagGroupObj, fbsdPermRead, fbsdEntryTypeAllow),
		entry(fbsdTagOther, 0, fbsdEntryTypeAllow),
	)
	if !fbsdTrivial(&base, fbsdBrandPOSIX) {
		t.Fatalf("3-entry POSIX ACL must be trivial")
	}
	withMask := base
	withMask.Entries[withMask.Cnt] = entry(fbsdTagMask, fbsdPermRead, fbsdEntryTypeAllow)
	withMask.Cnt++
	if fbsdTrivial(&withMask, fbsdBrandPOSIX) {
		t.Fatalf("4-entry POSIX ACL must not be trivial")
	}
}

// TestFreeBSDStripRoundTrip pins fbsdStrip over both brands: the output
// is always trivial for its brand (the libc acl_strip_np contract).
func TestFreeBSDStripRoundTrip(t *testing.T) {
	for _, mode := range []uint32{0o000, 0o001, 0o600, 0o644, 0o755, 0o777} {
		var psarc fbsdACL
		fbsdNFS4TrivialFromMode(&psarc, mode, false)
		var stripped fbsdACL
		fbsdStrip(&psarc, fbsdBrandNFS4, &stripped)
		if !fbsdNFS4Trivial(&stripped) {
			t.Fatalf("strip(psarc(%04o)) = %+v, must be trivial", mode, stripped)
		}
	}
	// A masked POSIX.1e ACL strips to the base entries plus the
	// recalculated mask (libc acl_calc_mask): four entries, so the
	// libc trivial test (exactly three entries) says not trivial.
	// The mask permeability mirrors the Rust machine exactly.
	posix := wantACL(
		entry(fbsdTagUserObj, fbsdPermRead|fbsdPermWrite, fbsdEntryTypeAllow),
		entry(fbsdTagGroupObj, fbsdPermRead, fbsdEntryTypeAllow),
		entry(fbsdTagOther, 0, fbsdEntryTypeAllow),
		entry(fbsdTagMask, fbsdPermRead, fbsdEntryTypeAllow),
	)
	var stripped fbsdACL
	fbsdStrip(&posix, fbsdBrandPOSIX, &stripped)
	want := wantACL(
		entry(fbsdTagUserObj, fbsdPermRead|fbsdPermWrite, fbsdEntryTypeAllow),
		entry(fbsdTagGroupObj, fbsdPermRead, fbsdEntryTypeAllow),
		entry(fbsdTagOther, 0, fbsdEntryTypeAllow),
		fbsdACLEntry{Tag: fbsdTagMask, ID: fbsdUndefinedID, Perm: fbsdPermRead},
	)
	if !fbsdACLsEqual(&stripped, &want) {
		t.Fatalf("strip(posix) = %+v, want %+v", stripped, want)
	}
	if fbsdTrivial(&stripped, fbsdBrandPOSIX) {
		t.Fatalf("masked POSIX strip output must stay nontrivial (four entries)")
	}
	// The unmasked three-entry form strips to itself and is trivial.
	plain := wantACL(
		entry(fbsdTagUserObj, fbsdPermRead|fbsdPermWrite, fbsdEntryTypeAllow),
		entry(fbsdTagGroupObj, fbsdPermRead, fbsdEntryTypeAllow),
		entry(fbsdTagOther, 0, fbsdEntryTypeAllow),
	)
	var plainStripped fbsdACL
	fbsdStrip(&plain, fbsdBrandPOSIX, &plainStripped)
	if !fbsdACLsEqual(&plainStripped, &plain) || !fbsdTrivial(&plainStripped, fbsdBrandPOSIX) {
		t.Fatalf("strip(plain posix) = %+v, must stay trivial", plainStripped)
	}
}
