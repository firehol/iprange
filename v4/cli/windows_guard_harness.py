#!/usr/bin/env python3
"""Native same-source-guard session qualification for Windows spellings.

The same-source guard must refuse every Windows spelling of the
absent reader sidecar of a source database at the normal JSON-RPC
product interface, while still allowing distinct destinations:

- drive-relative        "C:db.iprange.readers"   (per-drive cwd)
- drive-relative upper  "C:DB.IPRANGE.READERS"
- absolute upper        "C:\\review\\DB.IPRANGE.READERS"
- rooted-without-volume "\\review\\db.iprange.readers"
- NTFS sigma family     "A\\u03c31.iprange.readers" (NTFS upcase-name
  equivalence of U+03A3/U+03C3, wave 19 round 19.14)
- extended-length       "\\\\?\\C:\\review\\db.iprange.readers"
  (wave 19 round 19.14)
- NT object-manager     "\\\\??\\C:\\review\\db.iprange.readers"
  (the "\\\\?\\" verbatim sibling, wave 19 round 19.15)
- forward-slash verbatim "\\\\?\\C:/review/db.iprange.readers"
  (same guarded identity; the kernel rejects the slash form for I/O,
  both engines still refuse it canonically, wave 19 round 19.15)
- namespace head x trailing-dot/space leaf (wave-19.18 parity P1-2 /
  P2-3): every namespace-head spelling of the sidecar whose final
  component carries a trailing dot or space (verbatim/NT UNC,
  GLOBALROOT verbatim/dos/object-manager, volume GUID, canonical and
  lowercase heads) denotes the same absent sidecar, because Win32
  strips the decoration at create/open.  The 65-row reviewer probe
  found this combined class divergent (12 rows Go allowed, 8 Rust
  allowed) and no committed test constructed it.

The distinct-destination controls publish real metadata bytes and the
harness validates the exact success response schema plus the delivered
file digest (wave 19 round 19.14 astra P2): a bare ``result:{}`` or an
omitted/empty output file can never pass.  The drive-root control
uses the SAME basename as the absent sidecar ("\\db.iprange.readers"
while the source sits in the per-drive working directory): it names
the distinct file at the drive root and must be published.

On Windows every sidecar spelling above denotes the protected sidecar
pathname; on POSIX (the negative control) all of them denote distinct
paths and must be allowed, proving the guard does not over-refuse.

The harness drives ``iprange.v1.database.metadata.get`` with a file
delivery over one persistent JSON-RPC service per product, against a
real v4 database copied into the working directory, and after the
refusal cases proves: the source bytes are unchanged, the sidecar is
still absent, and a follow-up metadata request still succeeds
(reopening) with fresh output matching the claimed digest.
Per-binary evidence records auditable identities: the binary
absolute path, its SHA-256, and one ``system.describe`` call whose
``implementation`` member must claim the expected language.

Report schema: ``iprange-cli-windows-guard-report-v1``.

``--verify-report PATH`` re-grades a committed report of either platform
without re-running the battery: the shared committed-report identity and
privacy rules, the schema, the build-ledger digests, every guard spelling the
battery produces, the refusal facts behind every recorded ``ok``, and the
source-integrity invariants.  ``--self-test`` executes its own accepted
baseline plus nine refused mutations of it, so the verifier is gated too.
"""

import argparse
import hashlib
import json
import os
import shutil
import sys

sys.dont_write_bytecode = True
_HERE = os.path.dirname(os.path.abspath(__file__))  # the v4/cli harness directory
if _HERE not in sys.path:
    sys.path.insert(0, _HERE)

import command_sanitize  # noqa: E402  (side-effect free)
from command_sanitize import (  # noqa: E402  (side-effect free)
    COMMITTED_REPORT_WRITERS,
    audit_report_writers,
    committed_report_problems,
    owned_temp_dir,
    personal_path_in_report,
    report_provenance,
    require_paths_outside_profile,
    run_shared_self_test,
    write_committed_report,
)
from crash_harness import HarnessJsonRpcService  # noqa: E402
# The report-verification machinery is shared, not copied.  The sha256sum
# ledger parser and the staging-path folding rule are owned by the Windows
# housekeeping harness; importing them here means one forged Windows report
# cannot pass one verifier and fail the other because the two disagree about
# what a ledger line is.  The import is one-directional (housekeeping never
# imports this module), and importing it costs 0.04 s on either host.
from windows_housekeeping_harness import _read_sha256_ledger, _tail  # noqa: E402
from schema.results import validate_result  # noqa: E402

REPORT_SCHEMA = "iprange-cli-windows-guard-report-v1"
RPC_READ_DEADLINE_SECONDS = 300.0
RPC_WRITE_DEADLINE_SECONDS = 120.0

ACCEPTED_MESSAGE = "destination must differ from the source database"
IS_WINDOWS = os.name == "nt"
METHOD = "iprange.v1.database.metadata.get"


def native_join(*parts):
    """os.path.join normalized to the native Windows separator.

    Some Windows interpreters (MSYS2 mingw64 CPython 3.14.x) patch
    ntpath so join/normpath emit "/"; every extended-length case
    destination needs backslashes throughout, and plain-path rows
    should stay canonical for evidence comparability.  No-op on
    interpreters with stock ntpath and on POSIX (wave-19.18 security
    verdict F1 harness reproducibility).
    """
    joined = os.path.join(*parts)
    if IS_WINDOWS:
        joined = joined.replace("/", "\\")
    return joined
OUTPUT_FACTS = ("bytes", "path", "rows", "sha256")

# The guard fixture carries the direct-v4 metadata record written by
# the fixture tool; every allowed delivery must publish exactly these
# bytes, independent of the product's own claims (astra turn-5 P2).
FIXTURE_METADATA = b'{"fixture":"direct-v4"}'


def file_evidence(path):
    with open(path, "rb") as fh:
        digest = hashlib.sha256(fh.read()).hexdigest()
    stat = os.stat(path)
    return {
        "path": os.path.abspath(path),
        "sha256": digest,
        "size": stat.st_size,
        "mtime": stat.st_mtime,
    }


def describe_implementation(service, expected):
    response = service.call("describe", "iprange.v1.system.describe", {})
    if "error" in response:
        raise AssertionError(
            "system.describe failed: " + json.dumps(response["error"])[:300]
        )
    implementation = (
        response.get("result", {}).get("implementation", "")
    )
    if implementation != expected:
        raise AssertionError(
            "system.describe implementation = %r, want %r"
            % (implementation, expected)
        )
    return implementation


def metadata_get_frame(source, destination):
    return {
        "source": {"path": source, "mode": "immutable"},
        "delivery": {
            "mode": "file",
            "path": destination,
            "publication_policy": "replace_existing",
            "max_output_bytes": "1048576",
            "max_open_files": 2,
        },
    }


def validate_success_response(response, method, destination,
                              expected_bytes=FIXTURE_METADATA):
    """Strict success-facts validation for one metadata.get file
    delivery (wave 19 round 19.14 astra P2, hardened astra turn-5).
    A successful response must carry the exact result schema, must
    report present=true (the guard fixture carries metadata), and the
    delivered file must equal the fixture's known metadata bytes; a
    bare ``result:{}``, a ``present:false`` answer, a schema
    violation, or a delivered file that merely matches the product's
    own claims is a harness failure, never a PASS.

    Returns (ok, reason).  Error responses are rejected here; refusal
    cases are validated separately with the exact canonical shape.
    """
    if "error" in response:
        return False, "unexpected error response"
    result = response.get("result")
    if not isinstance(result, dict):
        return False, "result is not an object"
    try:
        validate_result(method, result)
    except Exception as exc:  # schema.engine.ValidationError
        return False, "result schema violation: %s" % exc
    if result.get("present") is not True:
        return False, "present is not true (the fixture carries metadata)"
    output = result.get("output")
    if not isinstance(output, dict):
        return False, "present delivery without output facts"
    for key in OUTPUT_FACTS:
        if key not in output:
            return False, "output lacks %r" % key
    # The products publish the path fact as either the caller
    # spelling or its absolute form; both identify the delivered
    # file.
    if output.get("path") not in (destination, os.path.abspath(destination)):
        return False, "output.path %r not %r nor %r" % (
            output.get("path"), destination, os.path.abspath(destination))
    if not os.path.exists(destination):
        return False, "output file %r missing" % destination
    expected_digest = hashlib.sha256(expected_bytes).hexdigest()
    actual = hashlib.sha256(open(destination, "rb").read()).hexdigest()
    if output.get("sha256") != expected_digest:
        return False, "claimed digest %s != fixture metadata digest %s" % (
            output.get("sha256"), expected_digest)
    if actual != expected_digest:
        return False, "output file digest %s != fixture metadata digest %s" % (
            actual, expected_digest)
    if os.path.getsize(destination) != len(expected_bytes):
        return False, "output file size %d != expected %d" % (
            os.path.getsize(destination), len(expected_bytes))
    return True, ""


