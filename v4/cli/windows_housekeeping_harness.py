#!/usr/bin/env python3
"""Windows-host qualification of the iprange v1 windows_housekeeping kind.

Milestone-4 (delivery step 5) resource gate.  The
``windows_housekeeping`` maintenance kind is platform-bound by design
(it exists to garbage-collect Windows filesystem names that cannot be
removed while a handle is open, ``gc_name.rs`` envelope/inert naming);
the declarative suite therefore cannot commit a case for it.  This
script qualifies it at the normal JSON-RPC product interface, on the
authorized Windows validation host.

The harness proves the native retirement path twice, at two different
boundaries:

1. Native refresh exercise: it opens a live reader on a populated live
   database (pinning the current main file), runs
   ``iprange.v1.retention.first_seen.refresh`` with a
   ``removals_output``, and proves the refresh completes, publishes
   the exact removal log, and leaves no private ``.removals.tmp``
   residue.  This is the native coverage of the Go removalCollector
   Windows cleanup fix.

2. Deterministic GC pair proof: product-created Windows GC envelopes
   are timing-dependent (the retirement machinery deletes the pair
   when the operation finishes), so the harness cannot rely on a
   leftover envelope appearing at the product boundary.  Instead it
   crafts one format-valid 8192-byte authenticated GC envelope plus
   its inert payload twin (``gc_envelope_windows.py`` mirrors the
   committed codec in ``gc_codec.go``/``gc_codec.rs``: magic, record
   size, version, kinds, identity payloads, name commitments, the
   creator-only security commitment, and the per-block CRC-32C), with
   the exact creator-only protected DACL the product installs
   (``security_windows.go buildDescriptor``).  It then proves
   ``maintenance.list`` kind ``windows_housekeeping`` lists the pair
   with a valid authenticated directory identity and
   ``maintenance.remove`` removes it with the listed row passed
   unchanged, with durable absence afterwards.  This is a product
   artifact, not a test hook: both products validate it through their
   ordinary GC codec and creator-only security checks.

On any non-Windows platform the run records the truthful negative --
both products answer ``os_unsupported``/``read_only_failure`` for
this kind -- and exits 0 with a ``skipped`` record naming the
platform.  This makes the same script the Linux negative control
(the negative is recorded over the same refresh-built directory, so
the refusal is proven on a real artifact directory).

Per-binary evidence records auditable identities instead of trusting
the caller's ``rust=``/``go=`` labels: the binary absolute path, its
SHA-256, and one ``system.describe`` call whose ``implementation``
member must claim the expected language.

On any non-Windows platform the run records the truthful negative --
both products answer ``os_unsupported``/``read_only_failure`` for
this kind -- and exits 0 with a ``skipped`` record naming the
platform.  This makes the same script the Linux negative control
(the negative is recorded over the same refresh-built directory, so
the refusal is proven on a real artifact directory).

Reuses ``HarnessJsonRpcService`` from ``crash_harness.py`` (import
side-effect free).  Report schema:
``iprange-cli-windows-housekeeping-report-v2``.

Sixth-wave (SOW-0028) additions to this script:

- Two deterministic abort/failure exercises prove the removal-output
  collector's terminal cleanup paths over the normal JSON-RPC product
  interface: a refresh abort (``result_budget.max_rows: "1"`` makes
  the collector refuse row 2, the workflow answers
  ``output_limit``/``not_started``, and the private ``.removals.tmp``
  must be discarded with no removal destination created) and a
  publish failure (the removal destination pre-exists under
  ``publication_policy: fail_if_exists``, the committed refresh
  answers an error recording ``removals_publication_failure``, and
  the private temporary must be discarded without touching the
  destination).  No wall-clock assertions are used anywhere.
- ``--provenance PATH`` accepts a JSON file (``{"revision",
  "tree_clean", "build_commands", "toolchain": {"go", "rustc",
  "date"}}``) recorded verbatim as ``report.build_provenance``;
  every binary record additionally carries mtime and size next to
  its SHA-256.
- The cross-language listing check validates every cross-listed row:
  each row's ``directory_identity`` must equal the local
  ``windows_directory_identity``, the cross entries count must equal
  the local listing's entries, and both products' listings are
  validated by ``check_synthesized_pair_rows``.
"""

import argparse
import base64
import datetime
import hashlib
import json
import os
import platform
import shutil
import sys
import uuid

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from crash_harness import (  # noqa: E402  (side-effect free)
    HarnessJsonRpcService,
    write_direct_csv_feed,
)

# Exact product GC-envelope naming (gc_name.rs): ENVELOPE_PREFIX,
# separator b"-", SUFFIX b".tmp"; attempt 16 bytes, ordinal 4 bytes,
# both lowercase hex.
GC_ENVELOPE_PREFIX = ".iprange-gcauth-"
GC_SUFFIX = ".tmp"
MARKER_NAME = "MARKER.txt"
MARKER_TEXT = "windows_housekeeping harness marker\n"

# Live refresh flow sizes: the target live database gets
# TARGET_CSV_ROWS direct records; the coverage source (an immutable
# feed database named "alpha") covers the first COVERAGE_TEXT_ROWS of
# those ranges, so the refresh removes the remaining addresses and
# the removals collector publishes at least one row.  REFRESH_VALUE
# is the first-seen timestamp the refresh stamps.
TARGET_CSV_ROWS = 200
COVERAGE_TEXT_ROWS = 150
REFRESH_VALUE = 123456

WRITER_BUDGET = {"max_heap_bytes": "16777216", "max_private_pages": "20000",
                 "max_growth_pages": "20000", "max_open_files": 4}


