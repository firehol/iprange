#!/usr/bin/env python3
"""Arm table for the stat-to-open swap-race battery.

An arm is one user-supplied path that a production engine reaches through a
``stat``-then-``open`` sequence, plus the JSON-RPC request that drives it.
Each arm is pinned to two answers that must hold before any racing is
meaningful:

* ``stable_success`` -- the answer when the name is a stable regular file.
  It must be a real success (``RESULT``): an arm whose request fails for an
  unrelated reason never reaches the open under test, so its "no hangs"
  result proves nothing (this is the defect the wave-19.22 probe had).
* ``stable_refusal`` -- the answer when the name is a stable FIFO. The class
  is the frozen product refusal for that arm (``invalid_path`` for writer
  inputs and metadata sources, ``invalid_argument`` for the immutable
  reader open). Rust is the semantic authority and Go must match exactly,
  the same parity rule the committed FIFO-surface gate applies.

Fixture shapes here mirror the case corpus: live databases come from
``iprange.v1.database.create`` served by the binary under test, the immutable
snapshot for the reader arm comes from the v4 fixture tool, and publications
seed their destination with ``fail_if_exists`` so the raced request can use
``replace_existing`` and still succeed on a regular name.
"""

import json
import os
import shutil

WRITER_BUDGET = {"max_heap_bytes": "16777216", "max_private_pages": "256",
                 "max_growth_pages": "256", "max_open_files": 4}
FEED_BUDGET = {"max_heap_bytes": "16777216", "max_output_pages": "20000",
               "max_workspace_pages": "20000", "max_open_files": 3}

CSV_ROWS = "from,to,value\n192.0.2.0,192.0.2.1,1\n"
NETSET_ROWS = "192.0.2.0/32\n192.0.2.1\n"
METADATA_BYTES = b"iprange-race-metadata"

ARM_NAMES = ("meta", "csv", "feed", "atlist", "reader")

# Fixture construction is not raced, so it gets a plain generous bound;
# a setup request that never answers is a fixture failure, not a hang.
SETUP_DEADLINE_SECONDS = 10.0


class Arm:
    """One raced open boundary.

    ``target`` is the name the swapper replaces. ``regular_template`` is a
    stable file whose hard links are renamed into ``target``; using links
    keeps the racing loop free of data copies, which is what makes the
    ``stat``-to-``open`` window small enough to hit repeatedly.
    """

    def __init__(self, name, work, target, regular_template,
                 stable_success, stable_refusal, method,
                 raced_refusals=None):
        self.name = name
        self.work = work
        self.target = target
        self.regular_template = regular_template
        self.stable_success = list(stable_success)
        self.stable_refusal = list(stable_refusal)
        # ``raced_refusals`` is the answer universe while the swapper is
        # racing. It is normally the static refusal class, plus the refusal
        # that only a lost ``stat``-to-``open`` race can produce: the
        # immutable reader open answers ``wrong_state`` once a post-open
        # identity check notices that the name stopped naming a regular
        # file, a class the static controls never reach. Measured on the
        # qualification binaries over 150 raced attempts per arm, both
        # engines answer exactly these classes and never block.
        self.raced_refusals = list(raced_refusals
                                   if raced_refusals is not None
                                   else stable_refusal)
        self.method = method

    def request(self):
        """The JSON-RPC request whose open of ``target`` is under test."""
        raise NotImplementedError

    def payload(self):
        """Bytes a wedge confirmation writes if the name is a FIFO at that
        moment. The content must be what the arm reads on its regular path,
        so "answered after the writer appeared" is a real success and not an
        answer bought with malformed input."""

        with open(self.regular_template, "rb") as stream:
            return stream.read()