def product_all_ok(product):
    """Aggregate verdict of one product record: every case ok plus
    the source-integrity, sidecar-absence, and reopening facts (all
    stored inside the product cases record)."""
    cases = product.get("cases", {})
    return (
        all(v.get("ok", True) for k, v in cases.items() if isinstance(v, dict))
        and cases.get("source_unchanged", False)
        and cases.get("sidecar_absent_after", False)
        and cases.get("reopen_allowed", False)
    )


# Executed-control count of the pure-function battery.  The battery is a flat
# list of expect() calls plus a few inside one fixture loop, so a dropped or
# unreachable control lowers the count while still printing nothing but PASS
# for the survivors.  "0 failures" is only evidence when the number that ran
# is pinned.
GUARD_SELF_TEST_CONTROLS = 39
# The destination-comparison control compares backslash-spelled Windows
# paths, so it is unreachable on a POSIX run; the pin covers both shapes
# rather than being loosened to whatever the current host happens to reach.
GUARD_SELF_TEST_NATIVE_ONLY = 1


def selftest():
    """Pure-function regression battery for the strict validators
    (wave 19 round 19.14 astra P2): every counterexample that the old
    weak evaluator accepted must now be rejected, and a genuine
    successful delivery must still pass."""
    import tempfile
    ok = True
    executed = [0]

    def expect(label, cond):
        nonlocal ok
        executed[0] += 1
        if not cond:
            print("SELFTEST FAIL: %s" % label)
            ok = False

    # astra's executed counterexample: result:{} for successful calls.
    expect("result {} rejected",
           not validate_success_response({"result": {}}, METHOD, r"C:\x\meta.txt")[0])
    expect("missing present rejected",
           not validate_success_response(
               {"result": {"method": METHOD}}, METHOD, r"C:\x\meta.txt")[0])
    expect("present without output rejected",
           not validate_success_response(
               {"result": {"method": METHOD, "present": True}},
               METHOD, r"C:\x\meta.txt")[0])
    expect("impossible digest rejected",
           not validate_success_response(
               {"result": {"method": METHOD, "present": True, "output": {
                   "bytes": "48", "rows": "1", "sha256": "0" * 64,
                   "path": r"C:\x\meta.txt"}}},
               METHOD, r"C:\x\meta.txt")[0])

    with tempfile.TemporaryDirectory(prefix="iprange-guard-selftest-") as td:
        dest = os.path.join(td, "meta.txt")
        claimed = hashlib.sha256(FIXTURE_METADATA).hexdigest()
        response = {"result": {"method": METHOD, "present": True, "output": {
            "bytes": str(len(FIXTURE_METADATA)), "rows": "1", "sha256": claimed,
            "path": dest}}}
        # astra's counterexample: omit meta.txt entirely.
        expect("omitted file rejected",
               not validate_success_response(response, METHOD, dest)[0])
        # astra's counterexample: empty pre-existing reopen file.
        with open(dest, "wb") as fh:
            fh.write(b"")
        expect("empty file rejected",
               not validate_success_response(response, METHOD, dest)[0])
        # The genuine fixture metadata bytes pass.
        with open(dest, "wb") as fh:
            fh.write(FIXTURE_METADATA)
        expect("genuine delivery accepted",
               validate_success_response(response, METHOD, dest)[0])
        # present:false with a leftover destination (a missed guard).
        expect("present:false with existing dest rejected",
               not validate_success_response(
                   {"result": {"method": METHOD, "present": False}},
                   METHOD, dest)[0])
        # astra turn-5 counterexample: present:false with the
        # destination absent (a legal answer for a metadata-less
        # database, but the guard fixture carries metadata, so the
        # allowed controls must deliver real bytes).
        missing = os.path.join(td, "missing.txt")
        expect("present:false with absent dest rejected",
               not validate_success_response(
                   {"result": {"method": METHOD, "present": False}},
                   METHOD, missing)[0])
        # astra turn-5 counterexample: an empty delivered file that
        # matches its own claimed empty digest must not pass.
        empty_dest = os.path.join(td, "empty.txt")
        with open(empty_dest, "wb") as fh:
            fh.write(b"")
        empty_claim = {"result": {"method": METHOD, "present": True, "output": {
            "bytes": "0", "rows": "1",
            "sha256": hashlib.sha256(b"").hexdigest(),
            "path": empty_dest}}}
        expect("self-consistent empty digest rejected",
               not validate_success_response(empty_claim, METHOD, empty_dest)[0])
        # astra turn-5 counterexample: strict schema violations are
        # rejected through validate_result (numeric bytes, null rows,
        # unknown result member).
        with open(dest, "wb") as fh:
            fh.write(FIXTURE_METADATA)
        bad_types = {"result": {"method": METHOD, "present": True, "output": {
            "bytes": len(FIXTURE_METADATA), "rows": None, "sha256": claimed,
            "path": dest, "extra": 1}}}
        expect("schema violations rejected",
               not validate_success_response(bad_types, METHOD, dest)[0])
        wrong_method = {"result": {"method": "iprange.v1.reader.lookup",
                                   "present": True, "output": {
                                       "bytes": str(len(FIXTURE_METADATA)),
                                       "rows": "1", "sha256": claimed,
                                       "path": dest}}}
        expect("wrong method echo rejected",
               not validate_success_response(wrong_method, METHOD, dest)[0])

    # guard_cases must build on both platform shapes without
    # crashing and with the platform-appropriate case mix (wave 19
    # round 19.14 regression: conditional None entries once made the
    # POSIX negative control crash with an unpacking TypeError).
    posix_cases = guard_cases("/tmp/guard-work")
    win_cases = guard_cases("C:\\guard-work")
    # The extended-length destination families require backslash
    # separators; a forward-slash work spelling must generate the same
    # canonical destinations (wave-19.18 security verdict F1 harness
    # reproducibility).
    if IS_WINDOWS:
        expect("forward-slash work builds identical destinations",
               {(c[0], c[2]) for c in guard_cases("C:/guard-work")}
               == {(c[0], c[2]) for c in win_cases})
    expect("posix cases non-empty", len(posix_cases) > 0)
    expect("win cases non-empty", len(win_cases) > 0)
    expect("every case is a triple",
           all(len(c) == 3 for c in posix_cases + win_cases))
    names_posix = {c[0] for c in posix_cases}
    names_win = {c[0] for c in win_cases}
    expect("posix has no drive-root case", "drive_root_allowed" not in names_posix)
    expect("win has drive-root case", "drive_root_allowed" in names_win)
    expect("posix has no drive-relative case", "drive_relative" not in names_posix)
    expect("win has drive-relative case", "drive_relative" in names_win)
    # The verbatim (extended-length) spelling is a Windows name family:
    # on POSIX it is a relative path whose parents never exist, so the
    # delivery cannot be exercised (wave 19 round 19.14).
    expect("win covers the verbatim sidecar",
           "verbatim_sidecar" in names_win)
    expect("posix skips the verbatim sidecar",
           "verbatim_sidecar" not in names_posix)
    expect("win covers the NT namespace sidecar",
           "nt_namespace_sidecar" in names_win)
    expect("posix skips the NT namespace sidecar",
           "nt_namespace_sidecar" not in names_posix)
    expect("win covers the forward-slash verbatim sidecar",
           "verbatim_fwd_sidecar" in names_win)
    expect("posix skips the forward-slash verbatim sidecar",
           "verbatim_fwd_sidecar" not in names_posix)
    expect("win covers the UNC loopback sidecar",
           "unc_loopback_sidecar" in names_win)
    expect("posix skips the UNC loopback sidecar",
           "unc_loopback_sidecar" not in names_posix)
    expect("win covers the volume-GUID sidecar only natively",
           ("volume_guid_sidecar" in names_win) == IS_WINDOWS)
    expect("posix skips the volume-GUID sidecar",
           "volume_guid_sidecar" not in names_posix)
    # Namespace head x trailing-dot/space leaf class (wave-19.18
    # parity P1-2 / P2-3): the UNC-head rows are static spellings and
    # must exist on Windows and be skipped on POSIX; the GLOBALROOT
    # and volume-GUID rows use natively discovered heads and must
    # exist only on native Windows runs, like their exact-leaf
    # siblings.
    unc_trail = ("verbatim_unc_trailing_dot", "verbatim_unc_trailing_space",
                 "verbatim_unc_lower_trailing_dot", "verbatim_unc_lower_trailing_space",
                 "nt_unc_trailing_dot", "nt_unc_trailing_space",
                 "nt_unc_lower_trailing_dot", "nt_unc_lower_trailing_space")
    expect("win covers the UNC head x trailing leaf class",
           all(name in names_win for name in unc_trail))
    expect("posix skips the UNC head x trailing leaf class",
           not any(name in names_posix for name in unc_trail))
    globalroot_trail = ("globalroot_trailing_dot", "globalroot_trailing_space",
                        "globalroot_dos_trailing_dot", "globalroot_dos_trailing_space",
                        "globalroot_objectmgr_trailing_dot", "globalroot_objectmgr_trailing_space",
                        "globalroot_lower_trailing_dot", "globalroot_lower_trailing_space",
                        "globalroot_dos_lower_trailing_dot", "globalroot_dos_lower_trailing_space",
                        "globalroot_objectmgr_lower_trailing_dot",
                        "globalroot_objectmgr_lower_trailing_space")
    expect("win GLOBALROOT trail rows ride native head discovery",
           (all(name in names_win for name in globalroot_trail))
           == ("globalroot_sidecar" in names_win))
    expect("posix skips the GLOBALROOT head x trailing leaf class",
           not any(name in names_posix for name in globalroot_trail))
    volume_guid_trail = ("volume_guid_trailing_dot", "volume_guid_trailing_space",
                         "volume_guid_lower_trailing_dot", "volume_guid_lower_trailing_space")
    expect("win covers the volume-GUID head x trailing leaf class natively only",
           (all(name in names_win for name in volume_guid_trail)) == IS_WINDOWS)
    expect("posix skips the volume-GUID head x trailing leaf class",
           not any(name in names_posix for name in volume_guid_trail))
    # The case table is DATA, not a location this harness resolved: a case is
    # named by the Windows spelling of the sidecar it addresses and the report
    # records that spelling verbatim.  A personal-path scan that resolves a
    # drive-relative spelling through the invocation directory judges where the
    # operator launched the harness instead of what the harness measured, and
    # launching a guard run from inside the operator profile turned the harness's
    # own literals into profile paths so the shared writer refused the report
    # (wave-19.25).  This pair is the standing regression gate for that.  Both
    # halves are required: a pin satisfied by only the first could be bought by
    # switching the scan off, so the second proves the scan still fires on a
    # path that really does name the profile.  _IS_WINDOWS is pinned because the
    # drive-relative spelling only exists on the Windows leg; on that leg the
    # control runs natively, with the ambient platform facts.
    win_table = {name: destination
                 for name, _expectation, destination in win_cases}
    guard_report = {"schema": REPORT_SCHEMA, "products": {"go": {"cases": {
        name: {"destination": destination, "ok": True}
        for name, destination in win_table.items()}}}}
    expect("the windows case table carries a drive-relative literal",
           any(destination.startswith("C:")
               and destination[2:3] not in ("/", chr(92))
               for destination in win_table.values()))
    profile = command_sanitize.profile_path()
    saved_windows = command_sanitize._IS_WINDOWS
    saved_cwd = os.getcwd()
    try:
        command_sanitize._IS_WINDOWS = True
        os.chdir(profile or saved_cwd)
        expect("the case table survives the personal-path scan from inside the"
               " operator profile",
               personal_path_in_report(guard_report) is None)
        planted_refused = True
        if profile:
            planted = json.loads(json.dumps(guard_report))
            planted["products"]["go"]["cases"]["planted"] = {
                "destination": profile + os.sep + "leaked.iprange",
                "ok": True}
            planted_refused = personal_path_in_report(planted) is not None
        expect("a planted real profile path in the same report shape is refused",
               planted_refused)
    finally:
        command_sanitize._IS_WINDOWS = saved_windows
        os.chdir(saved_cwd)
    wanted = (GUARD_SELF_TEST_CONTROLS
              - (0 if IS_WINDOWS else GUARD_SELF_TEST_NATIVE_ONLY))
    if executed[0] != wanted:
        print("SELFTEST FAIL: %d controls executed, expected %d"
              % (executed[0], wanted))
        ok = False

    # The count travels with the verdict so --self-test can report exactly how
    # many controls ran, not merely that nothing failed.
    return ok, executed[0]