def sha256_file(path):
    """Lowercase SHA-256 of one file."""

    digest = hashlib.sha256()
    with open(path, "rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def file_evidence(path):
    """Auditable identity of one binary file.

    The absolute path, SHA-256, UTC ISO-8601 mtime, and byte size are
    recorded together so every hash in the report sits next to the
    exact file observed (SOW-0028 build provenance).
    """

    stat = os.stat(path)
    return {
        "path": path,
        "sha256": sha256_file(path),
        "mtime": datetime.datetime.fromtimestamp(
            stat.st_mtime, datetime.timezone.utc).isoformat(),
        "size": stat.st_size,
    }


def removals_tmp_residue(directory):
    """Private ``.removals.tmp`` basenames present in one directory."""

    if not os.path.isdir(directory):
        return []
    return sorted(name for name in os.listdir(directory)
                  if name.endswith(".removals.tmp"))


def load_provenance(path):
    """Load and validate one ``--provenance`` JSON file.

    Required shape: ``{"revision": str, "tree_clean": bool,
    "build_commands": [str], "toolchain": {"go": str, "rustc": str,
    "date": str}}``.  The object is recorded verbatim in the report
    as ``build_provenance``; extra members are preserved.  A missing
    or invalid file is a caller error.
    """

    try:
        with open(path, "r", encoding="utf-8") as stream:
            value = json.load(stream)
    except (OSError, ValueError) as exc:
        raise SystemExit(f"--provenance {path} is not readable JSON: {exc}")
    if not isinstance(value, dict):
        raise SystemExit("--provenance must name a JSON object")
    problems = []
    if not isinstance(value.get("revision"), str) or not value.get("revision"):
        problems.append("revision must be a non-empty string")
    if not isinstance(value.get("tree_clean"), bool):
        problems.append("tree_clean must be a boolean")
    commands = value.get("build_commands")
    if not isinstance(commands, list) or not all(
            isinstance(item, str) for item in commands):
        problems.append("build_commands must be a list of strings")
    toolchain = value.get("toolchain")
    if not isinstance(toolchain, dict):
        problems.append("toolchain must be an object")
    else:
        for member in ("go", "rustc", "date"):
            if not isinstance(toolchain.get(member), str) or \
                    not toolchain.get(member):
                problems.append(
                    f"toolchain.{member} must be a non-empty string")
    if problems:
        raise SystemExit("--provenance is invalid: " + "; ".join(problems))
    return value


def envelope_basenames(directory):
    """Sorted GC envelope basenames present in one directory.

    Only exact product-shaped envelope names count
    (``.iprange-gcauth-<attempt>-<ordinal>.tmp``); every other file
    (live database, sidecar, removals output, the harness marker) is
    deliberately ignored, which is what makes the
    entries == len(rows) check a selectivity proof.
    """

    if not os.path.isdir(directory):
        return []
    return sorted(
        name for name in os.listdir(directory)
        if name.startswith(GC_ENVELOPE_PREFIX) and name.endswith(GC_SUFFIX))


def write_overlap_text_feed(path, count):
    """One immutable text feed covering the first ``count`` direct rows.

    The ranges replicate ``write_direct_csv_feed`` rows 0..count so
    the coverage source overlaps the target live database exactly:
    refresh keeps the first ``count`` records and removes the rest.
    """

    import ipaddress

    with open(path, "w", encoding="utf-8", newline="") as stream:
        for index in range(count):
            low = 0x0A000000 + index * 64
            high = low + 63
            stream.write(f"{ipaddress.IPv4Address(low)}-"
                         f"{ipaddress.IPv4Address(high)}\n")


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
    """Validate one absolute executable path (run.py parity).

    The returned path is the caller-supplied absolute path, not
    ``os.path.realpath``: MSYS2/Cygwin Python normalizes a native
    Windows path such as ``C:\\Temp\\x.exe`` to the broken
    ``/:\\Temp\\x.exe``, which then fails every later open.  The
    path is already absolute and file-verified, so no normalization is
    needed.
    """

    if not os.path.isabs(value):
        raise SystemExit(f"{label} is not an absolute executable file: {value}")
    if not os.path.isfile(value) or not os.access(value, os.X_OK):
        raise SystemExit(f"{label} is not an absolute executable file: {value}")
    return value


def describe_identity(service):
    """One system.describe call; returns (implementation, result)."""

    response = service.call("describe", "iprange.v1.system.describe", {})
    if "error" in response:
        raise AssertionError(
            f"system.describe failed: {json.dumps(response['error'])[:300]}")
    result = response.get("result") or {}
    return result.get("implementation"), result


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
    2 = UTF-16LE); on Windows the kind only lists UTF-16LE names, and
    the decoded name must equal one real envelope file in the scanned
    directory.
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


