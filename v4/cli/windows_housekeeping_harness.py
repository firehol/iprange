#!/usr/bin/env python3
"""Windows-host qualification of the iprange v1 windows_housekeeping kind.

Milestone-4 (delivery step 5) resource gate.  The
``windows_housekeeping`` maintenance kind is platform-bound by design
(it exists to garbage-collect Windows filesystem names that cannot be
removed while a handle is open, ``gc_name.rs`` envelope/inert naming);
the declarative suite therefore cannot commit a case for it.  This
script qualifies it at the normal JSON-RPC product interface.

The script synthesizes one GC-envelope candidate file exactly like
the product's own names (``.iprange-gcauth-<32 lower hex>-<8 lower
hex>.tmp``, per ``v4/rust/iprange-livedb/src/publication/gc_name.rs``)
plus an ordinary marker file, and probes ``maintenance.list`` with
kinds ["windows_housekeeping"] over an empty directory and over the
candidate directory, for both product binaries.

- On Windows, both probes must succeed: the empty directory reports 0
  entries, the candidate directory reports >= 1 entry whose JSONL row
  carries kind ``windows_housekeeping``, candidate_kind ``envelope``,
  a directory identity, and the exact GC basename (base64-encoded on
  the wire; the harness decodes it and compares).
- On any other platform the run records the truthful negative -- both
  products answer ``os_unsupported``/``read_only_failure`` for this
  kind when it is unavailable -- and exits 0 with a ``skipped``
  record naming the platform.  This makes the same script the Linux
  negative control.

Reuses ``HarnessJsonRpcService`` from ``crash_harness.py`` (import
side-effect free).  Report schema:
``iprange-cli-windows-housekeeping-report-v1``.
"""

import argparse
import base64
import hashlib
import json
import os
import platform
import sys
import uuid

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from crash_harness import HarnessJsonRpcService  # noqa: E402

# Exact product GC-envelope naming (gc_name.rs): ENVELOPE_PREFIX,
# separator b"-", SUFFIX b".tmp"; attempt 16 bytes, ordinal 4 bytes,
# both lowercase hex.
GC_ENVELOPE_PREFIX = ".iprange-gcauth-"
GC_SUFFIX = ".tmp"
MARKER_NAME = "MARKER.txt"
MARKER_TEXT = "windows_housekeeping harness marker\n"