def windows_c_volume_guid():
    """Device path of the C: volume ("\\\\?\\\\Volume{...}\\\\") on a native
    Windows host, or None elsewhere: the volume-GUID spelling of a
    path names the same file as the drive-letter spelling, so the
    guard must refuse it like the other sidecar spellings (wave 19
    round 19.15 security P1)."""
    if os.name != "nt":
        return None
    import subprocess
    try:
        out = subprocess.run(
            ["powershell", "-NoProfile", "-Command",
             "(Get-CimInstance Win32_Volume -Filter \"DriveLetter='C:'\").DeviceID"],
            capture_output=True, text=True, timeout=30)
        value = out.stdout.strip()
        if value.endswith("\\"):
            return value
        return value + "\\" if value else None
    except Exception:
        return None


def windows_c_globalroot(work):
    """NT device-root prefix backing the C: volume
    ("\\?\\GLOBALROOT\\Device\\HarddiskVolumeN") on a native Windows
    host, or None elsewhere: the GLOBALROOT spelling names the same
    real files as the drive-letter spelling through the kernel, while
    Go's EvalSymlinks cannot walk the intermediate \\Device component,
    so the guard must refuse it through the kernel-identity arm like
    the volume-GUID spelling (wave-19.17 security P1)."""
    if os.name != "nt":
        return None
    for n in range(0, 16):
        probe = r"\\?\GLOBALROOT\Device\HarddiskVolume%d%s" % (n, work[2:])
        try:
            if os.path.isdir(probe):
                return r"\\?\GLOBALROOT\Device\HarddiskVolume%d" % n
        except OSError:
            return None
    return None



