// FreeBSD ACL machine algorithms (shared, platform-independent): the
// exact libc acl(3) and NFSv4-ACL semantics the creator-only machine
// needs, translated from the FreeBSD authorities (lib/libc/posix1e
// acl_strip.c, acl_calc_mask.c and sys/kern/subr_acl_nfs4.c). The
// definitions are ABI-exact so the freebsd syscall layer can pass the
// struct to __acl_get_fd/__acl_set_fd and the algorithms can run on
// the host and in unit tests everywhere.

package security

// fbsdTag values (sys/acl.h): POSIX.1e and NFSv4 tags share the
// same numeric space; the NFSv4 machine uses the USER_OBJ, GROUP_OBJ
// and EVERYONE members.
const (
	fbsdTagUndefined uint32 = 0x00000000
	fbsdTagUserObj   uint32 = 0x00000001
	fbsdTagUser      uint32 = 0x00000002
	fbsdTagGroupObj  uint32 = 0x00000004
	fbsdTagGroup     uint32 = 0x00000008
	fbsdTagMask      uint32 = 0x00000010
	fbsdTagOther     uint32 = 0x00000020
	fbsdTagEveryone  uint32 = 0x00000040
	fbsdUndefinedID  uint32 = 0xffffffff
	fbsdMaxEntries   uint32 = 254
)

// fbsdACLEntryType values (sys/acl.h; valid for NFSv4 ACLs).
const (
	fbsdEntryTypeAllow uint16 = 0x0100
	fbsdEntryTypeDeny  uint16 = 0x0200
)

// fbsdFlag values (sys/acl.h; valid for NFSv4 ACLs).
const (
	fbsdFlagFileInherit uint16 = 0x0001
	fbsdFlagDirInherit  uint16 = 0x0002
	fbsdFlagNoPropagate uint16 = 0x0004
	fbsdFlagInheritOnly uint16 = 0x0008
)

// fbsdPerm values (sys/acl.h): the NFSv4 mask bits, plus the
// POSIX.1e low three bits (READ 4, WRITE 2, EXECUTE 1) and the
// POSIX.1e WRITE_DATA/APPEND aliases.
const (
	fbsdPermExecute     uint32 = 0x00000001
	fbsdPermWrite       uint32 = 0x00000002
	fbsdPermRead        uint32 = 0x00000004
	fbsdPermReadData    uint32 = 0x00000008
	fbsdPermWriteData   uint32 = 0x00000010
	fbsdPermAppendData  uint32 = 0x00000020
	fbsdPermReadNamed   uint32 = 0x00000040
	fbsdPermWriteNamed  uint32 = 0x00000080
	fbsdPermDeleteChild uint32 = 0x00000100
	fbsdPermReadAttrs   uint32 = 0x00000200
	fbsdPermWriteAttrs  uint32 = 0x00000400
	fbsdPermDelete      uint32 = 0x00000800
	fbsdPermReadACL     uint32 = 0x00001000
	fbsdPermWriteACL    uint32 = 0x00002000
	fbsdPermWriteOwner  uint32 = 0x00004000
	fbsdPermSync        uint32 = 0x00008000
	fbsdPermBits        uint32 = fbsdPermExecute | fbsdPermWrite | fbsdPermRead
)

// fbsdACLEntry is one ACL entry in the kernel ABI layout
// (sys/acl.h "Current struct acl_entry"). The field order and widths
// are fixed: tag, id, perm, entry type, flags.
type fbsdACLEntry struct {
	Tag       uint32
	ID        uint32
	Perm      uint32
	EntryType uint16
	Flags     uint16
}

// fbsdACL is one access ACL in the kernel ABI layout (sys/acl.h
// "Current struct acl"): the entry array is fixed at the kernel
// maximum, and Cnt names the valid prefix.
type fbsdACL struct {
	MaxCnt  uint32
	Cnt     uint32
	Spare   [4]int32
	Entries [fbsdMaxEntries]fbsdACLEntry
}

// fbsdACLBrand selects the ACL semantics of one filesystem
// (libc acl_get_fd: fpathconf(_PC_ACL_NFS4) == 1 picks the NFSv4
// brand, everything else POSIX.1e).
type fbsdACLBrand int