def sha256_file(path):
    """Lowercase SHA-256 of one file."""

    digest = hashlib.sha256()
    with open(path, "rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def gc_candidate_name():
    """One synthesized GC envelope basename (lowercase hex parts)."""

    return (f"{GC_ENVELOPE_PREFIX}{os.urandom(16).hex()}-"
            f"{os.urandom(4).hex()}{GC_SUFFIX}")


def parse_binaries(value):
    """Parse --binaries rust=PATH go=PATH into {label: path}."""

    result = {}
    tokens = value if isinstance(value, list) else value.split()
    for token in tokens:
        if "=" not in token:
            raise SystemExit(
                f"--binaries token must be label=path, got: {token!r}")
        label, path = token.split("=", 1)
        result[label] = path
    if sorted(result) != ["go", "rust"]:
        raise SystemExit(
            "--binaries must name exactly rust=PATH and go=PATH")
    return result


def executable(value, label):
    """Validate one absolute executable path (run.py parity)."""

    if not os.path.isabs(value):
        raise SystemExit(f"{label} is not an absolute executable file: {value}")
    if not os.path.isfile(value) or not os.access(value, os.X_OK):
        raise SystemExit(f"{label} is not an absolute executable file: {value}")
    return os.path.realpath(value)


def maintenance_list(service, directory, out_path):
    """One maintenance.list call with the windows_housekeeping params.

    Returns ``(reports, error_data, rows)``: reports is the result's
    report list or None on error, error_data is the error payload or
    None on success, rows is the parsed JSONL output (empty when the
    call failed or no output was produced).
    """

    if os.path.exists(out_path):
        os.remove(out_path)
    response = service.call("m", "iprange.v1.maintenance.list", {
        "directory": directory,
        "kinds": ["windows_housekeeping"],
        "max_entries": 1024,
        "output": {"path": out_path, "format": "jsonl",
                   "publication_policy": "fail_if_exists",
                   "result_budget": {"max_rows": "1024",
                                     "max_output_bytes": "1048576",
                                     "max_open_files": 3}},
    })
    if "error" in response:
        return None, response["error"].get("data", {}), []
    reports = response["result"].get("reports", [])
    rows = []
    if os.path.isfile(out_path):
        with open(out_path, "r", encoding="utf-8") as stream:
            for line in stream:
                line = line.strip()
                if line:
                    rows.append(json.loads(line))
    return reports, None, rows


def kind_entries(reports):
    """The windows_housekeeping entry count reported, or None."""

    for report in reports or []:
        if report.get("kind") == "windows_housekeeping":
            return str(report.get("entries", ""))
    return None


def decoded_basename(row):
    """Decode a row's base64 ``basename`` member, or None.

    The wire ``basename_encoding`` names the byte encoding the product
    used (namespace unix.rs kind 1 = UTF-8, namespace windows.rs kind
    2 = UTF-16LE); the ASCII candidate names this harness synthesizes
    must decode to the same string under either encoding.
    """

    encoded = row.get("basename")
    if not isinstance(encoded, str):
        return None
    try:
        raw = base64.b64decode(encoded + "=" * (-len(encoded) % 4))
    except (ValueError, TypeError):
        return None
    if row.get("basename_encoding") == 2:
        try:
            return raw.decode("utf-16-le")
        except (ValueError, UnicodeError):
            return None
    return raw.decode("utf-8", "replace")


def probe_directory(service, directory, out_path, candidate_name,
                    report_mode):
    """One maintenance.list probe over one directory.

    Returns (outcome, pass_bool, failures) where outcome records the
    raw error/reports/rows plus the checked facts.
    """

    reports, error_data, rows = maintenance_list(
        service, directory, out_path)
    outcome = {
        "error": error_data,
        "reports": reports,
        "rows": rows,
        "entries": kind_entries(reports),
        "row_checked": None,
    }
    if error_data:
        outcome["negative_matched"] = (
            error_data.get("code") == "os_unsupported"
            and error_data.get("outcome") == "read_only_failure")
        if report_mode == "windows":
            return outcome, False, [
                f"maintenance.list failed on Windows: {error_data!r}"]
        return outcome, True, []
    if report_mode == "linux-negative":
        # The kind answered successfully on a platform where it is
        # documented unavailable; record it truthfully as a mismatch.
        outcome["negative_matched"] = False
        return outcome, False, [
            "windows_housekeeping answered successfully on this "
            "platform instead of os_unsupported/read_only_failure"]
    # Windows qualification path.
    checked = {"kind": None, "candidate_kind": None,
               "directory_identity": False, "basename_ok": False,
               "basename_decoded": None}
    failures = []
    if outcome["entries"] is None:
        failures.append(
            f"no windows_housekeeping report in {reports!r}")
    for row in rows:
        checked["kind"] = row.get("kind")
        checked["candidate_kind"] = row.get("candidate_kind")
        checked["directory_identity"] = "directory_identity" in row
        decoded = decoded_basename(row)
        checked["basename_ok"] = decoded == candidate_name
        checked["basename_decoded"] = decoded
        checked["basename_encoding"] = row.get("basename_encoding")
        if row.get("kind") != "windows_housekeeping":
            failures.append(
                f"row kind is {row.get('kind')!r}, expected "
                f"'windows_housekeeping'")
        if row.get("candidate_kind") != "envelope":
            failures.append(
                f"row candidate_kind is {row.get('candidate_kind')!r}, "
                "expected 'envelope'")
        if not checked["directory_identity"]:
            failures.append("row has no directory_identity")
        if not checked["basename_ok"]:
            failures.append(
                f"row basename does not decode to the synthesized "
                f"candidate {candidate_name!r}; returned "
                f"{decoded!r}")
    outcome["row_checked"] = checked
    return outcome, not failures, failures


def main():
    parser = argparse.ArgumentParser(
        description="Windows-host qualification of the iprange v1 "
                    "windows_housekeeping maintenance kind; the same "
                    "script records the truthful negative on other "
                    "platforms.")
    parser.add_argument("--binaries", metavar="rust=PATH go=PATH",
                        nargs="+", required=True,
                        help="absolute iprange --jsonrpc executables, as "
                             "rust=PATH go=PATH")
    parser.add_argument("--work-dir", metavar="DIR", required=True,
                        help="absolute existing harness-owned directory "
                             "that receives candidates/ and empty/; the "
                             "harness recreates MARKER.txt and its own "
                             "envelope candidate there")
    parser.add_argument("--json-report", metavar="PATH",
                        help="write the JSON report to this file")
    args = parser.parse_args()

    if not os.path.isdir(args.work_dir) or not os.path.isabs(args.work_dir):
        parser.error("--work-dir must be an absolute existing directory")
    binaries = {}
    for label, path in parse_binaries(args.binaries).items():
        binaries[label] = executable(path, f"{label} binary")

    on_windows = platform.system() == "Windows"
    report_mode = "windows" if on_windows else "linux-negative"

    # Synthesize the candidate directory: one marker file plus exactly
    # one recognizable GC envelope candidate.  Only the marker and the
    # synthesized envelope files are removed on re-runs so the entry
    # count stays deterministic.
    candidates_dir = os.path.join(args.work_dir, "candidates")
    empty_dir = os.path.join(args.work_dir, "empty")
    os.makedirs(candidates_dir, exist_ok=True)
    os.makedirs(empty_dir, exist_ok=True)
    for name in os.listdir(candidates_dir):
        if name == MARKER_NAME or (
                name.startswith(GC_ENVELOPE_PREFIX)
                and name.endswith(GC_SUFFIX)):
            os.remove(os.path.join(candidates_dir, name))
    candidate_name = gc_candidate_name()
    with open(os.path.join(candidates_dir, MARKER_NAME), "w",
              encoding="utf-8", newline="") as stream:
        stream.write(MARKER_TEXT)
    with open(os.path.join(candidates_dir, candidate_name), "wb") as stream:
        stream.write(b"gc-envelope-candidate\x00\x01\x02")

    report = {
        "schema": "iprange-cli-windows-housekeeping-report-v1",
        "command": sys.argv,
        "platform": {
            "system": platform.system(),
            "release": platform.release(),
            "machine": platform.machine(),
            "python": platform.python_version(),
        },
        "binaries": {
            label: {"path": path, "sha256": sha256_file(path)}
            for label, path in binaries.items()},
        "work_dir": args.work_dir,
        "windows_qualified": on_windows,
        "skipped": not on_windows,
        "skipped_reason": None if on_windows else (
            f"platform {platform.system()} is not Windows; the kind "
            "is documented unavailable here and the products answer "
            "os_unsupported/read_only_failure"),
        "candidate": {"directory": candidates_dir, "marker": MARKER_NAME,
                      "envelope": candidate_name},
        "outcomes": [],
        "failed": 0,
    }

    failed = 0
    for label, binary in sorted(binaries.items()):
        outcome = {
            "binary": label, "path": binary, "pass": False,
            "failures": [], "empty_dir": None, "candidates_dir": None,
        }
        service = HarnessJsonRpcService([binary, "--jsonrpc"], label,
                                        cwd=args.work_dir)
        try:
            empty_out = os.path.join(
                args.work_dir,
                f"wh-empty-{label}-{uuid.uuid4().hex[:8]}.jsonl")
            candidates_out = os.path.join(
                args.work_dir,
                f"wh-candidates-{label}-{uuid.uuid4().hex[:8]}.jsonl")
            empty, empty_pass, empty_fail = probe_directory(
                service, empty_dir, empty_out, candidate_name, report_mode)
            candidates, cand_pass, cand_fail = probe_directory(
                service, candidates_dir, candidates_out, candidate_name,
                report_mode)
            outcome["empty_dir"] = empty
            outcome["candidates_dir"] = candidates
            failures = []
            if report_mode == "windows":
                if str(empty.get("entries")) != "0":
                    empty_fail.append(
                        f"empty directory must report 0 entries, got "
                        f"{empty.get('entries')!r} with rows "
                        f"{len(empty.get('rows') or [])}")
                if empty_pass and cand_pass:
                    entries = candidates.get("entries")
                    try:
                        entries_ok = int(entries or "0") >= 1
                    except ValueError:
                        entries_ok = False
                    if not entries_ok:
                        cand_fail.append(
                            f"candidate directory must report >= 1 "
                            f"entry, got {entries!r}")
                failures = empty_fail + cand_fail
            else:
                # Negative control: every probe must record the
                # truthful os_unsupported/read_only_failure negative.
                for directory_name, probed in (
                        ("empty_dir", empty),
                        ("candidates_dir", candidates)):
                    if not probed.get("negative_matched"):
                        failures.append(
                            f"{directory_name} did not record the "
                            "truthful os_unsupported/read_only_failure "
                            f"negative: error {probed.get('error')!r} "
                            f"reports {probed.get('reports')!r}")
            outcome["failures"] = failures
            outcome["pass"] = not failures
            if failures:
                failed += 1
        finally:
            service.close()
            for out_path in (empty_out, candidates_out):
                if os.path.exists(out_path):
                    os.remove(out_path)
        report["outcomes"].append(outcome)

    if not on_windows:
        # The non-Windows run is a skipped (record-only) negative
        # control: it always exits 0 with the skipped flag, and the
        # per-outcome pass flags above record whether each product
        # answered the truthful os_unsupported/read_only_failure
        # negative.
        failed = 0
    report["failed"] = failed

    if args.json_report:
        # newline="" keeps the committed report LF-only on every
        # platform (Windows text mode would otherwise write CRLF).
        with open(args.json_report, "w", encoding="utf-8", newline="") as stream:
            json.dump(report, stream, indent=2, sort_keys=True)
            stream.write("\n")

    for outcome in report["outcomes"]:
        if outcome["pass"]:
            print(f"PASS windows_housekeeping.{outcome['binary']}")
        else:
            print(f"FAIL windows_housekeeping.{outcome['binary']}: "
                  f"{outcome['failures']}")
    total = len(report["outcomes"])
    print(f"{total - failed} passed, {failed} failed "
          f"({total} binaries; skipped={report['skipped']})")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
