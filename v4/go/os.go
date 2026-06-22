//go:build unix

package iprangedb

// Unix file layer for v4: an mmap reader and a pread/pwrite writer, with flock(2) (§11)
// and the §10 open hardening (O_NOFOLLOW / O_CLOEXEC, fstat the fd, SEEK_HOLE, re-fstat,
// last-byte probe).
//
// The shareable artifact is the v3 snapshot; this live store is a local file (NFS
// unsupported, §11). A corrupt / truncated / hostile file is rejected — never a SIGBUS,
// loop, or out-of-bounds read.

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// DefaultLockWait is the default bound on how long a writer waits for LOCK_EX before a
// typed timeout (§11); a deployment knob, not part of the format.
const DefaultLockWait = 30 * time.Second

func flockShared(fd int) error {
	if err := unix.Flock(fd, unix.LOCK_SH); err != nil {
		return errf("Io", "flock(LOCK_SH): "+err.Error())
	}
	return nil
}

func flockExclusive(fd int, wait time.Duration) error {
	// Bounded-wait LOCK_EX via LOCK_NB retry (a stalled-reader defense, §11).
	deadline := time.Now().Add(wait)
	for {
		err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if err != unix.EWOULDBLOCK {
			return errf("Io", "flock(LOCK_EX): "+err.Error())
		}
		if time.Now().After(deadline) {
			return errf("Io", "flock(LOCK_EX) timed out")
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// MmapReader is a read-only mmap of a v4 file, holding LOCK_SH for its lifetime (§11).
// Call Reader once and reuse the returned *Reader for many queries. Close releases the
// mapping and the lock.
//
// Lock contract (§11): the mmap'd bytes are only valid while mapped under the lock, so an
// MmapReader necessarily holds LOCK_SH across the caller's queries. This is the read
// session's locked window — follow the open -> read -> Close model and keep it
// short-lived; a writer's LOCK_EX is blocked while any reader holds LOCK_SH, so do not
// retain an idle MmapReader.
type MmapReader struct {
	file *os.File // keeps the fd (and the shared lock) alive
	data []byte   // the mmap'd bytes
}

// OpenMmap opens and maps path read-only with the §10 hardening + LOCK_SH. It errors
// (never SIGBUS/loops) on a symlink final component, sparse hole, truncation, TOCTOU
// replacement, or a filesystem without hole detection.
func OpenMmap(path string) (*MmapReader, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errf("Io", "open: "+err.Error())
	}
	ok := false
	defer func() {
		if !ok {
			_ = file.Close()
		}
	}()
	fd := int(file.Fd())
	if err := flockShared(fd); err != nil {
		return nil, err
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return nil, errf("Io", "fstat: "+err.Error())
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, errStructural("not a regular file")
	}
	length := st.Size
	if length < int64(2*pageSize) {
		return nil, errFileTooShort(uint64(2*pageSize), uint64(length))
	}
	// SEEK_HOLE: a hole inside the mapped range would SIGBUS; refuse it (§10).
	hole, err := unix.Seek(fd, 0, unix.SEEK_HOLE)
	if err != nil {
		return nil, errStructural("hole detection unavailable — read into a buffer and use Open")
	}
	if hole != length {
		return nil, errStructural("sparse file (hole) — refusing to mmap")
	}
	data, err := unix.Mmap(fd, 0, int(length), unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		return nil, errf("Io", "mmap: "+err.Error())
	}
	defer func() {
		if !ok {
			_ = unix.Munmap(data)
		}
	}()
	// Re-fstat after mmap and reject on size/inode/device change (TOCTOU, §10).
	var st2 unix.Stat_t
	if err := unix.Fstat(fd, &st2); err != nil {
		return nil, errf("Io", "re-fstat: "+err.Error())
	}
	if st2.Size != length || st2.Ino != st.Ino || st2.Dev != st.Dev {
		return nil, errStructural("file changed during mmap (TOCTOU)")
	}
	// Probe the last referenced byte: pread past EOF returns 0, not SIGBUS (§10).
	var probe [1]byte
	if n, perr := file.ReadAt(probe[:], length-1); perr != nil || n != 1 {
		return nil, errStructural("file truncated after fstat (probe failed)")
	}
	ok = true
	return &MmapReader{file: file, data: data}, nil
}

// Reader returns a validated reader over the mapped bytes (§9 full validation). Call once
// and reuse the returned reader.
func (m *MmapReader) Reader() (*Reader, error) { return Open(m.data) }

// Bytes returns the mapped bytes (validate via Reader before trusting them).
func (m *MmapReader) Bytes() []byte { return m.data }

// Close unmaps the file and releases the shared lock.
func (m *MmapReader) Close() error {
	err := unix.Munmap(m.data)
	if cerr := m.file.Close(); err == nil {
		err = cerr
	}
	return err
}

// FileWriter is a read/write handle holding LOCK_EX (§11): mutate via Set/Delete, then
// Commit (the two-fsync double-meta protocol, §6.3). It loads the file image into memory
// (fine for files that fit in RAM). It is generic over the key width; use
// CreateFileWriterV4/V6 or OpenFileWriterV4/V6.
type FileWriter[K ipKey[K]] struct {
	file *os.File
	w    *Writer[K]
}

// CreateFileWriterV4 creates a new IPv4 file (see createFileWriter).
func CreateFileWriterV4(path string, scopeWidth uint8, createdUnixtime uint64, wait time.Duration) (*FileWriter[Ipv4Key], error) {
	return createFileWriter[Ipv4Key](path, scopeWidth, createdUnixtime, wait)
}

// CreateFileWriterV6 creates a new IPv6 file (see createFileWriter).
func CreateFileWriterV6(path string, scopeWidth uint8, createdUnixtime uint64, wait time.Duration) (*FileWriter[Ipv6Key], error) {
	return createFileWriter[Ipv6Key](path, scopeWidth, createdUnixtime, wait)
}

// createFileWriter creates a new file (must not exist — O_EXCL) and writes the initial
// empty DB durably. Holds LOCK_EX.
func createFileWriter[K ipKey[K]](path string, scopeWidth uint8, createdUnixtime uint64, wait time.Duration) (*FileWriter[K], error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, errf("Io", "create: "+err.Error())
	}
	ok := false
	defer func() {
		if !ok {
			_ = file.Close()
		}
	}()
	if err := flockExclusive(int(file.Fd()), wait); err != nil {
		return nil, err
	}
	w := createWriter[K](scopeWidth, createdUnixtime)
	img := w.Image()
	if err := file.Truncate(int64(len(img))); err != nil {
		return nil, errf("Io", "truncate: "+err.Error())
	}
	if _, err := file.WriteAt(img, 0); err != nil {
		return nil, errf("Io", "write: "+err.Error())
	}
	if err := file.Sync(); err != nil {
		return nil, errf("Io", "sync: "+err.Error())
	}
	ok = true
	return &FileWriter[K]{file: file, w: w}, nil
}

// OpenFileWriterV4 opens an existing IPv4 file for mutation (see openFileWriter).
func OpenFileWriterV4(path string, wait time.Duration) (*FileWriter[Ipv4Key], error) {
	return openFileWriter[Ipv4Key](path, wait)
}

// OpenFileWriterV6 opens an existing IPv6 file for mutation (see openFileWriter).
func OpenFileWriterV6(path string, wait time.Duration) (*FileWriter[Ipv6Key], error) {
	return openFileWriter[Ipv6Key](path, wait)
}

// openFileWriter opens an existing file for mutation: LOCK_EX, read the image, validate +
// derive the free set (§6.2 / §7). It rejects a non-regular file.
func openFileWriter[K ipKey[K]](path string, wait time.Duration) (*FileWriter[K], error) {
	file, err := os.OpenFile(path, os.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errf("Io", "open: "+err.Error())
	}
	ok := false
	defer func() {
		if !ok {
			_ = file.Close()
		}
	}()
	if err := flockExclusive(int(file.Fd()), wait); err != nil {
		return nil, err
	}
	var st unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &st); err != nil {
		return nil, errf("Io", "fstat: "+err.Error())
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, errStructural("not a regular file")
	}
	buf := make([]byte, st.Size)
	if _, err := file.ReadAt(buf, 0); err != nil {
		return nil, errf("Io", "read: "+err.Error())
	}
	w, err := openImage[K](buf)
	if err != nil {
		return nil, err
	}
	ok = true
	return &FileWriter[K]{file: file, w: w}, nil
}