def guard_cases(work):
    """Windows-targeted spellings of the absent sidecar plus the
    distinct-destination controls.  Expectation depends on the host
    platform (Windows refuses sidecar spellings, both platforms allow
    the controls).  Each case names the source database basename it
    targets ("db.iprange" for the primary source, "db_\\u00e4.iprange"
    for the non-ASCII fold pair, "A\\u03a31.iprange" for the NTFS
    sigma family)."""
    # Extended-length destinations are NT object-namespace spellings
    # where "/" is a literal name character, never a separator: every
    # "\\?\<work>\..." case (and the GLOBALROOT/volume-GUID head
    # discovery) requires backslashes throughout.  The interpreter's
    # ntpath may rewrite separators to "/", so force the canonical
    # spelling here; a forward-slash caller must produce the identical
    # case set (wave-19.18 harness reproducibility, security verdict
    # F1).
    if IS_WINDOWS:
        work = work.replace("/", "\\")
    drive = work[:2] if len(work) >= 2 and work[1] == ":" else None
    # Win32 strips trailing dots and spaces at create, so these
    # spellings denote the absent sidecar db.iprange.readers.
    dot_space = [
        ("absolute_trailing_dot",
         native_join(work, "db.iprange") + ".readers."),
        ("absolute_trailing_space",
         native_join(work, "db.iprange") + ".readers "),
    ]
    candidates = (
        [
            ("drive_relative", "%sdb.iprange.readers" % drive),
            ("drive_relative_upper", "%sDB.IPRANGE.READERS" % drive),
        ]
        if drive
        else []
    )
    candidates += [
        ("absolute_upper", native_join(work, "DB.IPRANGE.READERS")),
    ]
    # Relative-source spellings (astra turn-5 P1): the service runs
    # with cwd=work, so a "$rel:" source names the same fixture the
    # absolute spellings address; the relative sidecar derivation must
    # still be refused through the cross-family namespace arm (Go used
    # to collapse the relative spelling's ancestor to the drive root).
    candidates.append(
        ("relative_source_control_allowed", "$rel:./db.iprange",
         native_join(work, "meta-rel.txt")))
    if drive:
        # rooted-without-volume spelling exists only on Windows; on
        # POSIX the same string is a plain relative file name.
        candidates.append(
            ("rooted_sidecar",
             "\\" + work[len(drive):].lstrip("\\/")
             + "\\db.iprange.readers"))
    candidates += dot_space + [
        ("non_ascii", native_join(work, "DB_\u00c4.IPRANGE.READERS")),
        # NTFS upcase-name equivalence of the Greek sigma class (wave
        # 19 round 19.14 astra P1): "\u03c3" spells the absent sidecar
        # of source "A\u03a31.iprange" (the contextual lowercase fold
        # splits U+03A3 into U+03C2/U+03C3 and would let the spelling
        # escape); the final-sigma U+03C2 spelling is refused
        # conservatively on volumes that separate it, matching the
        # fold.
        ("ntfs_sigma", native_join(work, "A\u03c31.iprange.readers")),
        ("ntfs_final_sigma", native_join(work, "A\u03c21.iprange.readers")),
    ]
    if drive:
        # Extended-length spelling (wave 19 round 19.14 astra P1): the
        # verbatim name of the absent sidecar, refused canonically.
        # Windows-only: on POSIX the spelling is a relative path whose
        # parents never exist, so the delivery cannot be exercised;
        # the GOOS-gated no-strip behavior is pinned by both engines'
        # unit tables.
        candidates.append(
            ("verbatim_sidecar", "\\\\?\\" + work + "\\db.iprange.readers"))
        # NT object-manager spelling of the verbatim family (wave 19
        # round 19.15 security P1): "\\??\C:\...\db.iprange.readers"
        # names the same absent sidecar and must be refused
        # canonically like "\\?\".  Windows-only, same skip rule as
        # verbatim_sidecar.
        candidates.append(
            ("nt_namespace_sidecar", "\\??\\" + work + "\\db.iprange.readers"))
        # Forward-slash verbatim spelling (wave 19 round 19.15
        # security P2): the kernel rejects slash I/O for the "\\?\"
        # family, but the guarded identity is the same file, so both
        # engines refuse it canonically (Go canonicalAbsolute now
        # normalizes separators like Rust PathBuf).  Windows-only,
        # same skip rule.
        candidates.append(
            ("verbatim_fwd_sidecar",
             "\\\\?\\" + work.replace("\\", "/") + "/db.iprange.readers"))
        # Cross-family spellings (wave 19 round 19.15 security P1):
        # loopback UNC and volume-GUID namespaces name the same real
        # files as the drive-letter spelling; no lexical strip can
        # reconcile them, so the guard compares the kernel file
        # identity of the deepest existing ancestor plus the folded
        # suffix.  Windows-only, same skip rule: on POSIX these are
        # relative or unresolvable spellings and the delivery cannot
        # be exercised.
        candidates.append(
            ("unc_loopback_sidecar",
             "\\\\localhost\\C$" + work[2:] + "\\db.iprange.readers"))
        candidates.append(
            ("unc_loopback_ip_sidecar",
             "\\\\127.0.0.1\\C$" + work[2:] + "\\db.iprange.readers"))
        candidates.append(
            ("verbatim_unc_loopback_sidecar",
             "\\\\?\\UNC\\localhost\\C$" + work[2:] + "\\db.iprange.readers"))
        candidates.append(
            ("nt_unc_loopback_sidecar",
             "\\??\\UNC\\localhost\\C$" + work[2:] + "\\db.iprange.readers"))
        # Namespace head x trailing-dot/space leaf (wave-19.18 parity
        # P1-2 / P2-3): Win32 strips a trailing dot or space from the
        # final component at create/open, so each head spelling below
        # denotes the same absent reader sidecar as the plain spelling
        # and must be refused.  These rows pin the combined class of
        # the 65-row reviewer probe (canonical and lowercase UNC
        # heads; GLOBALROOT and volume-GUID rows follow with their
        # natively discovered heads).
        for head, stem in (
                (r"\\?\UNC\localhost\C$", "verbatim_unc"),
                (r"\\?\unc\localhost\c$", "verbatim_unc_lower"),
                (r"\??\UNC\localhost\C$", "nt_unc"),
                (r"\??\unc\localhost\c$", "nt_unc_lower")):
            for leaf, leaf_name in ((".", "trailing_dot"), (" ", "trailing_space")):
                candidates.append((stem + "_" + leaf_name,
                                   head + work[2:] + "\\db.iprange.readers" + leaf))
        volume_guid = windows_c_volume_guid()
        if volume_guid:
            candidates.append(
                ("volume_guid_sidecar",
                 volume_guid + work[2:] + "\\db.iprange.readers"))
            for head, stem in ((volume_guid, "volume_guid"),
                               (volume_guid.lower(), "volume_guid_lower")):
                for leaf, leaf_name in ((".", "trailing_dot"), (" ", "trailing_space")):
                    candidates.append((stem + "_" + leaf_name,
                                       head + work[2:] + "\\db.iprange.readers" + leaf))
        # NT device-root spellings (wave-19.17 security P1): the
        # GLOBALROOT device namespace names the same real files as the
        # drive-letter spelling; the guard must refuse it through the
        # kernel-identity arm exactly like the volume-GUID spelling.
        # Windows-only, same skip rule as the other cross-family
        # spellings.
        globalroot = windows_c_globalroot(work)
        if globalroot:
            candidates.append(
                ("globalroot_sidecar",
                 globalroot + work[2:] + "\\db.iprange.readers"))
            candidates.append(
                ("globalroot_device_sidecar",
                 globalroot.replace(r"\\?\GLOBALROOT", r"\\.\GLOBALROOT")
                 + work[2:] + "\\db.iprange.readers"))
            # GLOBALROOT device-root heads x trailing-dot/space leaf
            # (wave-19.18 parity P1-2 / P2-3).  The replace-based
            # derivations follow the existing globalroot_device_sidecar
            # pattern; the object-manager twin and the lowercase heads
            # are the spellings the 65-row probe diverged on
            # (report7.json F-traildot/G-trailspace x
            # globalroot_objectmgr[,_lower], globalroot_verbatim[,_lower],
            # volume_guid[,_lower]) together with the UNC rows above.
            for head, stem in (
                    (globalroot, "globalroot"),
                    (globalroot.replace(r"\\?\GLOBALROOT", r"\\.\GLOBALROOT"),
                     "globalroot_dos"),
                    (globalroot.replace(r"\\?\GLOBALROOT", r"\??\GLOBALROOT"),
                     "globalroot_objectmgr"),
                    (globalroot.lower(), "globalroot_lower"),
                    (globalroot.replace(r"\\?\GLOBALROOT", r"\\.\GLOBALROOT")
                     .lower(), "globalroot_dos_lower"),
                    (globalroot.replace(r"\\?\GLOBALROOT", r"\??\GLOBALROOT")
                     .lower(), "globalroot_objectmgr_lower")):
                for leaf, leaf_name in ((".", "trailing_dot"), (" ", "trailing_space")):
                    candidates.append((stem + "_" + leaf_name,
                                       head + work[2:] + "\\db.iprange.readers" + leaf))
        # Distinct drive-root control with the SAME basename as the
        # absent sidecar (wave 19 round 19.14 astra P1): with the
        # source in the per-drive working directory,
        # "\db.iprange.readers" names the distinct C:\db.iprange.readers
        # and must be published, not refused as the source's sidecar.
        candidates.append(("drive_root_allowed", "\\db.iprange.readers"))
        # Relative source + loopback-UNC sidecar destination: the
        # relative sidecar derivation must be refused through the
        # namespace arm exactly like the absolute spellings
        # (Windows-only: on POSIX the UNC spelling is a plain
        # unresolvable relative name and the delivery cannot be
        # exercised).
        candidates.append(
            ("relative_source_unc_loopback_sidecar", "$rel:./db.iprange",
             "\\\\localhost\\C$" + work[2:] + "\\db.iprange.readers"))
    candidates.append(("control_allowed", native_join(work, "meta.txt")))
    source_base = {
        "non_ascii": "db_\u00e4.iprange",
        "ntfs_sigma": "A\u03a31.iprange",
        "ntfs_final_sigma": "A\u03a31.iprange",
    }
    result = []
    for entry in candidates:
        if len(entry) == 3:
            # A case tuple may carry its own explicit source spelling
            # (the "$rel:" relative-source cases, astra turn-5 P1).
            result.append((entry[0], entry[1], entry[2]))
        else:
            name, path = entry
            result.append((name, source_base.get(name, "db.iprange"), path))
    return result


