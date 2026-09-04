"""Craft one format-valid Windows GC envelope pair for qualification.

The committed v4 format defines the 8,192-byte housekeeping envelope
(binary-format-v4.md section 14.4.1; Go ``v4/go/internal/live/
gc_codec.go``, Rust ``publication/gc_codec.rs``): two independently
selectable 4,096-byte blocks, each a 512-byte sequence/CRC-sealed
header followed by the retired artifact's source basename in the
declared encoding.  This module builds one envelope whose block
layout, name commitments, and creator-only security commitment are
byte-exact with the product codecs, and creates the paired inert
payload file with the same protected creator-only DACL the product
installs (``security_windows.go buildDescriptor``), so the product's
own ``maintenance.list`` accepts the pair with no cleanup problem and
``maintenance.remove`` can resolve it to durable absence.

The format and the ownership-security contract are public product
contracts; the module contains no test-only product hook.  All
Windows API access goes through ctypes so the script runs under the
mingw64 Python of the authorized Windows validation host.
"""

import ctypes
import ctypes.wintypes as wintypes
import hashlib
import os
import struct

# ---------------------------------------------------------------------------
# GC envelope codec constants (binary-format-v4.md 14.4.1).
# ---------------------------------------------------------------------------

GC_ENVELOPE_SIZE = 8192
GC_PAGE = 4096
GC_RECORD_SIZE = 512
GC_VERSION = 1
GC_MAGIC = b"IPR4GCA1"

# Offsets inside one 512-byte header block.
GC_KIND_OFF = 12
GC_ENCODING_OFF = 14
GC_ATTEMPT_OFF = 16
GC_ORDINAL_OFF = 32
GC_DIR_KIND_OFF = 36
GC_ART_KIND_OFF = 38
GC_DIR_IDENTITY = 40
GC_SRC_COMMIT_OFF = 72
GC_INERT_COMMIT = 104
GC_PAYLOAD_PRES = 136
GC_ART_IDENTITY = 144
GC_PAYLOAD_RESERVED = (176, 288)
GC_SEC_KIND_OFF = 288
GC_ROLE_OFF = 290
GC_SEC_RESERVED = (292, 296)
GC_SEC_COMMIT_OFF = 296
GC_SOURCE_LEN_OFF = 328
GC_TAIL_RESERVED = (332, 496)
GC_SEQUENCE_OFF = 496
GC_PRECRC_RESERVED = (504, 508)
GC_CRC_OFF = 508
GC_SOURCE_OFF = 512

# Envelope/inert name patterns (gc_name.go).
GC_ENVELOPE_PREFIX = ".iprange-gcauth-"
GC_INERT_PREFIX = ".iprange-gc-"
GC_SUFFIX = ".tmp"

# Identity kinds and values (identity_local_windows.go).
IDENTITY_KIND = 2           # Windows volume-serial + file-reference
CREATION_SECURITY_KIND = 2  # Windows creator-only
ARTIFACT_PRIVATE_OUTPUT = 1
DIRECTORY_ROLE_DESTINATION = 1
BASENAME_ENCODING_UTF16LE = 2

# Creator-only security constants (security_windows.go).
FILE_ALL_ACCESS = 0x001F01FF
SE_DACL_PROTECTED = 0x1000
COMMITMENT_DOMAIN = b"IPR4PSEC"

# Header block CRC-32C (Castagnoli) table.
_CRC32C_TABLE = []
for _i in range(256):
    _c = _i
    for _ in range(8):
        _c = (0x82F63B78 ^ (_c >> 1)) if (_c & 1) else (_c >> 1)
    _CRC32C_TABLE.append(_c & 0xFFFFFFFF)


def crc32c(data):
    crc = 0xFFFFFFFF
    for byte in data:
        crc = (crc >> 8) ^ _CRC32C_TABLE[(crc ^ byte) & 0xFF]
    return crc ^ 0xFFFFFFFF


def ascii_utf16le(name):
    return b"".join(bytes([ch, 0]) for ch in name.encode("ascii"))