class DirectReplaceArm(Arm):
    """``direct.replace`` with the raced name as metadata source or CSV input."""

    def __init__(self, name, work, target, regular_template, database,
                 csv_input, metadata_mode):
        super().__init__(name, work, target, regular_template,
                         ["RESULT"], ["-32010/invalid_path"],
                         "iprange.v1.direct.replace",
                         raced_refusals=["-32010/invalid_path"])
        self.database = database
        self.csv_input = csv_input
        self.metadata_mode = metadata_mode

    def request(self):
        metadata = ({"mode": "keep"} if self.metadata_mode == "keep" else
                    {"mode": "replace_file", "path": self.target})
        csv_path = self.csv_input if self.metadata_mode == "file" else self.target
        return {"jsonrpc": "2.0", "id": 1,
                "method": "iprange.v1.direct.replace",
                "params": {"path": self.database,
                           "input": {"path": csv_path,
                                     "max_line_bytes": 1048576},
                           "metadata": metadata,
                           "writer_budget": WRITER_BUDGET}}


class PublishArm(Arm):
    """``current.publish`` with the raced name as input path or @-list."""

    def __init__(self, name, work, target, regular_template, destination,
                 at_list):
        super().__init__(name, work, target, regular_template,
                         ["RESULT"], ["-32010/invalid_path"],
                         "iprange.v1.current.publish",
                         raced_refusals=["-32010/invalid_path"])
        self.destination = destination
        self.at_list = at_list

    def _paths(self):
        return [self.target] if not self.at_list else ["@" + self.target]

    def request(self):
        return {"jsonrpc": "2.0", "id": 1,
                "method": "iprange.v1.current.publish",
                "params": {"input": {"paths": self._paths(),
                                     "family": "ipv4",
                                     "fix_network": False,
                                     "default_prefix": 32,
                                     "dns": {"threads": 1, "silent": True},
                                     "expand_at_paths": True,
                                     "max_line_bytes": 1048576,
                                     "max_expanded_paths": 1000},
                           "feed": "alpha",
                           "value_tag": {"text": "race"},
                           "metadata": {"mode": "clear"},
                           "destination": self.destination,
                           "publication_policy": "replace_existing",
                           "immutable_feed_budget": FEED_BUDGET}}


class ReaderArm(Arm):
    """``reader.open`` of an immutable snapshot whose name is raced."""

    def __init__(self, work, target, regular_template):
        super().__init__("reader", work, target, regular_template,
                         ["RESULT"], ["-32010/invalid_argument"],
                         "iprange.v1.reader.open",
                         raced_refusals=["-32010/invalid_argument",
                                         "-32010/wrong_state"])

    def request(self):
        return {"jsonrpc": "2.0", "id": 1, "method": "iprange.v1.reader.open",
                "params": {"source": {"path": self.target,
                                      "mode": "immutable"}}}


def write_work_files(work):
    """Create the stable fixture inputs an arm may consume.

    Every arm reads from a template file rather than from the raced name, so
    a raced request that succeeds reads content that always exists.
    """

    os.makedirs(work, exist_ok=True)
    paths = {
        "csv": os.path.join(work, "template.csv"),
        "netset": os.path.join(work, "template.netset"),
        "list": os.path.join(work, "template.list"),
        "metadata": os.path.join(work, "template.metadata"),
    }
    with open(paths["csv"], "w", encoding="utf-8") as stream:
        stream.write(CSV_ROWS)
    with open(paths["netset"], "w", encoding="utf-8") as stream:
        stream.write(NETSET_ROWS)
    # The @-list names the netset template, which the engine opens after it
    # has opened the list itself; only the list is raced.
    with open(paths["list"], "w", encoding="utf-8") as stream:
        stream.write(paths["netset"] + "\n")
    with open(paths["metadata"], "wb") as stream:
        stream.write(METADATA_BYTES)
    return paths


def create_live_database(session_factory, work, path):
    """Create the live direct database the writer arms replace in place."""

    service = session_factory(work)
    observation = service.call({"jsonrpc": "2.0", "id": 0,
                                "method": "iprange.v1.database.create",
                                "params": {"path": path, "family": "ipv4",
                                           "value_kind": "direct",
                                           "structure_kind": "none",
                                           "value_tag": {"text": "direct"},
                                           "reader_capacity": 8}},
                               SETUP_DEADLINE_SECONDS)
    response = observation.response
    service.kill()
    if observation.kind != "answered" or "result" not in (response or {}):
        raise RuntimeError(f"database.create failed: "
                           f"{json.dumps(response or observation.kind)[:400]}")
    return path