// Set applies set([from,to]) = scope (§8). Not durable until Commit.
func (fw *FileWriter[K]) Set(from, to K, scope []byte) error { return fw.w.Set(from, to, scope) }

// Delete applies delete([from,to]) (§8). Not durable until Commit.
func (fw *FileWriter[K]) Delete(from, to K) error { return fw.w.Delete(from, to) }

// RecordCount returns the records in the (pending) tree.
func (fw *FileWriter[K]) RecordCount() uint64 { return fw.w.RecordCount() }

// Commit commits durably (§6.3): pwrite the new data pages, fsync (Barrier 1), pwrite the
// new meta, fsync (Barrier 2). On any error the txn is abandoned with no acknowledged
// commit; recovery is automatic on the next open.
func (fw *FileWriter[K]) Commit(updatedUnixtime uint64) error {
	dirty := fw.w.takeDirty()
	// Grow / reclaim trailing to match the in-memory image length.
	if err := fw.file.Truncate(int64(len(fw.w.Image()))); err != nil {
		return errf("Io", "truncate: "+err.Error())
	}
	img := fw.w.Image()
	for _, p := range dirty {
		off := int(p) * pageSize
		if _, err := fw.file.WriteAt(img[off:off+pageSize], int64(off)); err != nil {
			return errf("Io", "pwrite data: "+err.Error())
		}
	}
	if err := fw.file.Sync(); err != nil { // Barrier 1: data durable before the meta references it
		return errf("Io", "fsync barrier 1: "+err.Error())
	}
	inactive, err := fw.w.commitMeta(updatedUnixtime)
	if err != nil {
		return err
	}
	img = fw.w.Image()
	off := int(inactive) * pageSize
	if _, err := fw.file.WriteAt(img[off:off+pageSize], int64(off)); err != nil {
		return errf("Io", "pwrite meta: "+err.Error())
	}
	if err := fw.file.Sync(); err != nil { // Barrier 2: the commit point
		return errf("Io", "fsync barrier 2: "+err.Error())
	}
	return nil
}

// Close releases the exclusive lock (uncommitted mutations are discarded).
func (fw *FileWriter[K]) Close() error { return fw.file.Close() }