def gc_name(prefix, attempt, ordinal):
    """One fixed-width housekeeping name (gc_name.go gcName)."""

    return f"{prefix}{attempt.hex()}-{ordinal:08x}{GC_SUFFIX}"


def _name_commitment(domain, encoding, name_utf16le):
    digest = hashlib.sha256()
    digest.update(domain.encode("ascii"))
    digest.update(struct.pack("<H", encoding))
    digest.update(struct.pack("<I", len(name_utf16le)))
    digest.update(name_utf16le)
    return digest.digest()


def _pack_block(attempt, ordinal, source_ascii, directory_identity,
                artifact_identity, creator_commit):
    """One sequence-1 header block (gc_codec gcEncode)."""

    source_utf16 = ascii_utf16le(source_ascii)
    inert_ascii = gc_name(GC_INERT_PREFIX, attempt, ordinal)
    inert_utf16 = ascii_utf16le(inert_ascii)
    block = bytearray(GC_PAGE)
    block[0:8] = GC_MAGIC
    struct.pack_into("<H", block, 8, GC_RECORD_SIZE)
    struct.pack_into("<H", block, 10, GC_VERSION)
    struct.pack_into("<H", block, GC_KIND_OFF, ARTIFACT_PRIVATE_OUTPUT)
    struct.pack_into("<H", block, GC_ENCODING_OFF, BASENAME_ENCODING_UTF16LE)
    block[GC_ATTEMPT_OFF:GC_ATTEMPT_OFF + 16] = attempt
    struct.pack_into("<I", block, GC_ORDINAL_OFF, ordinal)
    struct.pack_into("<H", block, GC_DIR_KIND_OFF, IDENTITY_KIND)
    struct.pack_into("<H", block, GC_ART_KIND_OFF, IDENTITY_KIND)
    block[GC_DIR_IDENTITY:GC_DIR_IDENTITY + 32] = directory_identity
    block[GC_SRC_COMMIT_OFF:GC_SRC_COMMIT_OFF + 32] = _name_commitment(
        "IPR4GCAUTH", BASENAME_ENCODING_UTF16LE, source_utf16)
    block[GC_INERT_COMMIT:GC_INERT_COMMIT + 32] = _name_commitment(
        "IPR4GCNAME", BASENAME_ENCODING_UTF16LE, inert_utf16)
    struct.pack_into("<H", block, GC_PAYLOAD_PRES, 0)  # payload absent
    block[GC_ART_IDENTITY:GC_ART_IDENTITY + 32] = artifact_identity
    struct.pack_into("<H", block, GC_SEC_KIND_OFF, CREATION_SECURITY_KIND)
    struct.pack_into("<H", block, GC_ROLE_OFF, DIRECTORY_ROLE_DESTINATION)
    block[GC_SEC_COMMIT_OFF:GC_SEC_COMMIT_OFF + 32] = creator_commit
    struct.pack_into("<I", block, GC_SOURCE_LEN_OFF, len(source_utf16))
    struct.pack_into("<Q", block, GC_SEQUENCE_OFF, 1)
    block[GC_SOURCE_OFF:GC_SOURCE_OFF + len(source_utf16)] = source_utf16
    # The seal covers the whole 4,096-byte block with its own checksum
    # field zeroed (gc_codec.go gcChecksumValid / Rust checksum_valid);
    # covering only the 512-byte header produces a block the products
    # reject as not selectable as soon as the source name is non-empty.
    checksum = crc32c(bytes(block[:GC_CRC_OFF]) + b"\x00" * 4
                      + bytes(block[GC_CRC_OFF + 4:]))
    struct.pack_into("<I", block, GC_CRC_OFF, checksum)
    return bytes(block)


def envelope_bytes(attempt, ordinal, source_ascii, directory_identity,
                   artifact_identity, creator_commit):
    """The full 8,192-byte envelope: two identical sequence-1 blocks."""

    block = _pack_block(attempt, ordinal, source_ascii, directory_identity,
                        artifact_identity, creator_commit)
    return block + block