def seed_publication(session_factory, arm):
    """Publish once with ``fail_if_exists`` so the raced arm can replace."""

    service = session_factory(arm.work)
    seed = {"jsonrpc": "2.0", "id": 0,
            "method": "iprange.v1.current.publish",
            "params": dict(arm.request()["params"],
                           publication_policy="fail_if_exists")}
    observation = service.call(seed, SETUP_DEADLINE_SECONDS)
    response = observation.response
    service.kill()
    if observation.kind != "answered" or "result" not in (response or {}):
        raise RuntimeError(f"publication seed failed: "
                           f"{json.dumps(response or observation.kind)[:400]}")
    return arm.destination


def build_arm(name, work, binary, fixture_tool, session_factory):
    """Build one arm, creating the fixtures it needs under ``work``."""

    if name not in ARM_NAMES:
        raise ValueError(f"unknown race arm {name!r}")
    templates = write_work_files(work)
    database = os.path.join(work, "race.iprange")
    destination = os.path.join(work, "published.iprange")
    if name == "meta":
        create_live_database(session_factory, work, database)
        arm = DirectReplaceArm("meta", work, os.path.join(work, "meta.t"),
                               templates["metadata"], database,
                               templates["csv"], "file")
    elif name == "csv":
        create_live_database(session_factory, work, database)
        arm = DirectReplaceArm("csv", work, os.path.join(work, "csv.t"),
                               templates["csv"], database,
                               templates["csv"], "keep")
    elif name == "feed":
        arm = PublishArm("feed", work, os.path.join(work, "in.netset"),
                         templates["netset"], destination, False)
        _pin_regular(arm)          # the seed reads the same name the race uses
        seed_publication(session_factory, arm)
    elif name == "atlist":
        arm = PublishArm("atlist", work, os.path.join(work, "list.t"),
                         templates["list"], destination, True)
        _pin_regular(arm)
        seed_publication(session_factory, arm)
    else:
        snapshot = os.path.join(work, "database.iprange")
        subprocess_snapshot(fixture_tool, snapshot)
        arm = ReaderArm(work, os.path.join(work, "db.t"), snapshot)
    # Every arm starts from a regular node so a helper that never races is
    # distinguishable from a fixture that never existed.
    _pin_regular(arm)
    return arm


def subprocess_snapshot(fixture_tool, path):
    """Produce one immutable v4 snapshot with the committed fixture tool."""

    import subprocess
    if not fixture_tool:
        raise ValueError("the reader arm requires --fixture-tool")
    completed = subprocess.run([fixture_tool, "direct-v4", path],
                               capture_output=True, check=False)
    if completed.returncode != 0 or not os.path.isfile(path):
        raise RuntimeError(
            "fixture tool could not produce "
            f"{path}: rc={completed.returncode} "
            f"{completed.stderr.decode('utf-8', 'replace')[:300]}")
    return path


def _pin_regular(arm):
    """Put the regular node at the raced name (no FIFO, no missing entry)."""

    if os.path.lexists(arm.target):
        os.unlink(arm.target)
    os.link(arm.regular_template, arm.target)
    return arm.target


def pin_fifo(arm):
    """Replace the raced name with a FIFO that has no writer."""

    if os.path.lexists(arm.target):
        os.unlink(arm.target)
    os.mkfifo(arm.target, 0o600)
    return arm.target


def describe(arm):
    """Report-safe description of one arm."""

    from command_sanitize import sanitized_path_value
    return {"method": arm.method, "target": sanitized_path_value(arm.target),
            "stable_success": arm.stable_success,
            "stable_refusal": arm.stable_refusal,
            "raced_refusals": arm.raced_refusals}