def refresh_flow_steps(live_dir, removals_output=None):
    """One native first-seen refresh step list.

    Shared by the success exercise and the deterministic abort
    exercises.  Creates ``live_dir``, writes the marker, the target
    CSV feed, and the coverage source feed, and returns the five
    steps: ``database.create``, ``direct.replace``, ``reader.open``
    (pinning the current main), ``current.publish`` (feed "alpha"),
    and ``retention.first_seen.refresh`` with a ``removals_output``.
    ``removals_output`` overrides the default removals_output member
    of the refresh step (None selects the success-flow default).
    """

    os.makedirs(live_dir, exist_ok=True)
    with open(os.path.join(live_dir, MARKER_NAME), "w",
              encoding="utf-8", newline="") as stream:
        stream.write(MARKER_TEXT)

    db_path = os.path.join(live_dir, "live.iprange")
    cov_path = os.path.join(live_dir, "coverage.iprange")
    csv_path = os.path.join(live_dir, "gen_a.csv")
    cov_feed = os.path.join(live_dir, "coverage.txt")
    write_direct_csv_feed(csv_path, TARGET_CSV_ROWS)
    write_overlap_text_feed(cov_feed, COVERAGE_TEXT_ROWS)

    if removals_output is None:
        removals_output = {
            "path": os.path.join(live_dir, "removals.jsonl"),
            "publication_policy": "fail_if_exists",
            "result_budget": {"max_rows": "4096",
                              "max_output_bytes": "1048576",
                              "max_open_files": 3}}

    return [
        ("create", "iprange.v1.database.create", {
            "path": db_path, "family": "ipv4", "value_kind": "direct",
            "structure_kind": "none", "value_tag": {"text": "first_seen"},
            "reader_capacity": 8}),
        ("replace", "iprange.v1.direct.replace", {
            "path": db_path,
            "input": {"path": csv_path, "max_line_bytes": 1024},
            "metadata": {"mode": "replace_utf8", "text": "first-seen-data"},
            "writer_budget": WRITER_BUDGET}),
        ("reader.open", "iprange.v1.reader.open", {
            "source": {"path": db_path, "mode": "live"}}),
        ("publish", "iprange.v1.current.publish", {
            "input": {"paths": [cov_feed], "family": "ipv4",
                      "fix_network": True, "default_prefix": 32,
                      "dns": {"threads": 1, "silent": True},
                      "expand_at_paths": False, "max_line_bytes": 1024,
                      "max_expanded_paths": 4},
            "feed": "alpha", "value_tag": {"text": "coverage"},
            "metadata": {"mode": "replace_utf8", "text": "coverage"},
            "destination": cov_path, "publication_policy": "fail_if_exists",
            "immutable_feed_budget": {"max_heap_bytes": "16777216",
                                      "max_output_pages": "20000",
                                      "max_workspace_pages": "20000",
                                      "max_open_files": 3}}),
        ("refresh", "iprange.v1.retention.first_seen.refresh", {
            "path": db_path,
            "current": {"source": {"path": cov_path, "mode": "immutable"},
                        "feed": "alpha"},
            "refresh_value": REFRESH_VALUE,
            "removals_output": removals_output,
            "metadata": {"mode": "keep"},
            "writer_budget": WRITER_BUDGET}),
    ]


def run_refresh_envelope_flow(service, live_dir, label):
    """Drive each product through the native envelope-creating flow.

    Steps (every request and response is recorded verbatim for the
    evidence trail):

    1. ``database.create`` -- the target live database;
    2. ``direct.replace`` -- populate it with TARGET_CSV_ROWS records;
    3. ``reader.open`` (mode live) -- pins the current main file so
       the next commit's Windows retirement cannot rename it away;
    4. ``current.publish`` -- the immutable coverage source (feed
       "alpha") covering COVERAGE_TEXT_ROWS ranges;
    5. ``retention.first_seen.refresh`` with a ``removals_output`` --
       commits a new live generation; on Windows the retirement of
       the pinned previous main goes through the GC machinery and
       leaves a real 8192-byte envelope beside the database.

    The reader is intentionally left open; the caller closes it only
    after listing.  Returns a dict with ``steps`` (request id,
    method, params, response), ``reader`` (the open reader handle or
    None), ``step_error`` (the method whose RPC answer was an error,
    or None), ``envelopes`` (basenames found), and ``files`` (the
    directory listing).
    """

    flow = {"steps": [], "reader": None, "step_error": None,
            "envelopes": [], "files": []}
    for rid, method, params in refresh_flow_steps(live_dir):
        response = service.call(rid, method, params)
        flow["steps"].append({"request_id": rid, "method": method,
                              "params": params, "response": response})
        if "error" in response:
            flow["step_error"] = method
            break
        if method == "iprange.v1.reader.open":
            flow["reader"] = response["result"]["reader"]

    flow["envelopes"] = envelope_basenames(live_dir)
    flow["files"] = sorted(os.listdir(live_dir))
    return flow


