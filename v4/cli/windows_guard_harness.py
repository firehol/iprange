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

from crash_harness import HarnessJsonRpcService  # noqa: E402

REPORT_SCHEMA = "iprange-cli-windows-guard-report-v1"
RPC_READ_DEADLINE_SECONDS = 300.0
RPC_WRITE_DEADLINE_SECONDS = 120.0

ACCEPTED_MESSAGE = "destination must differ from the source database"
IS_WINDOWS = os.name == "nt"
METHOD = "iprange.v1.database.metadata.get"
OUTPUT_FACTS = ("bytes", "path", "rows", "sha256")


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


def validate_success_response(response, method, destination):
    """Strict success-facts validation for one metadata.get file
    delivery (wave 19 round 19.14 astra P2).  A successful response
    must carry the exact result schema and the delivered file must
    match the claimed digest/bytes on disk; a bare ``result:{}``, an
    omitted output file, or a ``present:false`` response that leaves
    the destination existing is a harness failure, never a PASS.

    Returns (ok, reason).  Error responses are rejected here; refusal
    cases are validated separately with the exact canonical shape.
    """
    if "error" in response:
        return False, "unexpected error response"
    result = response.get("result")
    if not isinstance(result, dict):
        return False, "result is not an object"
    if result.get("method") != method:
        return False, "result.method = %r, want %r" % (result.get("method"), method)
    present = result.get("present")
    if not isinstance(present, bool):
        return False, "result.present is not a boolean"
    if present:
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
        actual = hashlib.sha256(open(destination, "rb").read()).hexdigest()
        if actual != output.get("sha256"):
            return False, "output file digest %s != claimed %s" % (
                actual, output.get("sha256"))
        if str(os.path.getsize(destination)) != str(output.get("bytes")):
            return False, "output file size %d != claimed %r" % (
                os.path.getsize(destination), output.get("bytes"))
        return True, ""
    if os.path.exists(destination):
        return False, "present:false but destination %r exists" % destination
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


def selftest():
    """Pure-function regression battery for the strict validators
    (wave 19 round 19.14 astra P2): every counterexample that the old
    weak evaluator accepted must now be rejected, and a genuine
    successful delivery must still pass."""
    import tempfile
    ok = True

    def expect(label, cond):
        nonlocal ok
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
        claimed = hashlib.sha256(b"mymetadata").hexdigest()
        response = {"result": {"method": METHOD, "present": True, "output": {
            "bytes": str(len(b"mymetadata")), "rows": "1", "sha256": claimed,
            "path": dest}}}
        # astra's counterexample: omit meta.txt entirely.
        expect("omitted file rejected",
               not validate_success_response(response, METHOD, dest)[0])
        # astra's counterexample: empty pre-existing reopen file.
        with open(dest, "wb") as fh:
            fh.write(b"")
        expect("empty file rejected",
               not validate_success_response(response, METHOD, dest)[0])
        # The genuine bytes pass.
        with open(dest, "wb") as fh:
            fh.write(b"mymetadata")
        expect("genuine delivery accepted",
               validate_success_response(response, METHOD, dest)[0])
        # present:false with a leftover destination (a missed guard).
        expect("present:false with existing dest rejected",
               not validate_success_response(
                   {"result": {"method": METHOD, "present": False}},
                   METHOD, dest)[0])

    # guard_cases must build on both platform shapes without
    # crashing and with the platform-appropriate case mix (wave 19
    # round 19.14 regression: conditional None entries once made the
    # POSIX negative control crash with an unpacking TypeError).
    posix_cases = guard_cases("/tmp/guard-work")
    win_cases = guard_cases("C:\\guard-work")
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
    return ok


def guard_cases(work):
    """Windows-targeted spellings of the absent sidecar plus the
    distinct-destination controls.  Expectation depends on the host
    platform (Windows refuses sidecar spellings, both platforms allow
    the controls).  Each case names the source database basename it
    targets ("db.iprange" for the primary source, "db_\\u00e4.iprange"
    for the non-ASCII fold pair, "A\\u03a31.iprange" for the NTFS
    sigma family)."""
    drive = work[:2] if len(work) >= 2 and work[1] == ":" else None
    # Win32 strips trailing dots and spaces at create, so these
    # spellings denote the absent sidecar db.iprange.readers.
    dot_space = [
        ("absolute_trailing_dot",
         os.path.join(work, "db.iprange") + ".readers."),
        ("absolute_trailing_space",
         os.path.join(work, "db.iprange") + ".readers "),
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
        ("absolute_upper", os.path.join(work, "DB.IPRANGE.READERS")),
    ]
    if drive:
        # rooted-without-volume spelling exists only on Windows; on
        # POSIX the same string is a plain relative file name.
        candidates.append(
            ("rooted_sidecar",
             os.path.sep + work[len(drive):].lstrip("\\/")
             + os.path.sep + "db.iprange.readers"))
    candidates += dot_space + [
        ("non_ascii", os.path.join(work, "DB_\u00c4.IPRANGE.READERS")),
        # NTFS upcase-name equivalence of the Greek sigma class (wave
        # 19 round 19.14 astra P1): "\u03c3" spells the absent sidecar
        # of source "A\u03a31.iprange" (the contextual lowercase fold
        # splits U+03A3 into U+03C2/U+03C3 and would let the spelling
        # escape); the final-sigma U+03C2 spelling is refused
        # conservatively on volumes that separate it, matching the
        # fold.
        ("ntfs_sigma", os.path.join(work, "A\u03c31.iprange.readers")),
        ("ntfs_final_sigma", os.path.join(work, "A\u03c21.iprange.readers")),
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
        # Distinct drive-root control with the SAME basename as the
        # absent sidecar (wave 19 round 19.14 astra P1): with the
        # source in the per-drive working directory,
        # "\db.iprange.readers" names the distinct C:\db.iprange.readers
        # and must be published, not refused as the source's sidecar.
        candidates.append(("drive_root_allowed", "\\db.iprange.readers"))
    candidates.append(("control_allowed", os.path.join(work, "meta.txt")))
    source_base = {
        "non_ascii": "db_\u00e4.iprange",
        "ntfs_sigma": "A\u03a31.iprange",
        "ntfs_final_sigma": "A\u03a31.iprange",
    }
    return [
        (name, source_base.get(name, "db.iprange"), path)
        for name, path in candidates
    ]


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
            path = os.path.join(work, base)
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
            source = os.path.join(work, source_base)
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
                expected = name not in ("control_allowed", "drive_root_allowed")
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
        reopen = os.path.join(work, "meta-reopen.txt")
        if os.path.exists(reopen):
            os.remove(reopen)
        primary = os.path.join(work, "db.iprange")
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


def main():
    if "--selftest" in sys.argv:
        return 0 if selftest() else 1
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--rust", required=True, help="Rust iprange binary")
    parser.add_argument("--go", required=True, help="Go iprange binary")
    parser.add_argument("--fixture", required=True,
                        help="immutable v4 database copied into the work dir")
    parser.add_argument("--work", required=True,
                        help="fresh working directory (one per product)")
    parser.add_argument("--out", required=True,
                        help="evidence JSON path")
    parser.add_argument("--provenance", default=None,
                        help="build provenance JSON recorded verbatim")
    args = parser.parse_args()

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
    with open(args.out, "w") as fh:
        json.dump(report, fh, indent=2, sort_keys=True)

    print("report: %s" % args.out)
    print("RESULT: %s" % ("PASS" if all_ok else "FAIL"))
    return 0 if all_ok else 1


if __name__ == "__main__":
    sys.exit(main())