const (
	// fbsdBrandPOSIX is the POSIX.1e brand (UFS and other POSIX ACL
	// filesystems).
	fbsdBrandPOSIX fbsdACLBrand = iota
	// fbsdBrandNFS4 is the NFSv4 brand (ZFS with nfsv4acls).
	fbsdBrandNFS4
)

// fbsdACLsEqual compares two ACLs entry by entry over all five fields
// (libc _acl_differs and kernel _acls_are_equal).
func fbsdACLsEqual(a, b *fbsdACL) bool {
	if a.Cnt != b.Cnt {
		return false
	}
	for i := uint32(0); i < a.Cnt; i++ {
		ea, eb := &a.Entries[i], &b.Entries[i]
		if ea.Tag != eb.Tag || ea.ID != eb.ID || ea.Perm != eb.Perm ||
			ea.EntryType != eb.EntryType || ea.Flags != eb.Flags {
			return false
		}
	}
	return true
}

// fbsdTrivial reports whether one ACL is trivial for its brand (libc
// acl_is_trivial_np): a POSIX.1e ACL is trivial exactly when it holds
// the three base entries; an NFSv4 ACL is trivial when its meaning
// fits the file mode (the PSARC trivial form, or the canonical-six
// draft form).
func fbsdTrivial(acl *fbsdACL, brand fbsdACLBrand) bool {
	if brand == fbsdBrandPOSIX {
		return acl.Cnt == 3
	}
	return fbsdNFS4Trivial(acl)
}

// fbsdStrip removes every extended entry and writes the trivial form
// of one ACL into out for its brand (libc acl_strip_np). The POSIX.1e
// arm keeps the base user/group/other entries and recalculates the mask
// entry when one existed (the Rust machine always passes the recalculate
// flag); the NFSv4 arm rebuilds the trivial PSARC form from the mode
// the ACL expresses.
func fbsdStrip(acl *fbsdACL, brand fbsdACLBrand, out *fbsdACL) {
	if brand == fbsdBrandPOSIX {
		fbsdPOSIXStrip(acl, out)
		return
	}
	mode := fbsdNFS4SyncMode(acl)
	fbsdNFS4TrivialFromMode(out, mode, false)
}

// fbsdPOSIXStrip keeps ACL_USER_OBJ, ACL_GROUP_OBJ and ACL_OTHER and
// writes them into out, dropping every named and mask entry; when the
// original carried a mask entry the mask is recalculated exactly like
// libc acl_strip_np: acl_calc_mask runs over the already-stripped ACL,
// so only the surviving group-class entries (the GROUP_OBJ entry)
// contribute, and the mask is appended as the last entry like the libc
// append. out must not alias acl (the base entries are copied before
// the mask recalc reads them).
func fbsdPOSIXStrip(acl *fbsdACL, out *fbsdACL) {
	out.MaxCnt = fbsdMaxEntries
	out.Cnt = 0
	hadMask := false
	for i := uint32(0); i < acl.Cnt; i++ {
		entry := acl.Entries[i]
		switch entry.Tag {
		case fbsdTagUserObj, fbsdTagGroupObj, fbsdTagOther:
			out.Entries[out.Cnt] = entry
			out.Cnt++
		case fbsdTagMask:
			hadMask = true
		}
	}
	if hadMask {
		maskMode := uint32(0)
		for i := uint32(0); i < out.Cnt; i++ {
			switch out.Entries[i].Tag {
			case fbsdTagUser, fbsdTagGroup, fbsdTagGroupObj:
				maskMode |= out.Entries[i].Perm & fbsdPermBits
			}
		}
		out.Entries[out.Cnt] = fbsdACLEntry{
			Tag:  fbsdTagMask,
			ID:   fbsdUndefinedID,
			Perm: maskMode,
		}
		out.Cnt++
	}
}

// fbsdNFS4Trivial runs the libc NFSv4 trivial test: an ACL with more
// than six entries is never trivial; otherwise the mode expressed by
// the ACL is rebuilt as the PSARC trivial form and, when that differs,
// as the canonical-six draft form, and the original must equal one of
// them (acl_strip.c acl_is_trivial_np).
func fbsdNFS4Trivial(acl *fbsdACL) bool {
	if acl.Cnt > 6 {
		return false
	}
	mode := fbsdNFS4SyncMode(acl)
	var psarc fbsdACL
	fbsdNFS4TrivialFromMode(&psarc, mode, false)
	if fbsdACLsEqual(acl, &psarc) {
		return true
	}
	var draft fbsdACL
	fbsdNFS4TrivialFromMode(&draft, mode, true)
	return fbsdACLsEqual(acl, &draft)
}