def synthesize_gc_pair(directory):
    """One format-valid Windows GC pair for the deterministic proof.

    ``gc_envelope_windows`` mirrors the committed envelope codec
    (binary-format-v4.md 14.4.1, ``gc_codec.go``/``gc_codec.rs``) and
    the Windows creator-only security machine
    (``security_windows.go buildDescriptor``).  The pair is:

    - ``.iprange-gcauth-<attempt-hex>-<ordinal:08x>.tmp``: the
      8,192-byte authenticated authority envelope, two identical
      sequence-1 blocks, artifact kind 1 (private output) which fixes
      the source component to the attempt-derived publish name
      ``.iprange-publish-<attempt-hex>.tmp`` (``gc_source.go
      gcNameMatches``), committed with the creator-only security
      commitment the products prove against the live envelope DACL;
    - ``.iprange-gc-<attempt-hex>-<ordinal:08x>.tmp``: the inert
      payload twin, created with the same protected creator-only DACL;
      its local identity (volume serial + low file reference) is the
      envelope-committed artifact identity, which is exactly the
      state the products classify as ``Inert`` (source absent, inert
      exact) -- a clean, conflict-free housekeeping row.

    Returns the synthesis facts: names, attempt, ordinal, identities
    (encoded wire payloads), the envelope SHA-256, and the marker
    used for the inert payload content.
    """

    import struct

    import gc_envelope_windows as gce

    os.makedirs(directory, exist_ok=True)
    attempt = os.urandom(16)
    if attempt == b"\x00" * 16:
        attempt = attempt[:-1] + b"\x01"
    ordinal = 1
    source_name = ".iprange-publish-" + attempt.hex() + ".tmp"
    envelope_name = gce.gc_name(gce.GC_ENVELOPE_PREFIX, attempt, ordinal)
    inert_name = gce.gc_name(gce.GC_INERT_PREFIX, attempt, ordinal)
    inert_path = os.path.join(directory, inert_name)
    envelope_path = os.path.join(directory, envelope_name)

    # The inert payload is the retired artifact: one regular file with
    # the product's creator-only protected DACL.
    gce.create_protected_file(inert_path)
    with open(inert_path, "wb") as stream:
        stream.write(MARKER_TEXT.encode("utf-8"))
    artifact_identity = gce.file_identity(inert_path)

    # The envelope file itself carries the same protected creator-only
    # DACL; the embedded security commitment is derived from the
    # envelope's own live descriptor, exactly like the products prove
    # it (gcVerifyRecord).
    gce.create_protected_file(envelope_path)
    commitment = gce.creator_only_commitment_of(envelope_path)

    dir_volume, dir_inode = gce.file_identity(directory)
    art_volume, art_inode = artifact_identity
    dir_payload = (struct.pack("<Q", dir_volume) + struct.pack("<Q", dir_inode)
                   + b"\x00" * 16)
    art_payload = (struct.pack("<Q", art_volume) + struct.pack("<Q", art_inode)
                   + b"\x00" * 16)
    data = gce.envelope_bytes(attempt, ordinal, source_name, dir_payload,
                              art_payload, commitment)
    with open(envelope_path, "wb") as stream:
        stream.write(data)

    return {
        "attempt": attempt.hex(),
        "ordinal": ordinal,
        "source_name": source_name,
        "envelope_name": envelope_name,
        "inert_name": inert_name,
        "directory_identity": {
            "kind": 2,
            "volume": dir_volume,
            "inode": dir_inode,
            "payload": dir_payload.hex(),
        },
        "artifact_identity": {
            "kind": 2,
            "volume": art_volume,
            "inode": art_inode,
            "payload": art_payload.hex(),
        },
        "envelope_sha256": sha256_file(envelope_path),
        "commitment_matches": (
            gce.creator_only_commitment_of(envelope_path) == commitment),
    }


def complete_native_refresh_exercise(service, live_dir, flow):
    """Native Windows coverage of the Go removalCollector fix.

    ``flow`` is the already-recorded refresh step evidence
    (database.create, direct.replace, reader.open pinning the current
    main, current.publish, and ``retention.first_seen.refresh`` with a
    removals_output, produced by ``run_refresh_envelope_flow``).  This
    function proves:

    - every product RPC step succeeded;
    - the removals_output was published with at least one removal row
      whose removed_at member equals the refresh value;
    - the private ``.removals.tmp`` temporary is gone after the call
      (the collector's explicit cleanup path);
    - the pinning reader closes cleanly.

    Returns ``(refresh_facts, failures)``.
    """

    failures = []
    refresh_facts = {"step_error": flow.get("step_error"),
                     "envelopes_after_flow": flow["envelopes"],
                     "removals_output_rows": None,
                     "first_seen_matches": None,
                     "tmp_residue": None}
    if flow.get("step_error"):
        failures.append(
            f"native refresh stopped at {flow['step_error']}; params "
            "and responses are recorded in outcome['flow']['steps']")
    else:
        removals_path = os.path.join(live_dir, "removals.jsonl")
        rows = []
        if os.path.isfile(removals_path):
            with open(removals_path, "r", encoding="utf-8") as stream:
                for line in stream:
                    line = line.strip()
                    if line:
                        rows.append(json.loads(line))
        refresh_facts["removals_output_rows"] = len(rows)
        refresh_facts["first_seen_matches"] = all(
            row.get("removed_at") == REFRESH_VALUE for row in rows)
        if not rows:
            failures.append("first_seen.refresh published no removal rows")
        elif not refresh_facts["first_seen_matches"]:
            failures.append(
                "first_seen.refresh removal rows do not carry the "
                f"refresh value {REFRESH_VALUE}")
        residue = removals_tmp_residue(live_dir)
        refresh_facts["tmp_residue"] = residue
        if residue:
            failures.append(
                f"removalCollector left a private temporary behind: "
                f"{residue}")
    if flow.get("reader") is not None:
        close_response = service.call(
            "close", "iprange.v1.reader.close",
            {"reader": flow["reader"]})
        refresh_facts["reader_close"] = close_response
        if "error" in close_response:
            failures.append(
                f"reader.close failed after native refresh: "
                f"{json.dumps(close_response['error'])[:300]}")
    refresh_facts["envelopes_after_close"] = envelope_basenames(live_dir)
    refresh_facts["files"] = sorted(os.listdir(live_dir))
    return refresh_facts, failures


