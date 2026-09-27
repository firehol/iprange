"""Kill a named-feed replace and prove the prior feeds survive.

The replace is a live transaction. Killing the process before it
answers must leave the committed generation unchanged. Beta is not the
feed being replaced, so its address must still match. Alpha's original
address must still match too. A replace that finished before the kill
is a failed proof, not a pass.
"""

import argparse
import json
import os
import signal
import subprocess
import sys
import tempfile
import time

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


class Session:
    def __init__(self, binary):
        self.proc = subprocess.Popen(
            [binary, "--jsonrpc"],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            start_new_session=True,
        )
        self.next_id = 1

    def call(self, method, params, wait=True):
        request_id = str(self.next_id)
        self.next_id += 1
        request = {"jsonrpc": "2.0", "id": request_id, "method": method, "params": params}
        self.proc.stdin.write(json.dumps(request).encode("utf-8") + b"\n")
        self.proc.stdin.flush()
        if not wait:
            return request_id
        line = self.proc.stdout.readline()
        if not line:
            raise AssertionError(f"{method} returned no response")
        response = json.loads(line)
        if "error" in response:
            raise AssertionError(response["error"])
        return response["result"]

    def kill(self):
        os.killpg(os.getpgid(self.proc.pid), signal.SIGKILL)
        self.proc.wait(timeout=10)


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
    session = Session(binary)
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
    if matches(session, database, "10.0.0.1") != "1":
        raise AssertionError("alpha was not committed before the crash")
    if matches(session, database, "10.0.0.9") != "1":
        raise AssertionError("beta was not committed before the crash")
    before = os.path.getsize(database)
    session.call("iprange.v1.feeds.replace", {
        "path": database,
        "feed": "alpha",
        "current": {
            "source": {"path": os.path.join(work, "huge.iprange"), "mode": "immutable"},
            "feed": "huge",
        },
        "metadata": {"mode": "keep"},
        "writer_budget": WRITER,
    }, wait=False)
    # The writer grows the live file when the replace starts. Kill then.
    # Killing before that growth does not prove the call was in progress.
    # Killing after the process exits does not prove a crash.
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
        session.kill()
        raise AssertionError("replace did not grow the live file before the deadline")
    if session.proc.poll() is not None:
        raise AssertionError("replace finished before the kill; the crash proof did not run")
    session.kill()
    opened = Session(binary)
    try:
        alpha_count = matches(opened, database, "10.0.0.1")
        beta_count = matches(opened, database, "10.0.0.9")
    finally:
        opened.proc.stdin.close()
        opened.proc.wait(timeout=30)
    if alpha_count != "1" or beta_count != "1":
        raise AssertionError(
            f"prior feeds did not survive: alpha={alpha_count} beta={beta_count}")


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
    except (OSError, AssertionError, json.JSONDecodeError, KeyError) as exc:
        print(f"FAIL {exc}", file=sys.stderr)
        sys.exit(1)