def run_product(binary, label, work, fixture, provenance):
    product = {}

    binary = os.path.abspath(binary)
    service = HarnessJsonRpcService(
        [binary, "--jsonrpc"], label, cwd=work,
        read_deadline=RPC_READ_DEADLINE_SECONDS,
        write_deadline=RPC_WRITE_DEADLINE_SECONDS,
    )
    try:
        product["binary"] = file_evidence(binary)
        product["implementation"] = describe_implementation(service, label)

        sources = {}
        for base in ("db.iprange", "db_\u00e4.iprange", "A\u03a31.iprange"):
            path = native_join(work, base)
            shutil.copyfile(fixture, path)
            sources[path] = {
                "sha256_before": hashlib.sha256(
                    open(path, "rb").read()
                ).hexdigest(),
                "sidecar_absent_before": not os.path.exists(
                    path + ".readers"
                ),
            }
        product["sources_before"] = {
            path: info for path, info in sources.items()
        }

        cases = {}
        for name, source_base, destination in guard_cases(work):
            if source_base.startswith("$rel:"):
                source = source_base[len("$rel:"):]
            else:
                source = native_join(work, source_base)
            response = service.call(
                "g1", METHOD,
                metadata_get_frame(source, destination),
            )
            refused = "error" in response
            error_data = response.get("error", {}).get("data", {})
            error_code = error_data.get("code")
            error_outcome = error_data.get("outcome")
            error_message = response.get("error", {}).get("message", "")
            if IS_WINDOWS:
                # Every spelling of the absent sidecar is refused; the
                # distinct-destination controls (meta.txt, the drive
                # root) stay allowed (wave 19 round 19.14).
                expected = name not in ALLOWED_DESTINATION_CASES
            else:
                # POSIX negative control: every spelling denotes a
                # distinct path and must stay allowed.
                expected = False
            if not refused:
                # Strict success-facts validation: the exact result
                # schema plus the delivered file matching the claimed
                # digest/bytes (wave 19 round 19.14 astra P2; a bare
                # `result:{}` or an omitted output must never pass).
                success_ok, reason = validate_success_response(
                    response, METHOD, destination)
                if not success_ok:
                    raise AssertionError(
                        "%s metadata.get to %r: invalid success: %s"
                        % (label, destination, reason)
                    )
            cases[name] = {
                "destination": destination,
                "refused": refused,
                "error_code": error_code,
                "error_outcome": error_outcome,
                "error_message": error_message,
                "expected_refused": expected,
                "ok": refused == expected and (
                    not expected
                    or (
                        error_outcome == "not_started"
                        and error_code == "invalid_argument"
                        and error_message == ACCEPTED_MESSAGE
                    )
                ),
            }
            if not cases[name]["ok"]:
                raise AssertionError(
                    "%s metadata.get to %r: %s"
                    % (label, destination, json.dumps(cases[name])[:400])
                )

        # The drive-root control publishes C:\db.iprange.readers; drop
        # the scratch file so the next product and the reopen stay
        # isolated from it (Windows-only: on POSIX the rooted control
        # is skipped and the backslash spelling stays in the scratch
        # working directory).
        if IS_WINDOWS:
            root_dest = os.path.abspath("\\db.iprange.readers")
            if os.path.exists(root_dest):
                os.remove(root_dest)

        # Every source must stay byte-identical with its sidecar still
        # absent after the refusal battery.
        unchanged = True
        sidecars_absent = True
        for path, info in sources.items():
            after = hashlib.sha256(open(path, "rb").read()).hexdigest()
            unchanged = unchanged and after == info["sha256_before"]
            sidecars_absent = sidecars_absent and not os.path.exists(
                path + ".readers"
            )
        cases["source_unchanged"] = unchanged
        cases["sidecar_absent_after"] = sidecars_absent

        # Reopening: one more file delivery against the primary source.
        # The output must be FRESH (a pre-existing file is removed
        # before the call) and must match the claimed digest/bytes
        # (wave 19 round 19.14 astra P2 counterexample: an empty
        # pre-existing reopen file must never count as success).
        reopen = native_join(work, "meta-reopen.txt")
        if os.path.exists(reopen):
            os.remove(reopen)
        primary = native_join(work, "db.iprange")
        response = service.call(
            "g2", METHOD, metadata_get_frame(primary, reopen),
        )
        if "error" in response:
            cases["reopen_allowed"] = False
            raise AssertionError(
                "%s reopen delivery failed: %s"
                % (label, json.dumps(response)[:300])
            )
        reopen_ok, reopen_reason = validate_success_response(
            response, METHOD, reopen)
        cases["reopen_allowed"] = reopen_ok
        if not reopen_ok:
            raise AssertionError(
                "%s reopen delivery invalid: %s" % (label, reopen_reason)
            )

        product["cases"] = cases
        product["all_ok"] = product_all_ok(product)
    finally:
        service.close()

    print("%s: %s" % ("OK " if product["all_ok"] else "BAD", label))
    return product


# ---------------------------------------------------------------------------
# Report verification (--verify-report)
# ---------------------------------------------------------------------------
#
# The guard report is produced on the authorized Windows host (and, as the
# negative control, on POSIX) and reaches ``evidence/`` as a copy, so once the
# harness exits nothing reads the artifact again.  A report nobody can re-grade
# is self-attesting: every refusal code, message and outcome in it could be
# replaced and no downstream gate would notice.  ``--verify-report`` is that
# reader, and the kind gate calls the same function over the committed file.
#
# Every rule below is about content the producer itself wrote, so a report is
# refused only for disagreeing with its own record or with the build ledger --
# never for a choice the harness is free to make.

PRODUCT_LABELS = ("go", "rust")
REPORT_PLATFORMS = ("posix", "windows")
# The distinct-destination controls that must be published, not refused.  One
# constant for the producer (run_product) and the verifier: a fourth control
# that only one of the two knows about is a case the guard silently stops
# proving.
ALLOWED_DESTINATION_CASES = ("control_allowed", "drive_root_allowed",
                             "relative_source_control_allowed")
# The invariants recorded inside the per-product ``cases`` map next to the
# cases themselves (see product_all_ok).
INVARIANT_FLAGS = ("reopen_allowed", "sidecar_absent_after",
                   "source_unchanged")
CASE_FIELDS = ("destination", "error_code", "error_message",
               "error_outcome", "expected_refused", "ok", "refused")
# Case names whose presence depends on what the native host could discover
# (the volume-GUID and GLOBALROOT device heads).  They cannot be required of a
# report: guard_cases() can only produce them where the device namespace
# answers, and a host without such a device writes a truthful report without
# them.
DISCOVERY_DEPENDENT_CASE_PREFIXES = ("globalroot", "volume_guid")


def _is_digest(value):
    """True when ``value`` has the wire form of a measured sha256 digest."""

    return (isinstance(value, str) and len(value) == 64
            and all(character in "0123456789abcdef" for character in value))


def _ledger_tail(path):
    """The build-ledger spelling of one recorded path.

    The report records the staging path the operator chose
    (``C:/msys64/tmp/<run>/win/go/iprange.exe``) while the ledger records its
    own layout (``win/go/iprange.exe``).  ``_tail`` folds the trailing
    components both sides agree on; it expects forward separators, so the
    backslash spelling a Windows interpreter may produce is folded first.
    """

    return _tail(str(path or "").replace("\\", "/"))


def required_case_names(report_platform):
    """Every case name a guard report of one platform must carry.

    Derived from ``guard_cases`` -- the producer's own table -- rather than
    from a second list kept here: a spelling added to the battery is required
    of the evidence the next time it is read, and a spelling removed from the
    battery stops being required, in one change.  The discovery-dependent names
    are excluded for the reason given at DISCOVERY_DEPENDENT_CASE_PREFIXES.
    """

    if report_platform not in REPORT_PLATFORMS:
        return set()
    probe = ("C:\\guardwork\\probe" if report_platform == "windows"
             else "/guardwork/probe")
    return {name for name, _source, _destination in guard_cases(probe)
            if not name.startswith(DISCOVERY_DEPENDENT_CASE_PREFIXES)}