// fbsdNFS4SyncMode computes the file mode expressed by one NFSv4 ACL
// (subr_acl_nfs4.c acl_nfs4_sync_mode_from_acl with an all-zero
// starting mode: the first permission observed per file-mode bit
// decides, with everyone entries feeding all three classes and allow
// entries setting the bit, deny entries leaving it clear).
func fbsdNFS4SyncMode(acl *fbsdACL) uint32 {
	const (
		userRead   = uint32(0o400)
		userWrite  = uint32(0o200)
		userExec   = uint32(0o100)
		groupRead  = uint32(0o040)
		groupWrite = uint32(0o020)
		groupExec  = uint32(0o010)
		otherRead  = uint32(0o004)
		otherWrite = uint32(0o002)
		otherExec  = uint32(0o001)
	)
	var mode, seen uint32
	for i := uint32(0); i < acl.Cnt; i++ {
		entry := &acl.Entries[i]
		if entry.EntryType != fbsdEntryTypeAllow && entry.EntryType != fbsdEntryTypeDeny {
			continue
		}
		if entry.Flags&fbsdFlagInheritOnly != 0 {
			continue
		}
		switch entry.Tag {
		case fbsdTagUserObj:
			if entry.Perm&fbsdPermReadData != 0 && seen&userRead == 0 {
				seen |= userRead
				if entry.EntryType == fbsdEntryTypeAllow {
					mode |= userRead
				}
			}
			if entry.Perm&fbsdPermWriteData != 0 && seen&userWrite == 0 {
				seen |= userWrite
				if entry.EntryType == fbsdEntryTypeAllow {
					mode |= userWrite
				}
			}
			if entry.Perm&fbsdPermExecute != 0 && seen&userExec == 0 {
				seen |= userExec
				if entry.EntryType == fbsdEntryTypeAllow {
					mode |= userExec
				}
			}
		case fbsdTagGroupObj:
			if entry.Perm&fbsdPermReadData != 0 && seen&groupRead == 0 {
				seen |= groupRead
				if entry.EntryType == fbsdEntryTypeAllow {
					mode |= groupRead
				}
			}
			if entry.Perm&fbsdPermWriteData != 0 && seen&groupWrite == 0 {
				seen |= groupWrite
				if entry.EntryType == fbsdEntryTypeAllow {
					mode |= groupWrite
				}
			}
			if entry.Perm&fbsdPermExecute != 0 && seen&groupExec == 0 {
				seen |= groupExec
				if entry.EntryType == fbsdEntryTypeAllow {
					mode |= groupExec
				}
			}
		case fbsdTagEveryone:
			if entry.Perm&fbsdPermReadData != 0 {
				if seen&userRead == 0 {
					seen |= userRead
					if entry.EntryType == fbsdEntryTypeAllow {
						mode |= userRead
					}
				}
				if seen&groupRead == 0 {
					seen |= groupRead
					if entry.EntryType == fbsdEntryTypeAllow {
						mode |= groupRead
					}
				}
				if seen&otherRead == 0 {
					seen |= otherRead
					if entry.EntryType == fbsdEntryTypeAllow {
						mode |= otherRead
					}
				}
			}
			if entry.Perm&fbsdPermWriteData != 0 {
				if seen&userWrite == 0 {
					seen |= userWrite
					if entry.EntryType == fbsdEntryTypeAllow {
						mode |= userWrite
					}
				}
				if seen&groupWrite == 0 {
					seen |= groupWrite
					if entry.EntryType == fbsdEntryTypeAllow {
						mode |= groupWrite
					}
				}
				if seen&otherWrite == 0 {
					seen |= otherWrite
					if entry.EntryType == fbsdEntryTypeAllow {
						mode |= otherWrite
					}
				}
			}
			if entry.Perm&fbsdPermExecute != 0 {
				if seen&userExec == 0 {
					seen |= userExec
					if entry.EntryType == fbsdEntryTypeAllow {
						mode |= userExec
					}
				}
				if seen&groupExec == 0 {
					seen |= groupExec
					if entry.EntryType == fbsdEntryTypeAllow {
						mode |= groupExec
					}
				}
				if seen&otherExec == 0 {
					seen |= otherExec
					if entry.EntryType == fbsdEntryTypeAllow {
						mode |= otherExec
					}
				}
			}
		}
	}
	return mode
}

