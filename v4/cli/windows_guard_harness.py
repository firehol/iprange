#!/usr/bin/env python3
"""Native same-source-guard session qualification for Windows spellings.

Wave 19.11 (astra turn-3 P1s): the same-source guard must refuse
every Windows spelling of the absent reader sidecar of a source
database at the normal JSON-RPC product interface, while still
allowing distinct destinations:

- drive-relative        "C:db.iprange.readers"   (per-drive cwd)
- drive-relative upper  "C:DB.IPRANGE.READERS"
- absolute upper        "C:\\review\\DB.IPRANGE.READERS"
- rooted-without-volume "\\review\\db.iprange.readers"

On Windows every spelling above denotes the protected sidecar
pathname; on POSIX (the negative control) all of them denote distinct
paths and must be allowed, proving the guard does not over-refuse.

The harness drives ``iprange.v1.database.metadata.get`` with a file
delivery over one persistent JSON-RPC service per product, against a
real v4 database copied into the working directory, and after the
refusal cases proves: the source bytes are unchanged, the sidecar is
still absent, and a follow-up metadata request still succeeds
(reopening).  Per-binary evidence records auditable identities: the
binary absolute path, its SHA-256, and one ``system.describe`` call
whose ``implementation`` member must claim the expected language.

Report schema: ``iprange-cli-windows-guard-report-v1``.
"""

import argparse
import hashlib
import json
import os
import shutil
import sys

sys.dont_write_bytecode = True
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))  # repo root
sys.path.insert(0, os.path.join(ROOT, "v4/cli"))

from crash_harness import HarnessJsonRpcService  # noqa: E402

REPORT_SCHEMA = "iprange-cli-windows-guard-report-v1"
ACCEPTED_MESSAGE = "destination must differ from the source database"
IS_WINDOWS = os.name == "nt"


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


def guard_cases(work):
    """Windows-targeted spellings of the absent sidecar plus the
    distinct-destination control.  Expectation depends on the host
    platform (Windows refuses, POSIX allows)."""
    drive = work[:2] if len(work) >= 2 and work[1] == ":" else None
    cases = [
        (
            "drive_relative",
            "%sdb.iprange.readers" % drive if drive else None,
        ),
        (
            "drive_relative_upper",
            "%sDB.IPRANGE.READERS" % drive if drive else None,
        ),
        ("absolute_upper", os.path.join(work, "DB.IPRANGE.READERS")),
        (
            "rooted_sidecar",
            os.path.sep + work[len(drive):].lstrip("\\/")
            + os.path.sep + "db.iprange.readers"
            if drive
            else None,
        ),
        ("control_allowed", os.path.join(work, "meta.txt")),
    ]
    return [
        (name, path) for name, path in cases if path is not None
    ]


def run_product(binary, label, work, fixture, provenance):
    product = {}

    binary = os.path.abspath(binary)
    service = HarnessJsonRpcService(
        [binary, "--jsonrpc"], label, cwd=work
    )
    try:
        product["binary"] = file_evidence(binary)
        product["implementation"] = describe_implementation(service, label)
        source = os.path.join(work, "db.iprange")
        shutil.copyfile(fixture, source)
        before = hashlib.sha256(open(source, "rb").read()).hexdigest()
        product["source_sha256_before"] = before
        product["sidecar_absent_before"] = not os.path.exists(
            source + ".readers"
        )

        cases = {}
        for name, destination in guard_cases(work):
            response = service.call(
                "g1", "iprange.v1.database.metadata.get",
                metadata_get_frame(source, destination),
            )
            refused = "error" in response
            error_data = response.get("error", {}).get("data", {})
            error_code = error_data.get("code")
            error_outcome = error_data.get("outcome")
            error_message = response.get("error", {}).get("message", "")
            if IS_WINDOWS:
                expected = name != "control_allowed"
            else:
                # POSIX negative control: every spelling denotes a
                # distinct path and must stay allowed.
                expected = False
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
                        and ACCEPTED_MESSAGE in error_message
                    )
                ),
            }
            if not cases[name]["ok"]:
                raise AssertionError(
                    "%s metadata.get to %r: %s"
                    % (label, destination, json.dumps(cases[name])[:400])
                )

        after = hashlib.sha256(open(source, "rb").read()).hexdigest()
        sidecar = os.path.join(work, "db.iprange.readers")
        cases["sidecar_absent_after"] = not os.path.exists(sidecar)
        cases["source_unchanged"] = after == before

        # Reopening: one more file delivery against the same source.
        reopen = os.path.join(work, "meta-reopen.txt")
        response = service.call(
            "g2", "iprange.v1.database.metadata.get",
            metadata_get_frame(source, reopen),
        )
        cases["reopen_allowed"] = "error" not in response and os.path.exists(
            reopen
        )
        if not cases["reopen_allowed"]:
            raise AssertionError(
                "%s reopen delivery failed: %s"
                % (label, json.dumps(response)[:300])
            )

        product["cases"] = cases
        product["all_ok"] = all(
            v.get("ok", True) for k, v in cases.items()
            if isinstance(v, dict)
        ) and cases["source_unchanged"] and cases["sidecar_absent_after"] \
            and cases["reopen_allowed"]
    finally:
        service.close()

    print("%s: %s" % ("OK " if product["all_ok"] else "BAD", label))
    return product


def main():
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
        work = os.path.join(args.work, label)
        os.makedirs(work, exist_ok=True)
        product = run_product(binary, label, work, args.fixture, provenance)
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
