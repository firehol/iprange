"""Kill a named-feed replace and prove the prior feeds survive.

The replace is a live transaction. Killing the process before it
answers must leave the committed generation unchanged. Beta is not the
feed being replaced, so its address must still match. Alpha's original
address must still match too. A replace that finished before the kill
is a failed proof, not a pass.
"""

import argparse
import os
import signal
import sys
import tempfile
import time

from client import BenchSession
from generate import generate, write_text


WRITER = {
    "max_heap_bytes": "268435456",
    "max_private_pages": "200000",
    "max_growth_pages": "200000",
    "max_open_files": 4,
}
PUBLISH_BUDGET = {
    "max_heap_bytes": "268435456",
    "max_output_pages": "200000",
    "max_workspace_pages": "200000",
    "max_open_files": 3,
}




def publish(session, text_path, destination, name):
    return session.call("iprange.v1.current.publish", {
        "input": {
            "paths": [text_path],
            "family": "ipv4",
            "fix_network": True,
            "default_prefix": 32,
            "dns": {"threads": 1, "silent": True},
            "expand_at_paths": False,
            "max_line_bytes": 1024,
            "max_expanded_paths": 4,
        },
        "feed": name,
        "value_tag": {"text": "feed"},
        "metadata": {"mode": "replace_utf8", "text": name},
        "destination": destination,
        "publication_policy": "fail_if_exists",
        "immutable_feed_budget": PUBLISH_BUDGET,
    })


def create_feed(session, database, name, source):
    session.call("iprange.v1.feeds.create", {
        "path": database,
        "feed": name,
        "current": {"source": {"path": source, "mode": "immutable"}, "feed": name},
        "metadata": {"mode": "keep"},
        "writer_budget": WRITER,
    })


def matches(session, database, address):
    result = session.call("iprange.v1.query.matching_feeds", {
        "source": {"path": database, "mode": "live"},
        "addresses": [address],
        "output": {
            "path": database + ".match-" + address.replace(".", "-") + ".csv",
            "format": "csv",
            "publication_policy": "replace_existing",
            "result_budget": {
                "max_rows": "16",
                "max_output_bytes": "4096",
                "max_open_files": 3,
            },
        },
    })
    return result["matching_feed_count"]


def baseline_rows(session, database, csv_path):
    """Exact post-state baseline: address → feed identities.

    The rows are compared as a set with identities, not substrings:
    losing an address, reassigning it to another feed, or exposing
    uncommitted replacement coverage all change the row set.
    """
    result = session.call("iprange.v1.query.matching_feeds", {
        "source": {"path": database, "mode": "live"},
        "addresses": ["10.0.0.1", "10.0.0.2", "10.0.0.9"],
        "output": {
            "path": csv_path,
            "format": "csv",
            "publication_policy": "replace_existing",
            "result_budget": {
                "max_rows": "16",
                "max_output_bytes": "4096",
                "max_open_files": 3,
            },
        },
    })
    with open(csv_path, encoding="utf-8") as stream:
        next(stream)
        rows = {tuple(line.rstrip("\n").split(",", 1)) for line in stream if line.strip()}
    return result["matching_feed_count"], rows


def prove(binary, work):
    alpha = os.path.join(work, "alpha.txt")
    beta = os.path.join(work, "beta.txt")
    huge = os.path.join(work, "huge.txt")
    with open(alpha, "w", encoding="utf-8") as stream:
        stream.write("10.0.0.1\n10.0.0.2\n")
    with open(beta, "w", encoding="utf-8") as stream:
        stream.write("10.0.0.9\n")
    with open(huge, "w", encoding="utf-8") as stream:
        write_text(generate(3, 400000, 4, 2_000_000), stream)
    session = BenchSession(binary, killable=True)

    def forward(signum, frame):
        del signum, frame
        session.kill()
        raise SystemExit(143)

    previous = signal.signal(signal.SIGTERM, forward)
    try:
        publish(session, alpha, os.path.join(work, "alpha.iprange"), "alpha")
        publish(session, beta, os.path.join(work, "beta.iprange"), "beta")
        publish(session, huge, os.path.join(work, "huge.iprange"), "huge")
        database = os.path.join(work, "db.iprange")
        session.call("iprange.v1.database.create", {
            "path": database,
            "family": "ipv4",
            "value_kind": "membership",
            "structure_kind": "none",
            "value_tag": {"text": "feeds"},
            "reader_capacity": 4,
        })
        create_feed(session, database, "alpha", os.path.join(work, "alpha.iprange"))
        create_feed(session, database, "beta", os.path.join(work, "beta.iprange"))
        baseline_csv = os.path.join(work, "baseline.csv")
        count, rows = baseline_rows(session, database, baseline_csv)
        expected = {("10.0.0.1", "alpha"), ("10.0.0.2", "alpha"), ("10.0.0.9", "beta")}
        if rows != expected or count != "3":
            raise AssertionError(
                f"pre-crash baseline differs: count={count} rows={sorted(rows)}")
        before = os.path.getsize(database)
        session.submit("iprange.v1.feeds.replace", {
            "path": database,
            "feed": "alpha",
            "current": {
                "source": {"path": os.path.join(work, "huge.iprange"), "mode": "immutable"},
                "feed": "huge",
            },
            "metadata": {"mode": "keep"},
            "writer_budget": WRITER,
        })
        # The writer grows the live file when the replace starts. Kill then.
        deadline = time.perf_counter() + 5
        grew = False
        while time.perf_counter() < deadline:
            if session.proc.poll() is not None:
                raise AssertionError("replace finished before the live file grew")
            if os.path.getsize(database) > before:
                grew = True
                break
            time.sleep(0.001)
        if not grew:
            raise AssertionError("replace did not grow the live file before the deadline")
        if session.proc.poll() is not None:
            raise AssertionError("replace finished before the kill; the crash proof did not run")
        session.kill()
    finally:
        signal.signal(signal.SIGTERM, previous)
        if session.proc.poll() is None:
            session.kill()
        session.close_forced()
    opened = BenchSession(binary)
    try:
        survived_csv = os.path.join(work, "survived.csv")
        count, rows = baseline_rows(opened, database, survived_csv)
    finally:
        opened.close()
    expected = {("10.0.0.1", "alpha"), ("10.0.0.2", "alpha"), ("10.0.0.9", "beta")}
    if rows != expected or count != "3":
        raise AssertionError(
            f"prior feeds did not survive intact: count={count} rows={sorted(rows)}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--rust", required=True)
    parser.add_argument("--go", required=True)
    args = parser.parse_args()
    for label, binary in (("rust", args.rust), ("go", args.go)):
        with tempfile.TemporaryDirectory(prefix=f"iprange-feed-crash-{label}-") as work:
            prove(binary, work)
        print(f"PASS {label} prior feeds survived replace crash")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, AssertionError, KeyError) as exc:
        print(f"FAIL {exc}", file=sys.stderr)
        sys.exit(1)