// fbsdNFS4TrivialFromMode builds the trivial NFSv4 ACL of one file
// mode into acl (subr_acl_nfs4.c acl_nfs4_trivial_from_mode_libc). The
// PSARC form (canonicalSix false) emits the allow/deny owner, group
// and everyone entries derived from the mode; the canonical-six form
// (canonicalSix true) emits the six fixed entries of the NFSv4 draft
// and distributes the mode bits between the deny and allow members.
func fbsdNFS4TrivialFromMode(acl *fbsdACL, mode uint32, canonicalSix bool) {
	acl.MaxCnt = fbsdMaxEntries
	acl.Cnt = 0
	if !canonicalSix {
		fbsdNFS4TrivialPSARC(acl, mode)
		return
	}
	fbsdNFS4TrivialDraft(acl, mode)
}

// fbsdNFS4TrivialPSARC computes the PSARC/2010/029 trivial ACL of one
// mode (subr_acl_nfs4.c acl_nfs4_compute_inherited_acl_psarc with a
// nil parent).
func fbsdNFS4TrivialPSARC(acl *fbsdACL, mode uint32) {
	base := fbsdPermReadACL | fbsdPermReadAttrs | fbsdPermReadNamed | fbsdPermSync
	userAllow := base
	groupAllow := base
	everyoneAllow := base
	userAllow |= fbsdPermWriteACL | fbsdPermWriteOwner | fbsdPermWriteAttrs | fbsdPermWriteNamed

	if mode&0o400 != 0 {
		userAllow |= fbsdPermReadData
	}
	if mode&0o200 != 0 {
		userAllow |= fbsdPermWriteData | fbsdPermAppendData
	}
	if mode&0o100 != 0 {
		userAllow |= fbsdPermExecute
	}
	if mode&0o040 != 0 {
		groupAllow |= fbsdPermReadData
	}
	if mode&0o020 != 0 {
		groupAllow |= fbsdPermWriteData | fbsdPermAppendData
	}
	if mode&0o010 != 0 {
		groupAllow |= fbsdPermExecute
	}
	if mode&0o004 != 0 {
		everyoneAllow |= fbsdPermReadData
	}
	if mode&0o002 != 0 {
		everyoneAllow |= fbsdPermWriteData | fbsdPermAppendData
	}
	if mode&0o001 != 0 {
		everyoneAllow |= fbsdPermExecute
	}

	userDeny := (groupAllow | everyoneAllow) &^ userAllow
	groupDeny := everyoneAllow &^ groupAllow
	userAllowFirst := groupDeny &^ userDeny

	if userAllowFirst != 0 {
		fbsdAppendEntry(acl, fbsdTagUserObj, userAllowFirst, fbsdEntryTypeAllow)
	}
	if userDeny != 0 {
		fbsdAppendEntry(acl, fbsdTagUserObj, userDeny, fbsdEntryTypeDeny)
	}
	if groupDeny != 0 {
		fbsdAppendEntry(acl, fbsdTagGroupObj, groupDeny, fbsdEntryTypeDeny)
	}
	fbsdAppendEntry(acl, fbsdTagUserObj, userAllow, fbsdEntryTypeAllow)
	fbsdAppendEntry(acl, fbsdTagGroupObj, groupAllow, fbsdEntryTypeAllow)
	fbsdAppendEntry(acl, fbsdTagEveryone, everyoneAllow, fbsdEntryTypeAllow)
}