def case_facts_problems(where, label, name, case, report_platform, problems):
    """Require one recorded guard case to describe a real guard decision.

    The three facts that make a guard refusal a refusal are the error code,
    the domain outcome, and the message; together with ``refused`` and
    ``expected_refused`` they are what the harness compared to decide ``ok``.
    Re-deriving ``ok`` from them here is the whole point: a report that
    flipped the verdict without flipping the facts -- or the other way round --
    contradicts itself and is refused.
    """

    missing = sorted(set(CASE_FIELDS) - set(case))
    extra = sorted(set(case) - set(CASE_FIELDS) - set(INVARIANT_FLAGS))
    if missing:
        problems.append(f"{where}: {label} case {name!r} records no "
                        f"{', '.join(missing)}; a guard case without its "
                        f"recorded facts cannot be re-graded")
        return
    if extra:
        problems.append(f"{where}: {label} case {name!r} carries "
                        f"{', '.join(extra)}, which this harness never "
                        f"records")
        return
    if report_platform == "windows":
        want_expected = name not in ALLOWED_DESTINATION_CASES
    elif report_platform == "posix":
        want_expected = False
    else:
        want_expected = bool(case.get("expected_refused"))
    expected = case.get("expected_refused")
    if expected is not want_expected:
        problems.append(
            f"{where}: {label} case {name!r} records expected_refused="
            f"{expected!r} on a {report_platform!r} run; the guard refuses "
            f"every sidecar spelling on Windows and nothing on POSIX, so the "
            f"expectation is a function of the platform and the case name")
        return
    refused = case.get("refused")
    if refused is not expected:
        problems.append(
            f"{where}: {label} case {name!r} expected a refusal and records "
            f"refused={refused!r}; a guard case that did not do what the "
            f"battery expected is a failure, not an ok")
        return
    if expected:
        for field, want in (("error_code", "invalid_argument"),
                            ("error_outcome", "not_started"),
                            ("error_message", ACCEPTED_MESSAGE)):
            if case.get(field) != want:
                problems.append(
                    f"{where}: {label} case {name!r} was refused with "
                    f"{field}={case.get(field)!r}, not {want!r}; the same-source "
                    f"guard answers with one canonical invalid_argument / "
                    f"not_started refusal, and another error is a different "
                    f"failure dressed up as the guard")
    else:
        if case.get("error_code") is not None \
                or case.get("error_outcome") is not None \
                or case.get("error_message") not in ("", None):
            problems.append(
                f"{where}: {label} case {name!r} was not refused but records "
                f"error facts code={case.get('error_code')!r} "
                f"outcome={case.get('error_outcome')!r} "
                f"message={_clip(case.get('error_message'))!r}")
    recomputed = (refused == expected and (not expected or (
        case.get("error_code") == "invalid_argument"
        and case.get("error_outcome") == "not_started"
        and case.get("error_message") == ACCEPTED_MESSAGE)))
    if case.get("ok") is not recomputed:
        problems.append(
            f"{where}: {label} case {name!r} records ok="
            f"{case.get('ok')!r} but its own recorded facts give "
            f"{recomputed}; the verdict is derived from the facts, so the two "
            f"cannot disagree")


def _clip(text, limit=70):
    collapsed = " ".join(str(text or "").split())
    return collapsed if len(collapsed) <= limit else collapsed[:limit] + "..."


def _verify_product(where, label, product, report_platform, fixture, ledger):
    """Grade one product record of a guard report."""

    problems = []
    if not isinstance(product, dict):
        return [f"{where}: products.{label} is not an object"]
    if product.get("implementation") != label:
        problems.append(
            f"{where}: products.{label}.implementation "
            f"{product.get('implementation')!r} does not name {label!r}; the "
            f"harness asked that binary who it is through system.describe, "
            f"and the answer is part of the evidence")
    binary = product.get("binary")
    if not isinstance(binary, dict):
        problems.append(f"{where}: products.{label} records no binary "
                        f"identity")
    else:
        digest = binary.get("sha256")
        if not _is_digest(digest):
            problems.append(f"{where}: products.{label}.binary.sha256 "
                            f"{digest!r} is not a measured digest")
        elif not isinstance(binary.get("path"), str) or not binary["path"]:
            problems.append(f"{where}: products.{label}.binary.path is "
                            f"missing, so the digest cannot be certified")
        elif not isinstance(binary.get("size"), int) or binary["size"] <= 0:
            problems.append(f"{where}: products.{label}.binary.size "
                            f"{binary.get('size')!r} is not a positive byte "
                            f"count of a real executable")
        elif ledger is not None:
            tail = _ledger_tail(binary.get("path"))
            if tail not in ledger:
                problems.append(
                    f"{where}: products.{label} ({binary.get('path')!r}) is "
                    f"not attested by the sha256 ledger")
            elif digest != ledger[tail]:
                problems.append(
                    f"{where}: products.{label}.sha256 does not match the "
                    f"ledger entry for {tail} (report {digest!r}, ledger "
                    f"{ledger[tail]!r})")
    sources = product.get("sources_before")
    if not isinstance(sources, dict) or not sources:
        problems.append(f"{where}: products.{label} records no sources_before; "
                        f"the guard is proved against a real source database, "
                        f"and its identity is the first fact of the run")
    else:
        for path in sorted(sources):
            record = sources[path]
            if not isinstance(record, dict):
                problems.append(f"{where}: products.{label} source {path!r} "
                                f"is not an object")
                continue
            if record.get("sha256_before") != fixture:
                problems.append(
                    f"{where}: products.{label} source {path!r} records "
                    f"sha256_before {record.get('sha256_before')!r}, not the "
                    f"fixture digest {fixture!r}; the run copies the fixture "
                    f"into place, so a source that is not the fixture is a "
                    f"different measurement")
            if record.get("sidecar_absent_before") is not True:
                problems.append(
                    f"{where}: products.{label} source {path!r} did not start "
                    f"with its reader sidecar absent; a refusal over an "
                    f"already-present sidecar is not the guard being proved")
    cases = product.get("cases")
    if not isinstance(cases, dict) or not cases:
        problems.append(f"{where}: products.{label} records no cases; the "
                        f"guard was exercised on nothing")
        return problems
    names = {name for name in cases if name not in INVARIANT_FLAGS}
    required = required_case_names(report_platform)
    absent = sorted(required - names)
    if absent:
        problems.append(
            f"{where}: products.{label} omits {len(absent)} of the "
            f"{len(required)} guard spellings this harness produces (for "
            f"example {absent[:4]}); a battery with the hard rows removed is "
            f"not the battery that was run")
    universe = required | {name for name in names
                           if name.startswith(DISCOVERY_DEPENDENT_CASE_PREFIXES)}
    invented = sorted(names - universe)
    if invented:
        problems.append(
            f"{where}: products.{label} records {len(invented)} case(s) no "
            f"guard battery produces ({invented[:4]}); an invented row is not "
            f"evidence of a refusal")
    for name in sorted(names):
        case = cases[name]
        if not isinstance(case, dict):
            problems.append(f"{where}: products.{label} case {name!r} is not "
                            f"an object")
            continue
        case_facts_problems(where, label, name, case, report_platform,
                            problems)
    for flag in INVARIANT_FLAGS:
        if flag not in cases:
            problems.append(
                f"{where}: products.{label} records no {flag}; the guard is "
                f"only proved when the refusal left the source untouched, its "
                f"sidecar still absent, and a later delivery still works")
        elif cases[flag] is not True:
            problems.append(f"{where}: products.{label} {flag} is "
                            f"{cases[flag]!r}; the battery raises instead of "
                            f"recording a failure, so a false flag here is a "
                            f"report written after the fact")
    if product.get("all_ok") is not True:
        problems.append(f"{where}: products.{label}.all_ok is "
                        f"{product.get('all_ok')!r}")
    elif product.get("all_ok") != product_all_ok(product):
        problems.append(f"{where}: products.{label}.all_ok disagrees with the "
                        f"cases it carries")
    return problems


