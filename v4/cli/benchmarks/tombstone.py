"""Prove the closed-handle tombstone evicts the oldest handle.

One session opens and closes the same immutable file 1025 times.
The first closed handle is evicted. A later lookup on it is
handle_not_found. The newest closed handle is still handle_closed.
A runner that rejected every closed handle would fail the newest
lookup. A runner that never evicted would fail the oldest lookup.
"""

import argparse
import os
import sys
import tempfile

from client import BenchSession

CAP = 1024


def fail(message):
    print(f"FAIL {message}", file=sys.stderr)
    return 1


def publish(session, work):
    feed = os.path.join(work, "feed.txt")
    with open(feed, "w", encoding="utf-8") as stream:
        stream.write("192.0.2.1\n")
    destination = os.path.join(work, "current.iprange")
    response = session.raw("iprange.v1.current.publish", {
        "input": {
            "paths": [feed],
            "family": "ipv4",
            "fix_network": True,
            "default_prefix": 32,
            "dns": {"threads": 1, "silent": True},
            "expand_at_paths": False,
            "max_line_bytes": 1024,
            "max_expanded_paths": 4,
        },
        "feed": "alpha",
        "value_tag": {"text": "feed"},
        "metadata": {"mode": "replace_utf8", "text": "alpha"},
        "destination": destination,
        "publication_policy": "fail_if_exists",
        "immutable_feed_budget": {
            "max_heap_bytes": "16777216",
            "max_output_pages": "20000",
            "max_workspace_pages": "20000",
            "max_open_files": 3,
        },
    })
    if "error" in response:
        raise AssertionError(response["error"])
    return destination


def open_reader(session, path):
    response = session.raw("iprange.v1.reader.open", {
        "source": {"path": path, "mode": "immutable"},
    })
    if "error" in response:
        raise AssertionError(response["error"])
    return response["result"]["reader"]


def close_reader(session, handle):
    response = session.raw("iprange.v1.reader.close", {"reader": handle})
    if "error" in response:
        raise AssertionError(response["error"])


def lookup_code(session, handle):
    response = session.raw("iprange.v1.reader.lookup", {
        "reader": handle,
        "addresses": ["192.0.2.1"],
    })
    error = response.get("error", {})
    data = error.get("data", {})
    return data.get("code"), data.get("outcome")


def prove(binary, work):
    session = BenchSession(binary)
    try:
        path = publish(session, work)
        first = open_reader(session, path)
        close_reader(session, first)
        code, outcome = lookup_code(session, first)
        if (code, outcome) != ("handle_closed", "not_started"):
            raise AssertionError(f"first close is {code}/{outcome}, want handle_closed")
        handles = [first]
        for _ in range(CAP):
            handle = open_reader(session, path)
            close_reader(session, handle)
            handles.append(handle)
        oldest = handles[0]
        newest = handles[-1]
        oldest_code, oldest_outcome = lookup_code(session, oldest)
        newest_code, newest_outcome = lookup_code(session, newest)
        if (oldest_code, oldest_outcome) != ("handle_not_found", "not_started"):
            raise AssertionError(f"evicted handle is {oldest_code}/{oldest_outcome}, want handle_not_found")
        if (newest_code, newest_outcome) != ("handle_closed", "not_started"):
            raise AssertionError(f"newest handle is {newest_code}/{newest_outcome}, want handle_closed")
    finally:
        session.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--rust", required=True)
    parser.add_argument("--go", required=True)
    args = parser.parse_args()
    for label, path in (("rust", args.rust), ("go", args.go)):
        if not os.path.isabs(path) or not os.access(path, os.X_OK):
            return fail(f"{label} is not an absolute executable: {path}")
    with tempfile.TemporaryDirectory(prefix="iprange-tombstone-") as work:
        for name, binary in (("rust", args.rust), ("go", args.go)):
            engine_work = os.path.join(work, name)
            os.makedirs(engine_work)
            prove(binary, engine_work)
    print(f"PASS tombstone eviction after {CAP + 1} closes")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, ValueError, AssertionError, KeyError) as exc:
        sys.exit(fail(str(exc)))
