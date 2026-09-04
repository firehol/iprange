#!/usr/bin/env python3
"""Kind-universe completeness gate for the milestone-4 evidence battery.

The mechanical file-kind ledger can only prove what the executed cases
observe.  This gate reads every matrix report (``--matrix``) and the
crash report (``--crash``) of one evidence revision and fails when a
required artifact kind has no observed evidence at all: the per-case
ledgers (path -> kind -> actor.method lineage), the root aggregates,
and the crash scenarios' kind lists.  A green gate means the battery
mechanically exercised every artifact class the v4 contract can
produce (main files, live sidecars, publication reservations,
authorized scratch, adapter outputs, metadata deliveries); "zero
unknown" is only meaningful when every required kind was actually
observed.

Exit status 0 when every required kind is covered; 1 otherwise.
"""

import argparse
import json
import sys

REQUIRED_KINDS = [
    "v4_main",
    "live_sidecar",
    "publication_reservation",
    "authorized_scratch",
    "adapter_output",
    "metadata_delivery",
]


def matrix_evidence(path):
    """Kind -> set of "actor.method" producers observed by one matrix."""

    with open(path, encoding="utf-8") as stream:
        report = json.load(stream)
    evidence = {}
    cases = report.get("cases", [])
    for case in cases:
        if case.get("status") != "PASS":
            continue
        for rel, facts in case.get("file_kinds", {}).items():
            bucket = evidence.setdefault(facts["kind"], set())
            bucket.update(facts.get("created_by", []))
    for kind, counts in report.get("file_kinds", {}).items():
        bucket = evidence.setdefault(kind, set())
        bucket.update(counts.get("created_by", {}).keys())
    return evidence, len(cases)


def crash_evidence(path):
    """Kind set observed by the crash scenarios of one crash report."""

    with open(path, encoding="utf-8") as stream:
        report = json.load(stream)
    evidence = {}
    for scenario in report.get("scenarios", []):
        for kind in scenario.get("kinds", []):
            evidence.setdefault(kind, set()).add(
                f"crash:{scenario.get('scenario', '?')}")
    return evidence, len(report.get("scenarios", []))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--matrix", action="append", default=[],
                        metavar="PATH", help="one matrix report (repeatable)")
    parser.add_argument("--crash", action="append", default=[],
                        metavar="PATH", help="one crash report (repeatable)")
    args = parser.parse_args()
    if not args.matrix and not args.crash:
        parser.error("at least one --matrix or --crash report is required")

    coverage = {kind: set() for kind in REQUIRED_KINDS}
    sources = []
    for path in args.matrix:
        evidence, cases = matrix_evidence(path)
        sources.append(f"matrix {path} ({cases} cases)")
        for kind, producers in evidence.items():
            coverage.setdefault(kind, set()).update(producers)
    for path in args.crash:
        evidence, scenarios = crash_evidence(path)
        sources.append(f"crash {path} ({scenarios} scenarios)")
        for kind, producers in evidence.items():
            coverage.setdefault(kind, set()).update(producers)

    missing = [kind for kind in REQUIRED_KINDS if not coverage[kind]]
    print("Artifact-kind coverage gate")
    print("Sources: " + "; ".join(sources))
    for kind in REQUIRED_KINDS:
        producers = sorted(coverage[kind])
        status = "OK  " if producers else "MISS"
        print(f"  {status} {kind}: {len(producers)} producer(s)")
        for producer in producers[:6]:
            print(f"         {producer}")
        if len(producers) > 6:
            print(f"         ... {len(producers) - 6} more")
    if missing:
        print(f"FAIL: required kinds never observed: {sorted(missing)}")
        return 1
    print("PASS: every required artifact kind was observed "
          "by the executed battery")
    return 0


if __name__ == "__main__":
    sys.exit(main())