// fbsdNFS4TrivialDraft builds the canonical-six NFSv4 ACL of one mode
// (subr_acl_nfs4.c acl_nfs4_sync_acl_from_mode_draft with an empty
// ACL: the six fixed entries are appended and the mode bits are
// distributed between the deny and allow members).
func fbsdNFS4TrivialDraft(acl *fbsdACL, mode uint32) {
	writeSet := fbsdPermWriteACL | fbsdPermWriteOwner | fbsdPermWriteAttrs | fbsdPermWriteNamed
	readSet := fbsdPermReadACL | fbsdPermReadAttrs | fbsdPermReadNamed | fbsdPermSync
	a1 := fbsdAppendEntry(acl, fbsdTagUserObj, 0, fbsdEntryTypeDeny)
	a2 := fbsdAppendEntry(acl, fbsdTagUserObj, writeSet, fbsdEntryTypeAllow)
	a3 := fbsdAppendEntry(acl, fbsdTagGroupObj, 0, fbsdEntryTypeDeny)
	a4 := fbsdAppendEntry(acl, fbsdTagGroupObj, 0, fbsdEntryTypeAllow)
	a5 := fbsdAppendEntry(acl, fbsdTagEveryone, writeSet, fbsdEntryTypeDeny)
	a6 := fbsdAppendEntry(acl, fbsdTagEveryone, readSet, fbsdEntryTypeAllow)

	if mode&0o400 != 0 {
		a2.Perm |= fbsdPermReadData
	} else {
		a1.Perm |= fbsdPermReadData
	}
	if mode&0o200 != 0 {
		a2.Perm |= fbsdPermWriteData | fbsdPermAppendData
	} else {
		a1.Perm |= fbsdPermWriteData | fbsdPermAppendData
	}
	if mode&0o100 != 0 {
		a2.Perm |= fbsdPermExecute
	} else {
		a1.Perm |= fbsdPermExecute
	}
	if mode&0o040 != 0 {
		a4.Perm |= fbsdPermReadData
	} else {
		a3.Perm |= fbsdPermReadData
	}
	if mode&0o020 != 0 {
		a4.Perm |= fbsdPermWriteData | fbsdPermAppendData
	} else {
		a3.Perm |= fbsdPermWriteData | fbsdPermAppendData
	}
	if mode&0o010 != 0 {
		a4.Perm |= fbsdPermExecute
	} else {
		a3.Perm |= fbsdPermExecute
	}
	if mode&0o004 != 0 {
		a6.Perm |= fbsdPermReadData
	} else {
		a5.Perm |= fbsdPermReadData
	}
	if mode&0o002 != 0 {
		a6.Perm |= fbsdPermWriteData | fbsdPermAppendData
	} else {
		a5.Perm |= fbsdPermWriteData | fbsdPermAppendData
	}
	if mode&0o001 != 0 {
		a6.Perm |= fbsdPermExecute
	} else {
		a5.Perm |= fbsdPermExecute
	}
}

// fbsdAppendEntry appends one entry to the ACL and returns it (libc
// _acl_append: the entry carries the undefined id and no flags).
func fbsdAppendEntry(acl *fbsdACL, tag uint32, perm uint32, entryType uint16) *fbsdACLEntry {
	entry := &acl.Entries[acl.Cnt]
	acl.Cnt++
	entry.Tag = tag
	entry.ID = fbsdUndefinedID
	entry.Perm = perm
	entry.EntryType = entryType
	entry.Flags = 0
	return entry
}

// fbsdPOSIXSort orders one POSIX.1e ACL into the canonical kernel
// order (libc _posix1e_acl_sort: tag ascending, then id ascending for
// the named user and group entries). The FreeBSD kernel rejects
// unsorted POSIX.1e submission, and the libc acl_strip_np output
// appends the recalculated mask after the base entries, so the set arm
// of the freebsd machine presorts exactly like libc acl_set_fd.
func fbsdPOSIXSort(acl *fbsdACL) {
	for i := uint32(1); i < acl.Cnt; i++ {
		entry := acl.Entries[i]
		j := i
		for j > 0 && fbsdPOSIXLess(entry, acl.Entries[j-1]) {
			acl.Entries[j] = acl.Entries[j-1]
			j--
		}
		acl.Entries[j] = entry
	}
}

// fbsdPOSIXLess orders two POSIX.1e entries by tag, then by id for the
// named user and group tags (libc _posix1e_acl_entry_compare).
func fbsdPOSIXLess(a, b fbsdACLEntry) bool {
	if a.Tag != b.Tag {
		return a.Tag < b.Tag
	}
	if a.Tag == fbsdTagUser || a.Tag == fbsdTagGroup {
		return a.ID < b.ID
	}
	return false
}