def verify_report(report, ledger_path=None, where="windows-guard.json"):
    """Re-validate one committed same-source-guard report.

    Re-applies the shared committed-report identity and privacy rules, this
    harness's own schema expectations, the build-ledger digests, and the
    cross-field identities a guard report must satisfy: every refusal case
    must carry the canonical refusal facts and an ``ok`` that follows from
    them, every allowed case must carry no error facts, every spelling the
    guard battery produces must be present, the source must be the fixture the
    ledger attests, and the per-product and report aggregates must equal what
    their own records say.  Every rule is a problem string; an empty list is
    a pass.

    ``ledger_path`` names a ``sha256sum``-format file.  Without it the
    digests are checked for shape only, and the caller says so out loud:
    an uncertified digest is a weaker verdict, not a passing one.
    """

    if not isinstance(report, dict):
        return [f"{where}: report is not an object"]
    entry = COMMITTED_REPORT_WRITERS.get("windows_guard_harness.py", {})
    problems = list(committed_report_problems(
        report, where=where, require_privacy=True,
        screened=entry.get("screened")))
    if report.get("schema") != REPORT_SCHEMA:
        problems.append(f"{where}: unexpected schema "
                        f"{report.get('schema')!r}")
        return problems
    report_platform = report.get("platform")
    if report_platform not in REPORT_PLATFORMS:
        problems.append(f"{where}: platform {report_platform!r} is neither "
                        f"'windows' nor the POSIX negative control 'posix'")
    provenance = report.get("build_provenance")
    if report.get("provenance") is not None \
            and report.get("provenance") != provenance:
        problems.append(
            f"{where}: 'provenance' and 'build_provenance' hold different "
            f"objects; the producer records the same provenance under both "
            f"names, so the two disagreeing is a hand edit")
    if isinstance(provenance, dict) \
            and isinstance(provenance.get("revision"), str) \
            and provenance["revision"] != report.get("git_head"):
        problems.append(
            f"{where}: build_provenance.revision "
            f"{provenance['revision'][:12]!r} is not the revision the report "
            f"itself names ({str(report.get('git_head'))[:12]!r}); the "
            f"qualification and the source it qualified are one thing or "
            f"nothing")
    fixture = (report.get("fixture") or {}).get("sha256") \
        if isinstance(report.get("fixture"), dict) else None
    if not _is_digest(fixture):
        problems.append(f"{where}: fixture.sha256 {fixture!r} is not a "
                        f"measured digest; every refused spelling is the "
                        f"sidecar of a copy of this file")
    ledger = None
    if ledger_path:
        ledger, ledger_problems = _read_sha256_ledger(ledger_path)
        problems.extend(ledger_problems)
        # The Windows run qualifies the fixture the battery staged under
        # ``win/``, so its digest is certifiable.  The POSIX negative control
        # is invoked against a per-run private copy of that fixture, which no
        # build ledger can name; requiring it there would reject a truthful
        # control, so the fixture is certified only where it is a staged
        # artifact.  The sources-vs-fixture identity below still binds every
        # refusal to the file the run actually copied into place.
        if (ledger and report_platform == "windows"
                and isinstance(report.get("fixture"), dict)):
            tail = _ledger_tail(report["fixture"].get("path"))
            if tail not in ledger:
                problems.append(f"{where}: the fixture "
                                f"{report['fixture'].get('path')!r} is not "
                                f"attested by the sha256 ledger")
            elif fixture != ledger[tail]:
                problems.append(
                    f"{where}: fixture.sha256 does not match the ledger entry "
                    f"for {tail} (report {fixture!r}, ledger "
                    f"{ledger[tail]!r})")
    products = report.get("products")
    if not isinstance(products, dict) or set(products) != set(PRODUCT_LABELS):
        recorded = (sorted(products) if isinstance(products, dict)
                    else products)
        problems.append(
            f"{where}: products must hold exactly go and rust, not "
            f"{recorded!r}; the guard verdict is a both-engine claim")
        return problems
    digests = {}
    for label in PRODUCT_LABELS:
        problems.extend(_verify_product(where, label, products[label],
                                        report_platform, fixture, ledger))
        record = (products[label].get("binary") or {}) \
            if isinstance(products[label], dict) else {}
        if _is_digest(record.get("sha256")):
            digests[label] = record["sha256"]
    if len(digests) == 2 and digests["go"] == digests["rust"]:
        problems.append(
            f"{where}: the go and rust records name the same binary "
            f"{digests['go']}; a guard battery that ran one executable twice "
            f"is not a two-engine observation")
    if products["go"].get("cases") == products["rust"].get("cases"):
        problems.append(
            f"{where}: the two products record identical case blocks; each "
            f"product runs in its own working directory, so one block was "
            f"copied onto the other")
    aggregate = all((products[label].get("all_ok") is True)
                    for label in PRODUCT_LABELS)
    if report.get("all_ok") is not True:
        problems.append(f"{where}: all_ok is {report.get('all_ok')!r}; the "
                        f"guard report is consumed as a pass verdict or not "
                        f"at all")
    elif report.get("all_ok") != aggregate:
        problems.append(f"{where}: all_ok disagrees with the per-product "
                        f"verdicts it carries")
    return problems


# ``--verify-report`` needs its own controls, pinned as a count for the same
# reason the kind gate pins its battery: a control that stops running is the
# failure mode this verifier exists to close.
VERIFY_SELF_TEST_CONTROLS = {"accept": 1, "reject": 9}


def _control_case(name, destination, expected_refused):
    if expected_refused:
        return {"destination": destination, "refused": True,
                "error_code": "invalid_argument",
                "error_message": ACCEPTED_MESSAGE,
                "error_outcome": "not_started",
                "expected_refused": True, "ok": True}
    return {"destination": destination, "refused": False,
            "error_code": None, "error_message": "",
            "error_outcome": None, "expected_refused": False, "ok": True}


def _control_product(label, binary_path, digest, work, fixture_digest):
    cases = {}
    for name in sorted(required_case_names("windows")):
        allowed = name in ALLOWED_DESTINATION_CASES
        cases[name] = _control_case(
            name, work + ("/meta.txt" if allowed
                          else "/db.iprange.readers"), not allowed)
    cases.update({"source_unchanged": True, "sidecar_absent_after": True,
                  "reopen_allowed": True})
    return {"implementation": label,
            "binary": {"path": binary_path, "sha256": digest,
                       "size": 14071808, "mtime": 1789468364.9},
            "sources_before": {
                work + "/db.iprange": {
                    "sha256_before": fixture_digest,
                    "sidecar_absent_before": True}},
            "cases": cases, "all_ok": True}


def _verification_self_test():
    """Drive ``verify_report`` over an accepted report and its mutations.

    Offline: the baseline report and the build ledger are synthesized in owned
    scratch and the report goes through the shared committed-report writer, so
    its provenance and privacy blocks are the real thing and every control
    names the defect it must catch.
    """

    problems = []
    executed = {"accept": 0, "reject": 0}
    room = owned_temp_dir("wg-verify-")
    try:
        go_path = "C:/stage/win/go/iprange.exe"
        rust_path = "C:/stage/win/rust/iprange.exe"
        fixture_path = "C:/stage/win/fixture.iprange"
        go_sha = "1" * 63 + "a"
        rust_sha = "2" * 63 + "b"
        fixture_sha = "3" * 63 + "c"
        ledger = os.path.join(room, "SHASUMS.txt")
        with open(ledger, "w", encoding="utf-8") as stream:
            stream.write(f"{go_sha}  win/go/iprange.exe\n")
            stream.write(f"{rust_sha}  win/rust/iprange.exe\n")
            stream.write(f"{fixture_sha}  win/fixture.iprange\n")
        head = report_provenance()["git_head"]

        def baseline():
            return {
                "schema": REPORT_SCHEMA, "platform": "windows",
                "fixture": {"path": fixture_path, "sha256": fixture_sha,
                            "size": 16384, "mtime": 1789468365.6},
                "products": {
                    "go": _control_product("go", go_path, go_sha,
                                           "C:/stage/work/go", fixture_sha),
                    "rust": _control_product("rust", rust_path, rust_sha,
                                             "C:/stage/work/rust",
                                             fixture_sha)},
                "all_ok": True,
                "build_provenance": {"revision": head, "tree_clean": True},
            }

        def written(extra=None):
            report = baseline()
            if extra:
                extra(report)
            if report.get("build_provenance") is not None:
                report["provenance"] = report["build_provenance"]
            dest = os.path.join(room, "report.json")
            write_committed_report(
                dest, report,
                argv=[os.path.basename(__file__), "--verify-report-control"],
                caller_paths=[("--rust", rust_path), ("--go", go_path),
                              ("--fixture", fixture_path),
                              ("--work", "C:/stage/work"),
                              ("--out", dest),
                              ("--provenance", os.path.join(room, "p.json"))])
            with open(dest, encoding="utf-8") as stream:
                return json.load(stream)

        def expect(label, report, must_name):
            executed["reject" if must_name else "accept"] += 1
            found = verify_report(report, ledger_path=ledger, where=label)
            if must_name:
                if not any(must_name in problem for problem in found):
                    problems.append(f"P3 {label}: not refused ({found[:2]})")
                else:
                    print(f"[P3] {label} refused")
            elif found:
                problems.append(f"P3 {label}: accepted baseline refused "
                                f"{found[:2]}")
            else:
                print(f"[P3] {label} accepted")

        expect("baseline guard report", written(), None)

        def forge_all_refusal_facts(report):
            # The reviewer repro: replace every refusal code, message and
            # outcome in the report while leaving the verdicts green.
            for product in report["products"].values():
                for name, case in product["cases"].items():
                    if isinstance(case, dict) \
                            and case.get("expected_refused"):
                        case["error_code"] = "io"
                        case["error_outcome"] = "started"
                        case["error_message"] = "unrelated failure"

        expect("every refusal fact replaced under a green ok",
               written(forge_all_refusal_facts),
               "was refused with error_code")

        def drop_a_hard_case(report):
            for product in report["products"].values():
                del product["cases"]["verbatim_sidecar"]

        expect("guard spellings removed from the battery",
               written(drop_a_hard_case), "omits")

        def claim_a_refusal_that_was_allowed(report):
            product = report["products"]["go"]
            case = product["cases"]["absolute_upper"]
            case["refused"] = False
            case["expected_refused"] = False

        expect("a sidecar spelling recorded as allowed",
               written(claim_a_refusal_that_was_allowed),
               "expected_refused=False on a 'windows' run")

        def ok_disagrees_with_facts(report):
            product = report["products"]["go"]
            product["cases"]["ntfs_sigma"]["error_message"] = "something else"

        expect("ok kept green over contradicting facts",
               written(ok_disagrees_with_facts), "its own recorded facts give")

        def source_is_not_the_fixture(report):
            for product in report["products"].values():
                for record in product["sources_before"].values():
                    record["sha256_before"] = "9" * 64

        expect("sources that are not the attested fixture",
               written(source_is_not_the_fixture), "not the fixture digest")

        def binary_not_in_the_ledger(report):
            report["products"]["go"]["binary"]["sha256"] = "7" * 64

        expect("binary digest the ledger does not name",
               written(binary_not_in_the_ledger), "does not match the ledger")

        def one_binary_for_both_products(report):
            report["products"]["rust"]["binary"] = dict(
                report["products"]["go"]["binary"])
            report["products"]["rust"]["cases"] = dict(
                report["products"]["go"]["cases"])

        expect("both products ran the same executable",
               written(one_binary_for_both_products), "one executable twice")

        def provenance_names_another_revision(report):
            report["build_provenance"]["revision"] = "ab" * 20

        expect("provenance revision contradicts the report revision",
               written(provenance_names_another_revision),
               "is not the revision the report itself names")

        def reopen_never_happened(report):
            for product in report["products"].values():
                product["cases"]["reopen_allowed"] = False

        expect("reopen invariant dropped", written(reopen_never_happened),
               "reopen_allowed")
    finally:
        shutil.rmtree(room, ignore_errors=True)
    return problems, executed