def run_refresh_abort_exercises(service, work_dir, label):
    """Deterministic abort/failure cleanup proofs for the removals
    collector (sixth-wave P2).

    ``complete_native_refresh_exercise`` proves the successful
    publication terminal path only.  These two independent
    fresh-directory flows prove the two remaining terminal paths over
    the normal JSON-RPC product interface; both are deterministic
    (pure data and policy, no wall-clock observation):

    1. ``refresh_abort`` -- the removals_output budget is
       ``max_rows: "1"`` while the refresh must remove
       ``TARGET_CSV_ROWS - COVERAGE_TEXT_ROWS`` records: the
       collector refuses row 2 inside the finish step, the workflow
       aborts with ``output_limit``/``not_started``, and the
       collector must discard the private ``.removals.tmp`` it
       already created.  Asserted: the refresh step answers exactly
       that error, no ``*.removals.tmp`` survives, no removal
       destination was created, and the pinning reader closes
       cleanly.

    2. ``publish_failure`` -- the removal destination pre-exists with
       marker content under ``publication_policy: fail_if_exists``:
       the refresh commits and publication fails on the existing
       destination (hard-link refusal), and the collector must
       discard the private temporary without touching the
       destination.  Asserted: the refresh step answers an error with
       outcome ``committed`` whose details record
       ``removals_publication_failure``, no ``*.removals.tmp``
       survives, and the destination still holds exactly the
       pre-existing marker content (not replaced, not removed).

    Returns ``(facts, failures)``.
    """

    facts = {}
    failures = []
    exercises = (
        ("refresh_abort", "abort-budget", "1", False),
        ("publish_failure", "abort-publish", "4096", True),
    )
    for name, directory_label, max_rows, precreate in exercises:
        directory = os.path.join(work_dir, f"{directory_label}-{label}")
        shutil.rmtree(directory, ignore_errors=True)
        destination = os.path.join(directory, "removals.jsonl")
        if precreate:
            os.makedirs(directory, exist_ok=True)
            with open(destination, "w", encoding="utf-8",
                      newline="") as stream:
                stream.write(MARKER_TEXT)
        removals_output = {
            "path": destination,
            "publication_policy": "fail_if_exists",
            "result_budget": {"max_rows": max_rows,
                              "max_output_bytes": "1048576",
                              "max_open_files": 3}}
        flow = {"steps": [], "reader": None, "step_error": None,
                "envelopes": [], "files": []}
        for rid, method, params in refresh_flow_steps(
                directory, removals_output):
            response = service.call(rid, method, params)
            flow["steps"].append({"request_id": rid, "method": method,
                                  "params": params,
                                  "response": response})
            if "error" in response:
                flow["step_error"] = method
                break
            if method == "iprange.v1.reader.open":
                flow["reader"] = response["result"]["reader"]
        flow["envelopes"] = envelope_basenames(directory)
        flow["files"] = sorted(os.listdir(directory))
        exercise = {"directory": directory, "flow": flow}

        refresh = "iprange.v1.retention.first_seen.refresh"
        if flow["step_error"] is None:
            failures.append(
                f"{name}: expected the refresh step to answer an "
                "error, but the whole flow succeeded")
        elif flow["step_error"] != refresh:
            failures.append(
                f"{name}: the flow stopped at {flow['step_error']} "
                "before the refresh step; params and responses are "
                "recorded in the flow steps")
        else:
            response = flow["steps"][-1]["response"]
            error_data = (response.get("error") or {}).get("data") or {}
            exercise["error"] = {
                "code": error_data.get("code"),
                "outcome": error_data.get("outcome"),
                "message": error_data.get("message"),
                "details": error_data.get("details"),
            }
            if name == "refresh_abort":
                if error_data.get("code") != "output_limit" or \
                        error_data.get("outcome") != "not_started":
                    failures.append(
                        f"{name}: expected an output_limit/not_started "
                        f"abort, got code {error_data.get('code')!r} "
                        f"outcome {error_data.get('outcome')!r}")
            else:
                details = error_data.get("details") or {}
                if error_data.get("outcome") != "committed":
                    failures.append(
                        f"{name}: the publish failure must report the "
                        f"commit outcome 'committed', got "
                        f"{error_data.get('outcome')!r}")
                if "removals_publication_failure" not in details:
                    failures.append(
                        f"{name}: the publish failure must record "
                        "removals_publication_failure in the error "
                        "details")
        residue = removals_tmp_residue(directory)
        exercise["tmp_residue"] = residue
        if residue:
            failures.append(
                f"{name}: the removal collector left a private "
                f"temporary behind: {residue}")
        if name == "refresh_abort":
            destination_exists = os.path.exists(destination)
            exercise["destination_exists"] = destination_exists
            if destination_exists:
                failures.append(
                    f"{name}: a removal destination was created "
                    f"despite the abort: {destination}")
        else:
            destination_content = None
            if os.path.isfile(destination):
                with open(destination, "r", encoding="utf-8") as stream:
                    destination_content = stream.read()
            exercise["destination_content"] = destination_content
            if destination_content != MARKER_TEXT:
                failures.append(
                    f"{name}: the pre-existing removal destination "
                    "must survive untouched (exact marker content "
                    "expected)")
        if flow.get("reader") is not None:
            close_response = service.call(
                "close", "iprange.v1.reader.close",
                {"reader": flow["reader"]})
            exercise["reader_close"] = close_response
            if "error" in close_response:
                failures.append(
                    f"{name}: reader.close failed after the aborted "
                    f"refresh: {json.dumps(close_response['error'])[:300]}")
        exercise["files"] = sorted(os.listdir(directory))
        facts[name] = exercise
    return facts, failures