def creator_commitment(sid_bytes):
    """The creator-only commitment of one owner SID
    (security_windows.go commitment)."""

    digest = hashlib.sha256()
    digest.update(COMMITMENT_DOMAIN)
    digest.update(struct.pack("<I", len(sid_bytes)))
    digest.update(sid_bytes)
    digest.update(struct.pack("<I", FILE_ALL_ACCESS))
    digest.update(struct.pack("<H", SE_DACL_PROTECTED))
    return digest.digest()


# ---------------------------------------------------------------------------
# Windows identity/security helpers (ctypes; authorized Windows host).
# ---------------------------------------------------------------------------

kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
advapi32 = ctypes.WinDLL("advapi32", use_last_error=True)

FILE_SHARE_READ = 0x1
FILE_SHARE_WRITE = 0x2
FILE_SHARE_DELETE = 0x4
OPEN_EXISTING = 3
FILE_FLAG_BACKUP_SEMANTICS = 0x02000000
FILE_READ_ATTRIBUTES = 0x80
GENERIC_READ = 0x80000000
GENERIC_WRITE = 0x40000000
CREATE_NEW = 1
FILE_ATTRIBUTE_NORMAL = 0x80
INVALID_HANDLE_VALUE = ctypes.c_void_p(-1).value

TOKEN_QUERY = 0x0008
TokenUser = 1
OWNER_SECURITY_INFORMATION = 0x1
DACL_SECURITY_INFORMATION = 0x4
SE_FILE_OBJECT = 0
PROTECTED_DACL_SECURITY_INFORMATION = DACL_SECURITY_INFORMATION
NO_INHERITANCE = 0x0
SET_ACCESS = 0x00000001
TRUSTEE_IS_SID = 3
TRUSTEE_IS_USER = 1
ACCESS_ALLOWED_ACE_TYPE = 0x00
ACL_REVISION = 2

ERROR_INSUFFICIENT_BUFFER = 122


class SECURITY_ATTRIBUTES(ctypes.Structure):
    _fields_ = [("nLength", wintypes.DWORD),
                ("lpSecurityDescriptor", ctypes.c_void_p),
                ("bInheritHandle", wintypes.BOOL)]


class FILETIME(ctypes.Structure):
    _fields_ = [("dwLowDateTime", wintypes.DWORD),
                ("dwHighDateTime", wintypes.DWORD)]


class BY_HANDLE_FILE_INFORMATION(ctypes.Structure):
    _fields_ = [("dwFileAttributes", wintypes.DWORD),
                ("ftCreationTime", FILETIME),
                ("ftLastAccessTime", FILETIME),
                ("ftLastWriteTime", FILETIME),
                ("dwVolumeSerialNumber", wintypes.DWORD),
                ("nFileSizeHigh", wintypes.DWORD),
                ("nFileSizeLow", wintypes.DWORD),
                ("nNumberOfLinks", wintypes.DWORD),
                ("nFileIndexHigh", wintypes.DWORD),
                ("nFileIndexLow", wintypes.DWORD)]


class FILE_ID_INFO(ctypes.Structure):
    _fields_ = [("VolumeSerialNumber", ctypes.c_uint64),
                ("FileId", ctypes.c_ubyte * 16)]


class SID_IDENTIFIER_AUTHORITY(ctypes.Structure):
    _fields_ = [("Value", ctypes.c_ubyte * 6)]


class SID(ctypes.Structure):
    # SID_MAX_SUB_AUTHORITIES is 15; the declared capacity is read-only
    # projection, so a generous bound keeps sid_bytes valid for every
    # user SID shape without allocating a SID here.
    _fields_ = [("Revision", ctypes.c_ubyte),
                ("SubAuthorityCount", ctypes.c_ubyte),
                ("IdentifierAuthority", SID_IDENTIFIER_AUTHORITY),
                ("SubAuthority", wintypes.DWORD * 16)]


class TOKEN_USER(ctypes.Structure):
    _fields_ = [("User", ctypes.c_void_p)]  # PSID


def _close(handle):
    kernel32.CloseHandle(ctypes.c_void_p(handle))


