//go:build linux && amd64

package worker

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"unsafe"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/random"
	"github.com/firehol/iprange/v4/go/internal/security"
)

// Control is the mapped worker coordination record (Rust
// worker/control.rs Control, fault subset). Only the parent that created
// the control owns its path; a worker opens by path without ownership.
type Control struct {
	mapping *mapping.Mapping
	file    *os.File
	path    string
	data    []byte // cached full-extent view (View(0, controlLen)); stable because the extent never resizes
}

// CreateParent creates the 1 MiB control file with a random private name,
// maps it read-write, and initializes the fault-subset header (Rust
// Control::create_parent). The created path is unlinked by RemovePath or
// Close.
func CreateParent() (*Control, error) {
	nonce, err := random.Nonzero128()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(os.TempDir(), ".iprange-v4-worker-"+hex.EncodeToString(nonce[:])+".ctl")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, &format.Error{Code: format.CodeIO, Detail: "worker control create: " + err.Error()}
	}
	// creator-only policy: mode exactly 0600, no inherited access ACL,
	// and the ownership commitment proof (Rust control.rs create_file +
	// security::secure_creator_only). A restrictive umask can never make
	// the control file unopenable by the worker.
	profile, err := security.Capture()
	if err != nil {
		return nil, workerSecurityFailure(err)
	}
	if err := security.SecureCreatorOnly(f, profile); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, workerSecurityFailure(err)
	}
	fail := func(cause error) (*Control, error) {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, cause
	}
	if err := f.Truncate(controlLen); err != nil {
		return fail(&format.Error{Code: format.CodeIO, Detail: "worker control truncate: " + err.Error()})
	}
	m, err := mapping.MapFile(f, controlLen, true)
	if err != nil {
		return fail(err)
	}
	data, err := m.View(0, controlLen)
	if err != nil {
		return fail(err)
	}
	c := &Control{mapping: m, file: f, path: path, data: data}
	clear(c.data)
	copy(c.data[offMagic:offMagic+8], controlMagic[:])
	format.PutU32(c.data[offProtocol:offProtocol+4], protocol)
	copy(c.data[offBuildID:offBuildID+buildLen], buildID)
	copy(c.data[offNonce:offNonce+nonceLen], nonce[:])
	format.PutU32(c.data[offParentPID:offParentPID+4], uint32(os.Getpid()))
	// The state store is the last write of the header, exactly like the
	// Rust create_parent (set_state with Release ordering), so a worker
	// that observes Request also observes the identity fields above.
	c.SetState(stateRequest)
	return c, nil
}

// workerSecurityFailure maps any creator-only policy failure to the
// worker's Conflict class exactly like Rust control.rs namespace_error
// (the detail is the exact Rust string; the cause is folded away).
func workerSecurityFailure(cause error) error {
	return &format.Error{Code: format.CodeConflict, Detail: "worker control access policy could not be established"}
}

// OpenWorker opens an existing control file by path and maps its exact
// 1 MiB extent read-write (Rust Control::open_worker).
func OpenWorker(path string) (*Control, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, &format.Error{Code: format.CodeIO, Detail: "worker control open: " + err.Error()}
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, &format.Error{Code: format.CodeIO, Detail: "worker control stat: " + err.Error()}
	}
	if st.Size() != controlLen {
		_ = f.Close()
		return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker control length is invalid"}
	}
	m, err := mapping.MapFile(f, controlLen, true)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	data, err := m.View(0, controlLen)
	if err != nil {
		_ = m.Close()
		_ = f.Close()
		return nil, err
	}
	return &Control{mapping: m, file: f, data: data}, nil
}

// RemovePath unlinks the control file when this Control created it (Rust
// Control::remove_path). The mapping stays valid after the unlink.
func (c *Control) RemovePath() error {
	if c.path == "" {
		return nil
	}
	path := c.path
	c.path = ""
	if err := os.Remove(path); err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "worker control unlink: " + err.Error()}
	}
	return nil
}

// Arm registers the mapped region the worker wants isolated and re-arms
// the probe (Rust Control::arm). generation must be nonzero, the base
// non-null, and the length nonzero.
func (c *Control) Arm(generation uint64, role MappingRole, base uintptr, length uint64) error {
	if generation == 0 || base == 0 || length == 0 {
		return &format.Error{Code: format.CodeInvalidArgument, Detail: "worker mapping probe is empty"}
	}
	// Rust's arm takes the MappingRole enum, so an invalid role is
	// unrepresentable there; reject it here instead of letting the probe
	// silently degrade to unowned chaining.
	if _, ok := roleFromWire(uint32(role)); !ok {
		return &format.Error{Code: format.CodeInvalidArgument, Detail: "worker mapping role is invalid"}
	}
	data := c.data
	// x86-64 TSO orders the plain field stores before the armed=1 store,
	// exactly like Rust's Release stores in arm.
	mapAtomicStore32(baseOf(data), offHandling, 0)
	format.PutU64(data[offGeneration:offGeneration+8], generation)
	format.PutU32(data[offRole:offRole+4], uint32(role))
	format.PutU64(data[offBase:offBase+8], uint64(base))
	format.PutU64(data[offLen:offLen+8], length)
	mapAtomicStore32(baseOf(data), offArmed, 1)
	return nil
}