def check_housekeeping_rows(rows, directory):
    """Validate listed windows_housekeeping rows; returns failures.

    Every row must carry the kind, ``candidate_kind: envelope``,
    UTF-16LE basename encoding (2), an authenticated directory
    identity, and a basename that decodes to a real envelope file in
    the scanned directory.
    """

    failures = []
    real_envelopes = envelope_basenames(directory)
    for row in rows:
        if row.get("kind") != "windows_housekeeping":
            failures.append(
                f"row kind is {row.get('kind')!r}, expected "
                f"'windows_housekeeping'")
        if row.get("candidate_kind") != "envelope":
            failures.append(
                f"row candidate_kind is {row.get('candidate_kind')!r}, "
                "expected 'envelope'")
        if row.get("basename_encoding") != 2:
            failures.append(
                f"row basename_encoding is {row.get('basename_encoding')!r}, "
                "expected 2 (UTF-16LE) on Windows")
        if not row.get("directory_identity"):
            failures.append("row has no directory_identity")
        decoded = decoded_basename(row)
        if decoded not in real_envelopes:
            failures.append(
                f"row basename does not decode to a real envelope file: "
                f"{decoded!r}; directory has {real_envelopes}")
    return failures


def check_synthesized_pair_rows(rows, directory):
    """Validate the two rows of one synthesized GC pair.

    The scanner lists every GC candidate name, so the pair directory
    yields exactly two rows: the authenticated envelope candidate and
    the inert payload candidate (gc_source.go gcCandidateOf; both
    products list them).  Returns failures; every row must be free of
    the problem member, carry UTF-16LE basename encoding (2), and the
    envelope row must decode to the real envelope file in the
    directory.
    """

    failures = []
    envelope_rows = [r for r in rows
                     if r.get("candidate_kind") == "envelope"]
    inert_rows = [r for r in rows
                  if r.get("candidate_kind") == "inert_payload"]
    if len(envelope_rows) != 1:
        failures.append(
            f"expected exactly one envelope row, got {len(envelope_rows)}")
    if len(inert_rows) != 1:
        failures.append(
            f"expected exactly one inert_payload row, got {len(inert_rows)}")
    real_envelopes = envelope_basenames(directory)
    for row in rows:
        if row.get("kind") != "windows_housekeeping":
            failures.append(
                f"row kind is {row.get('kind')!r}, expected "
                "'windows_housekeeping'")
        if row.get("basename_encoding") != 2:
            failures.append(
                f"row basename_encoding is "
                f"{row.get('basename_encoding')!r}, expected 2 "
                "(UTF-16LE) on Windows")
        if not row.get("directory_identity"):
            failures.append("row has no directory_identity")
        if "problem" in row:
            failures.append(
                f"row carries a problem member: {row['problem']!r}")
        decoded = decoded_basename(row)
        if row.get("candidate_kind") == "envelope":
            if decoded not in real_envelopes:
                failures.append(
                    f"envelope row basename does not decode to a real "
                    f"envelope file: {decoded!r}; directory has "
                    f"{real_envelopes}")
    return failures