def _open_handle(path, access, creation, attributes, directory=False):
    kernel32.CreateFileW.restype = ctypes.c_void_p
    kernel32.CreateFileW.argtypes = [
        wintypes.LPCWSTR, wintypes.DWORD, wintypes.DWORD,
        ctypes.POINTER(SECURITY_ATTRIBUTES), wintypes.DWORD,
        wintypes.DWORD, wintypes.HANDLE]
    flags = FILE_ATTRIBUTE_NORMAL
    if directory:
        flags |= FILE_FLAG_BACKUP_SEMANTICS
    return kernel32.CreateFileW(
        path, access,
        FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE,
        attributes, creation, flags, None)


def effective_user_sid():
    """The effective token's user SID bytes (GetTokenInformation)."""

    process = kernel32.GetCurrentProcess()
    token = wintypes.HANDLE()
    if not advapi32.OpenProcessToken(
            ctypes.c_void_p(process), TOKEN_QUERY,
            ctypes.byref(token)):
        raise OSError("OpenProcessToken failed")
    try:
        length = wintypes.DWORD(0)
        advapi32.GetTokenInformation(token, TokenUser, None, 0,
                                     ctypes.byref(length))
        buf = ctypes.create_string_buffer(length.value)
        if not advapi32.GetTokenInformation(
                token, TokenUser, buf, length,
                ctypes.byref(length)):
            raise OSError("GetTokenInformation failed")
        sid_ptr = ctypes.cast(buf, ctypes.POINTER(ctypes.c_void_p))
        sid_address = ctypes.cast(sid_ptr, ctypes.POINTER(
            ctypes.c_void_p)).contents.value
        sid = ctypes.cast(sid_address, ctypes.POINTER(SID)).contents
        return sid_bytes(sid)
    finally:
        _close(token.value)


def sid_bytes(sid):
    """Binary SID projection (advapi32 copySid semantics)."""

    authority = bytes(sid.IdentifierAuthority.Value)
    subs = [sid.SubAuthority[i] for i in range(sid.SubAuthorityCount)]
    header = struct.pack("<BB", sid.Revision, sid.SubAuthorityCount)
    return (header + authority +
            b"".join(struct.pack("<I", s) for s in subs))


def file_identity(path):
    """(device, inode) of one path (volume serial + low file reference).

    Mirrors identity_helpers_windows.go fileIdentity: the device is
    the 64-bit volume serial (Windows 10 1607+), the inode is the low
    half of the FILE_ID_INFO identifier.  The kind-2 identity encoding
    is volume u64le at bytes 0..8 and file reference u64le at 8..16.
    """

    handle = _open_handle(path, FILE_READ_ATTRIBUTES, OPEN_EXISTING, None,
                          directory=os.path.isdir(path))
    if handle in (None, INVALID_HANDLE_VALUE):
        raise OSError("CreateFile for identity failed")
    try:
        info = FILE_ID_INFO()
        size = wintypes.DWORD(ctypes.sizeof(info))
        if not kernel32.GetFileInformationByHandleEx(
                ctypes.c_void_p(handle), 0x12,  # FileIdInfo
                ctypes.byref(info), size):
            raise OSError("GetFileInformationByHandleEx failed")
        file_id = bytes(info.FileId)
        low = struct.unpack("<Q", file_id[:8])[0]
        return info.VolumeSerialNumber, low
    finally:
        _close(handle)


def creator_only_commitment_of(path):
    """The creator-only commitment for files this module creates.

    Every file this module creates is built with the protected
    owner-only descriptor of the effective user token
    (``create_protected_file``, the same shape the product installs in
    ``security_windows.go buildDescriptor``); the product proves the
    live descriptor again when it lists or removes the pair
    (``CreatorOnlyCommitment``, which must equal the committed value).
    The commitment is therefore derived from the effective token user
    SID -- reading the owner back through GetSecurityInfo is not used
    because the MSYS2 mingw64 Python's ctypes cannot call that Win32
    entry point (it returns ERROR_INVALID_PARAMETER even for a plain
    file that the Go toolchain reads fine on the same host; the token
    projection is the source both the module and the product share).
    The ``path`` argument is accepted for caller clarity and is
    validated to exist before the commitment is returned.
    """

    if not os.path.isfile(path):
        raise OSError(f"protected file does not exist: {path}")
    return creator_commitment(effective_user_sid())