def _verify_main(path, ledger_path):
    """``--verify-report`` entry point: re-check one committed guard report."""

    try:
        with open(path, encoding="utf-8") as stream:
            report = json.load(stream)
    except (OSError, ValueError) as exc:
        print(f"VERIFY {path}: unreadable ({exc})")
        return 1
    problems = verify_report(report, ledger_path=ledger_path,
                            where=os.path.basename(path))
    for problem in problems:
        print(f"VERIFY {problem}")
    if problems:
        print(f"windows-guard report REJECTED: {len(problems)} problem(s) in "
              f"{path}")
        return 1
    print(f"windows-guard report VERIFIED: {path}"
          + ("" if ledger_path else " (no --sha256-ledger supplied; digests "
                                    "were not certified)"))
    return 0



def _self_test_entry():
    """``--self-test``: the validators, the shared provenance controls, and this
    writer's own commit discipline judged from this file's source.

    The audit runs against this module's directory so a tampered copy fails on
    what it actually contains rather than on the pristine file next to it.
    """
    ok, executed = selftest()
    executed_report = [0]

    def _count():  # keeps the printed count the one the battery pinned
        return None

    problems = audit_report_writers(
        cli_dir=os.path.dirname(os.path.abspath(__file__)),
        writers=["windows_guard_harness.py"], artifacts=False)
    for problem in problems:
        print("SELFTEST FAIL: %s" % problem)
    if problems:
        ok = False
    try:
        run_shared_self_test("windows_guard_harness")
    except SystemExit as exc:
        print("SELFTEST FAIL: shared provenance controls: %s" % exc)
        ok = False

    # The verifier is gated by its own controls: a verify_report that stopped
    # refusing anything must fail here, not merely fail to be noticed by
    # whoever reads the committed artifact next.
    verify_problems, verify_executed = _verification_self_test()
    for problem in verify_problems:
        print("SELFTEST FAIL: %s" % problem)
    if verify_problems:
        ok = False
    if verify_executed != VERIFY_SELF_TEST_CONTROLS:
        print("SELFTEST FAIL: verify-report control counts %s, expected %s"
              % (verify_executed, VERIFY_SELF_TEST_CONTROLS))
        ok = False
    print("guard self-test %s: %d controls executed (native-only controls "
          "unreachable on this host: %d), %d report-verification controls "
          "(%d accepted, %d refused)"
          % ("PASSED" if ok else "FAILED", executed,
             0 if IS_WINDOWS else GUARD_SELF_TEST_NATIVE_ONLY,
             sum(VERIFY_SELF_TEST_CONTROLS.values()),
             VERIFY_SELF_TEST_CONTROLS["accept"],
             VERIFY_SELF_TEST_CONTROLS["reject"]))
    return 0 if ok else 1


def main():
    if "--self-test" in sys.argv or "--selftest" in sys.argv:
        return _self_test_entry()
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--rust", help="Rust iprange binary")
    parser.add_argument("--go", help="Go iprange binary")
    parser.add_argument("--fixture",
                        help="immutable v4 database copied into the work dir")
    parser.add_argument("--work",
                        help="fresh working directory (one per product)")
    parser.add_argument("--out", help="evidence JSON path")
    parser.add_argument("--provenance", default=None,
                        help="build provenance JSON recorded verbatim")
    parser.add_argument("--verify-report", metavar="PATH", default=None,
                        help="re-validate a committed windows-guard report "
                             "(shared identity and privacy rules, schema, "
                             "binary and fixture digests against "
                             "--sha256-ledger, every guard spelling, the "
                             "refusal facts behind every ok, and the source "
                             "invariants) and exit")
    parser.add_argument("--sha256-ledger", metavar="PATH", default=None,
                        help="sha256sum-format ledger the staged binaries "
                             "were certified with, used by --verify-report")
    args = parser.parse_args()

    if args.verify_report:
        # Reading an artifact back is not a measurement: none of the
        # run-only inputs apply, and requiring them would push a reviewer to
        # pass dummy paths that then land in the report the next run writes.
        return _verify_main(args.verify_report, args.sha256_ledger)
    for option in ("--rust", "--go", "--fixture", "--work", "--out"):
        if getattr(args, option[2:].replace("-", "_")) is None:
            parser.error("%s is required (unless --verify-report)" % option)

    # Durable-artifact policy, applied before any product starts: the report
    # records the measured fixture/binary paths and the work directory, so a
    # profile-rooted input is how an operator-home path reaches a committed
    # file.  The shared writer repeats the check at commit time.
    caller_paths = (("--rust", args.rust), ("--go", args.go),
                    ("--fixture", args.fixture), ("--work", args.work),
                    ("--out", args.out), ("--provenance", args.provenance))
    require_paths_outside_profile(caller_paths)

    provenance = None
    if args.provenance:
        with open(args.provenance) as fh:
            provenance = json.load(fh)

    report = {
        "schema": REPORT_SCHEMA,
        "platform": "windows" if IS_WINDOWS else os.name,
        "fixture": file_evidence(args.fixture),
        "build_provenance": provenance,
    }

    products = {}
    all_ok = True
    for binary, label in (
        (args.rust, "rust"),
        (args.go, "go"),
    ):
        # Normalize the working directory to the native separator
        # spelling: the extended-length cases build "\\?\<work>\..."
        # destinations and the Win32 verbatim prefix requires
        # backslashes throughout (forward slashes fail the create
        # with error 123).
        work = os.path.normpath(os.path.join(args.work, label))
        if IS_WINDOWS:
            work = work.replace("/", "\\")
        os.makedirs(work, exist_ok=True)
        product = run_product(binary, label, work, args.fixture, provenance)
        product["all_ok"] = product_all_ok(product)
        products[label] = product
        all_ok = all_ok and product["all_ok"]

    report["products"] = products
    report["all_ok"] = all_ok
    if provenance:
        report["provenance"] = provenance
    os.makedirs(os.path.dirname(os.path.abspath(args.out)), exist_ok=True)
    # The shared writer owns command/git_head/checkout_root and the derived
    # privacy block, and refuses the write outright when a recorded path
    # lives under the operator's profile.  This harness runs on both the
    # authorized Windows host and POSIX (guard-posix.json), so both spellings
    # go through the same rules.
    write_committed_report(args.out, report, caller_paths=caller_paths)

    print("report: %s" % args.out)
    print("RESULT: %s" % ("PASS" if all_ok else "FAIL"))
    return 0 if all_ok else 1


if __name__ == "__main__":
    sys.exit(main())