def main():
    parser = argparse.ArgumentParser(
        description="Windows-host qualification of the iprange v1 "
                    "windows_housekeeping maintenance kind: a native "
                    "retention.first_seen.refresh exercise, two "
                    "deterministic abort/failure cleanup proofs for "
                    "the removal-output collector, and a "
                    "deterministic format-valid GC pair proof; the "
                    "same script records the truthful negative on "
                    "other platforms.  --provenance attaches the "
                    "exact source revision/toolchain to the report.")
    parser.add_argument("--binaries", metavar="rust=PATH go=PATH",
                        nargs="+", required=True,
                        help="absolute iprange --jsonrpc executables, as "
                             "rust=PATH go=PATH")
    parser.add_argument("--work-dir", metavar="DIR", required=True,
                        help="absolute existing harness-owned directory "
                             "that receives empty/ and live-<label>/")
    parser.add_argument("--json-report", metavar="PATH",
                        help="write the JSON report to this file")
    parser.add_argument("--provenance", metavar="PATH",
                        help="JSON file recording the exact source "
                             "revision, clean-tree status, build "
                             "commands, and toolchain that produced "
                             "the --binaries (schema "
                             '{"revision": str, "tree_clean": bool, '
                             '"build_commands": [str], "toolchain": '
                             '{"go": str, "rustc": str, "date": str}}); '
                             "recorded verbatim in the report as "
                             "build_provenance")
    args = parser.parse_args()

    if not os.path.isdir(args.work_dir) or not os.path.isabs(args.work_dir):
        parser.error("--work-dir must be an absolute existing directory")
    binaries = {}
    for label, path in parse_binaries(args.binaries).items():
        binaries[label] = executable(path, f"{label} binary")

    on_windows = platform.system() == "Windows"
    report_mode = "windows" if on_windows else "linux-negative"

    empty_dir = os.path.join(args.work_dir, "empty")
    os.makedirs(empty_dir, exist_ok=True)

    report = {
        "schema": "iprange-cli-windows-housekeeping-report-v2",
        "command": sys.argv,
        "platform": {
            "system": platform.system(),
            "release": platform.release(),
            "machine": platform.machine(),
            "python": platform.python_version(),
        },
        "binaries": {
            label: file_evidence(path)
            for label, path in binaries.items()},
        "work_dir": args.work_dir,
        "windows_qualified": on_windows,
        "skipped": not on_windows,
        "skipped_reason": None if on_windows else (
            f"platform {platform.system()} is not Windows; the kind "
            "is documented unavailable here and the products answer "
            "os_unsupported/read_only_failure"),
        "refresh_flow": {
            "target_csv_rows": TARGET_CSV_ROWS,
            "coverage_text_rows": COVERAGE_TEXT_ROWS,
            "refresh_value": REFRESH_VALUE,
        },
        "outcomes": [],
        "failed": 0,
    }
    if args.provenance:
        report["build_provenance"] = load_provenance(args.provenance)

    services = {
        label: HarnessJsonRpcService([binary, "--jsonrpc"], label,
                                     cwd=args.work_dir)
        for label, binary in sorted(binaries.items())}
    failed = 0
    outcomes = {}
    try:
        for label, binary in sorted(binaries.items()):
            outcome = {
                "binary": label, "path": binary, "pass": False,
                "failures": [], "empty_dir": None, "flow": None,
            }
            failures = []
            service = services[label]
            # Auditable identity: never trust the caller-provided
            # label alone.  The binary's own system.describe answer
            # must claim the expected implementation.
            try:
                implementation, describe = describe_identity(service)
                outcome_identity = file_evidence(binary)
                outcome_identity.update({
                    "implementation": implementation,
                    "describe_result": describe,
                })
                outcome["identity"] = outcome_identity
                if implementation != label:
                    failures.append(
                        f"system.describe claims implementation "
                        f"{implementation!r}, expected {label!r} for "
                        f"binary {binary}")
            except (AssertionError, KeyError, TypeError, ValueError) as exc:
                failures.append(f"identity audit failed: {exc}")

            empty_out = os.path.join(
                args.work_dir,
                f"wh-empty-{label}-{uuid.uuid4().hex[:8]}.jsonl")
            reports, error_data, rows = maintenance_list(
                service, empty_dir, empty_out)
            outcome["empty_dir"] = {
                "error": error_data, "reports": reports, "rows": rows,
                "entries": kind_entries(reports)}
            if error_data:
                outcome["empty_dir"]["negative_matched"] = (
                    error_data.get("code") == "os_unsupported"
                    and error_data.get("outcome") == "read_only_failure")
            if os.path.exists(empty_out):
                os.remove(empty_out)

            live_dir = os.path.join(args.work_dir, f"live-{label}")
            shutil.rmtree(live_dir, ignore_errors=True)
            flow = run_refresh_envelope_flow(service, live_dir, label)
            outcome["flow"] = flow

            # Deterministic abort/failure cleanup proofs for the
            # removal-output collector (both platforms: the collector
            # logic is the same product code; the Windows host adds
            # the open-handle nuance the fix addresses).
            abort_facts, abort_failures = run_refresh_abort_exercises(
                service, args.work_dir, label)
            outcome["refresh_abort"] = abort_facts
            failures.extend(abort_failures)

            if report_mode == "linux-negative":
                # Negative control over the real flow directory and
                # the empty directory: both probes must record the
                # truthful os_unsupported/read_only_failure negative.
                probes = [("empty_dir", outcome["empty_dir"])]
                flow_out = os.path.join(
                    args.work_dir, f"wh-flow-{label}-"
                    f"{uuid.uuid4().hex[:8]}.jsonl")
                reports, error, rows = maintenance_list(
                    service, live_dir, flow_out)
                flow_probe = {"error": error, "reports": reports,
                              "rows": rows, "entries": kind_entries(reports)}
                if error:
                    flow_probe["negative_matched"] = (
                        error.get("code") == "os_unsupported"
                        and error.get("outcome") == "read_only_failure")
                outcome["refresh_dir_probe"] = flow_probe
                if os.path.exists(flow_out):
                    os.remove(flow_out)
                probes.append(("refresh_dir", flow_probe))
                for directory_name, probed in probes:
                    if not probed.get("negative_matched"):
                        failures.append(
                            f"{directory_name} did not record the "
                            "truthful os_unsupported/read_only_failure "
                            f"negative: error {probed.get('error')!r} "
                            f"reports {probed.get('reports')!r}")
            else:
                # Windows qualification path.
                empty = outcome["empty_dir"]
                if empty["error"]:
                    failures.append(
                        f"maintenance.list failed on Windows over the "
                        f"empty directory: {empty['error']!r}")
                else:
                    try:
                        empty_entries = int(empty.get("entries") or "0")
                    except ValueError:
                        empty_entries = None
                    empty_rows = empty.get("rows") or []
                    if empty_entries != 0 or len(empty_rows) != 0 or \
                            empty_entries != len(empty_rows):
                        failures.append(
                            f"empty directory must report entries == "
                            f"rows == 0, got entries "
                            f"{empty.get('entries')!r} and "
                            f"{len(empty_rows)} rows")

                # Native refresh exercise: prove the Go removalCollector
                # Windows fix at the product boundary -- the refresh
                # completes, publishes the exact removal log, leaves no
                # private temporary, and the pinning reader closes.
                refresh_facts, refresh_failures = (
                    complete_native_refresh_exercise(service, live_dir,
                                                     flow))
                outcome["refresh_native"] = refresh_facts
                failures.extend(refresh_failures)
                # Leftover GC pairs from the native retirement are
                # timing-dependent (the cleanup machine completes them
                # best-effort); they are recorded as facts, never
                # asserted.

                # Deterministic GC pair proof: one format-valid
                # synthesized pair in a dedicated directory, listed,
                # cross-listed, removed with the listed row unchanged,
                # and proven durably absent.
                gc_dir = os.path.join(args.work_dir, f"gc-{label}")
                shutil.rmtree(gc_dir, ignore_errors=True)
                outcome["synth"] = synthesize_gc_pair(gc_dir)
                flow_out = os.path.join(
                    args.work_dir, f"wh-synth-{label}-"
                    f"{uuid.uuid4().hex[:8]}.jsonl")
                reports, error, rows = maintenance_list(
                    service, gc_dir, flow_out)
                if error:
                    failures.append(
                        f"maintenance.list failed on Windows over the "
                        f"synthesized pair directory: {error!r}")
                else:
                    entries = kind_entries(reports)
                    try:
                        entries_int = int(entries or "0")
                    except ValueError:
                        entries_int = None
                    if entries_int != 2 or len(rows) != 2 or \
                            entries_int != len(rows):
                        failures.append(
                            f"synthesized pair directory must report "
                            f"exactly 2 entries/rows (envelope plus "
                            f"inert payload candidates), got entries "
                            f"{entries!r} and {len(rows)} rows")
                    failures.extend(
                        check_synthesized_pair_rows(rows, gc_dir))
                    outcome["windows_listing"] = {
                        "entries": entries, "rows": rows}
                    if rows:
                        envelope_row = next(
                            (r for r in rows
                             if r.get("candidate_kind") == "envelope"),
                            rows[0])
                        outcome["windows_directory_identity"] = (
                            envelope_row.get("directory_identity"))
                        outcome["row_used"] = envelope_row
                    if os.path.exists(flow_out):
                        os.remove(flow_out)

                    # Cross-product directory identity: the other
                    # product lists the same directory; the
                    # directory_identity member of its rows must equal
                    # this product's record.
                    other = "go" if label == "rust" else "rust"
                    cross_out = os.path.join(
                        args.work_dir, f"wh-cross-{label}-"
                        f"{uuid.uuid4().hex[:8]}.jsonl")
                    cross_reports, cross_error, cross_rows = (
                        maintenance_list(services[other], gc_dir,
                                         cross_out))
                    cross = {"binary": other}
                    if cross_error:
                        cross["error"] = cross_error
                        failures.append(
                            f"cross-listing by {other} failed over the "
                            f"{label} synthesized pair directory: "
                            f"{cross_error!r}")
                    else:
                        cross["entries"] = kind_entries(cross_reports)
                        cross["rows"] = cross_rows
                        # Both products' listings must validate: the
                        # cross product scans the same synthesized
                        # pair, so the same pair-row checks apply to
                        # its rows.
                        cross_failures = check_synthesized_pair_rows(
                            cross_rows, gc_dir)
                        cross["rows_valid"] = not cross_failures
                        local_identity = outcome.get(
                            "windows_directory_identity")
                        cross["identity_matches"] = [
                            row.get("directory_identity") == local_identity
                            for row in cross_rows]
                        cross["directory_identity"] = (
                            cross_rows[0].get("directory_identity")
                            if cross_rows else None)
                        local_entries = (
                            outcome.get("windows_listing") or {}
                        ).get("entries")
                        cross["entries_match"] = (
                            cross["entries"] == local_entries)
                        cross["matched"] = (
                            bool(cross_rows)
                            and all(cross["identity_matches"]))
                        for finding in cross_failures:
                            failures.append(
                                f"cross-listing by {other} failed "
                                f"validation: {finding}")
                        if not cross["entries_match"]:
                            failures.append(
                                f"cross-listing entries "
                                f"{cross['entries']!r} must equal the "
                                f"local listing entries {local_entries!r}")
                        if not cross["matched"]:
                            failures.append(
                                f"directory_identity mismatch between "
                                f"{label} and {other}: every cross row "
                                f"must carry the identity "
                                f"{local_identity!r}, got "
                                f"{cross['identity_matches']!r} over "
                                f"{len(cross_rows)} rows")
                    outcome["cross_listing"] = cross
                    if os.path.exists(cross_out):
                        os.remove(cross_out)

                    if not failures:
                        # Pair proven listed with a valid identity and
                        # no problem.  Remove it through the envelope
                        # row -- the envelope is the authenticated GC
                        # authority, so its row is the removable entry;
                        # the inert_payload row carries the payload
                        # identity and is listing/classification
                        # evidence (gc_maintenance.rs remove requires
                        # the envelope identity).  Then prove durable
                        # absence: the directory must contain nothing
                        # at all after the removal.
                        envelope_row = next(
                            (r for r in rows
                             if r.get("candidate_kind") == "envelope"),
                            None)
                        if envelope_row is None:
                            failures.append(
                                "no envelope row among the listed "
                                "housekeeping rows")
                        else:
                            outcome["removed_rows"] = []
                            removal = service.call(
                                "rm", "iprange.v1.maintenance.remove",
                                {"entry": envelope_row})
                            outcome["removed_rows"].append({
                                "row": envelope_row, "response": removal})
                            if "error" in removal:
                                failures.append(
                                    "maintenance.remove failed with "
                                    "the listed envelope row passed "
                                    "unchanged: "
                                    f"{json.dumps(removal['error'].get('data', {}))[:300]}")
                        remaining = sorted(os.listdir(gc_dir))
                        outcome["envelopes_after"] = remaining
                        if remaining:
                            failures.append(
                                f"GC pair files still present after "
                                f"maintenance.remove: {remaining}")
                        after_out = os.path.join(
                            args.work_dir, f"wh-after-{label}-"
                            f"{uuid.uuid4().hex[:8]}.jsonl")
                        after_reports, after_error, after_rows = (
                            maintenance_list(service, gc_dir, after_out))
                        outcome["after_listing"] = {
                            "error": after_error,
                            "entries": kind_entries(after_reports),
                            "rows": after_rows}
                        if os.path.exists(after_out):
                            os.remove(after_out)
                        if after_error or after_rows or \
                                kind_entries(after_reports) != "0":
                            failures.append(
                                "synthesized pair directory still lists "
                                "windows_housekeeping entries after "
                                "removal: error "
                                f"{after_error!r}, rows {after_rows}, "
                                "entries "
                                f"{kind_entries(after_reports)!r}")

            outcome["failures"] = failures
            outcome["pass"] = not failures
            if failures:
                failed += 1
            outcomes[label] = outcome
            report["outcomes"].append(outcome)
    finally:
        for service in services.values():
            service.close()

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