def create_protected_file(path):
    """Create one file with the product's creator-only descriptor.

    Mirrors security_windows.go buildDescriptor: one SET_ACCESS
    FILE_ALL_ACCESS NO_INHERITANCE ACE for the effective owner SID,
    an initialized security descriptor with that owner and DACL, and
    SE_DACL_PROTECTED.  Files created this way pass the product's
    CreatorOnlyCommitment check.
    """

    sid = effective_user_sid()
    sd = ctypes.create_string_buffer(ctypes.sizeof(
        ctypes.c_void_p) * 8)
    ptr = ctypes.cast(sd, ctypes.c_void_p)
    security_descriptor = ctypes.create_string_buffer(0x28)
    if not advapi32.InitializeSecurityDescriptor(
            ctypes.cast(security_descriptor, ctypes.c_void_p),
            wintypes.DWORD(1)):
        raise OSError("InitializeSecurityDescriptor failed")

    sid_buf = ctypes.create_string_buffer(sid)
    sid_ptr = ctypes.cast(sid_buf, ctypes.c_void_p)

    class TRUSTEE(ctypes.Structure):
        # accctrl.h TRUSTEE_W layout: operation, multiple-trustee
        # pointer, form, type, then the name/SID union.  The field
        # order is load-bearing: swapping the first two members makes
        # the descriptor builder see TrusteeForm == TRUSTEE_IS_NAME
        # and fail with ERROR_INVALID_PARAMETER.
        _fields_ = [("MultipleTrusteeOperation", wintypes.DWORD),
                    ("pMultipleTrustee", ctypes.c_void_p),
                    ("TrusteeForm", wintypes.DWORD),
                    ("TrusteeType", wintypes.DWORD),
                    ("ptstrName", ctypes.c_void_p)]

    class EXPLICIT_ACCESS(ctypes.Structure):
        _fields_ = [("grfAccessPermissions", wintypes.DWORD),
                    ("grfAccessMode", wintypes.DWORD),
                    ("grfInheritance", wintypes.DWORD),
                    ("Trustee", TRUSTEE)]

    trustee = TRUSTEE()
    trustee.TrusteeForm = TRUSTEE_IS_SID
    trustee.TrusteeType = TRUSTEE_IS_USER
    trustee.ptstrName = sid_ptr
    explicit = EXPLICIT_ACCESS()
    explicit.grfAccessPermissions = FILE_ALL_ACCESS
    explicit.grfAccessMode = SET_ACCESS
    explicit.grfInheritance = NO_INHERITANCE
    explicit.Trustee = trustee

    acl = ctypes.c_void_p()
    if advapi32.SetEntriesInAclW(
            1, ctypes.byref(explicit), None, ctypes.byref(acl)) != 0:
        raise OSError("SetEntriesInAclW failed")

    # Owner + DACL + protected control on the descriptor.
    owner_result = advapi32.SetSecurityDescriptorOwner(
        ctypes.cast(security_descriptor, ctypes.c_void_p), sid_ptr, 0)
    dacl_result = advapi32.SetSecurityDescriptorDacl(
        ctypes.cast(security_descriptor, ctypes.c_void_p), 1, acl, 0)
    control_result = advapi32.SetSecurityDescriptorControl(
        ctypes.cast(security_descriptor, ctypes.c_void_p),
        SE_DACL_PROTECTED, SE_DACL_PROTECTED)
    if not (owner_result and dacl_result and control_result):
        raise OSError("security descriptor build failed")

    attributes = SECURITY_ATTRIBUTES()
    attributes.nLength = ctypes.sizeof(SECURITY_ATTRIBUTES)
    attributes.lpSecurityDescriptor = ctypes.cast(
        security_descriptor, ctypes.c_void_p)
    attributes.bInheritHandle = 0

    handle = _open_handle(path, GENERIC_READ | GENERIC_WRITE, CREATE_NEW,
                          ctypes.byref(attributes))
    if handle in (None, INVALID_HANDLE_VALUE):
        raise OSError("CreateFile with creator-only descriptor failed")
    _close(handle)