// Disarm clears the armed probe (Rust Control::disarm).
func (c *Control) Disarm() {
	mapAtomicStore32(baseOf(c.data), offArmed, 0)
}

// FaultRecord returns the validated fault record after an owned fault
// (Rust Control::fault_record). Every cross-check of the Rust authority is
// mirrored: state Fault, sealed marker, handling claimed, matching
// generation and role, positive code, in-range relative offset, and a
// base+relative address that equals the faulting address.
func (c *Control) FaultRecord() (FaultRecord, error) {
	data := c.data
	base := baseOf(data)
	fail := func(detail string) (FaultRecord, error) {
		return FaultRecord{}, &format.Error{Code: format.CodeConflict, Detail: detail}
	}
	if mapAtomicLoad32(base, offState) != stateFault ||
		format.U32(data[offFaultMarker:offFaultMarker+4]) != faultMarker ||
		mapAtomicLoad32(base, offHandling) != 1 {
		return fail("worker fault record is incomplete")
	}
	generation := format.U64(data[offGeneration : offGeneration+8])
	faultGeneration := format.U64(data[offFaultGen : offFaultGen+8])
	role, roleOK := roleFromWire(format.U32(data[offRole : offRole+4]))
	faultRole, faultRoleOK := roleFromWire(format.U32(data[offFaultRole : offFaultRole+4]))
	mappingBase := format.U64(data[offBase : offBase+8])
	mappingLen := format.U64(data[offLen : offLen+8])
	code := int32(format.U32(data[offFaultCode : offFaultCode+4]))
	relative := format.U64(data[offFaultRelative : offFaultRelative+8])
	address := format.U64(data[offFaultAddress : offFaultAddress+8])
	if generation == 0 ||
		generation != faultGeneration ||
		!roleOK || !faultRoleOK || role != faultRole ||
		code <= 0 ||
		mappingLen == 0 || relative >= mappingLen ||
		mappingBase > ^uint64(0)-relative || // Rust base.checked_add(relative) rejects overflow
		mappingBase+relative != address {
		return fail("worker fault ownership is inconsistent")
	}
	return FaultRecord{Role: role, Relative: relative, MappingLen: mappingLen}, nil
}

// Close releases the control mapping, the descriptor, and (when this
// Control created the path) the control file (Rust Control::drop).
func (c *Control) Close() {
	c.data = nil
	if c.mapping != nil {
		_ = c.mapping.Close()
	}
	if c.file != nil {
		_ = c.file.Close()
	}
	if c.path != "" {
		_ = os.Remove(c.path)
		c.path = ""
	}
}

// baseOf returns the raw address of the mapped control bytes.
func baseOf(data []byte) uintptr {
	return uintptr(unsafe.Pointer(&data[0]))
}

// base returns the control mapping base as an address (posix.rs
// Control::base). The naked handler resolves fields relative to it.
func (c *Control) base() uintptr {
	return baseOf(c.data)
}

// altStack returns the alternate-signal-stack extent: the last 64 KiB of
// the control mapping (Rust Control::alt_stack). The kernel writes signal
// frames there; the bytes stay in the file-backed mapping.
func (c *Control) altStack() (uintptr, uintptr) {
	return baseOf(c.data) + uintptr(controlLen-altStackLen), uintptr(altStackLen)
}

// state reads the control state word.
// SetBuildID pins the worker build identity of this process before
// any control access (the runtime analog of Rust env! at build time:
// the cmd binary resolves IPRANGE_V4_BUILD_ID or the fixed default and
// calls this once at startup; VerifyRequest compares the control header
// against the pinned value). A value outside the fixed 64-byte width is
// refused with the verbatim Rust invalid-argument class.
func SetBuildID(value string) error {
	if len(value) != buildLen {
		return &format.Error{Code: format.CodeInvalidArgument, Detail: "worker build id must be 64 bytes"}
	}
	buildID = value
	return nil
}

// State returns the current session state word (Rust control.rs
// state(): the mapped state slot; the cmd binary reads it for the
// parent-liveness spins Rust runs inside the crate).
func (c *Control) State() uint32 { return c.state() }

func (c *Control) state() uint32 {
	return mapAtomicLoad32(baseOf(c.data), offState)
}
