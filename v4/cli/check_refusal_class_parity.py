#!/usr/bin/env python3
"""Committed refusal-class parity gate: Go and Rust must classify the same
path shape identically, on a mechanically derived arms x path-kind grid.

Why this gate exists
--------------------
Each engine decides, before it opens anything, what a path *is*, and that
decision becomes the ``data.code`` a caller sees.  Two engines can serve
every well-formed database identically while disagreeing on what a symlink,
a directory, a FIFO, a hard link, or a zero-sized file means -- and a
classification disagreement is invisible to a case corpus, because each
corpus case asserts one engine's expectation at a time and the mixed
matrices skip single-actor cases.

This gate therefore does not assert a hand-picked list of refusals.  It
takes the cross product of the arm table and the path-kind table, executes
every cell on both engines against the *same* materialized target, and
reports each cell where the transport code, the ``data.code`` class, the
``data.outcome``, or the shape of the publication evidence differ.  Message
text is not compared: parity acceptance compares ``data.code``, outcome
class, and which publication facts the reply carries, and human-readable
text is a recorded diagnostic difference.

Post-visibility durability is part of that surface.  An adapter-owned
output (``export.destination``, the metadata file delivery of
``database.metadata.get``, and the ``removals_output`` of the first-seen
refresh) must report ``io`` with ``outcome_unknown`` and its publication
facts once the destination name has become visible and durability could not
be established, and must keep a definite refusal with no publication facts
before the name appears.  Two mechanically derived path kinds carry that
boundary -- ``dest-parent-unreadable`` (the destination's parent directory
has owner mode 0311, so publishing succeeds and ``sync_directory()`` fails
with EACCES on both engines) and ``dest-collision`` (a normal parent with a
pre-existing destination under ``fail_if_exists``) -- and the arms that
publish pin the expected class, outcome, and fact set.

Descriptor pressure
-------------------
``--pressure`` adds a third axis: the same arms under a bounded
``RLIMIT_NOFILE`` band and a pre-occupied descriptor table, one fresh process
per cell and engine.  There the two engines do not owe the same class in every
band.  Design section 13.3 measures the Go writer and worker arms as reaching
success one or two bands above the Rust reference, so a cell in which exactly
one engine completed is a band gap: it is reported, and it is not scored as a
divergence.  The allowance is bounded by the committed table rather than by the
shape of the answer -- a cell is a gap only when each engine's own reply is a
class that pin allows for that engine in that band -- and the verifier derives
the pair from ``PINNED_PRESSURE_CLASSES`` and the recorded answers instead of
trusting the flag the sweep wrote, so neither the sweep nor a doctored report
can relabel a real class divergence as a gap.

Which axis a run swept is recorded as ``pressure.mode``, named by the
``--pressure`` option and checked against the swept profile set and the recorded
command line.  The milestone's full sweep is a separate committed artifact,
``refusal-class-parity-full.json``, filed beside the routine
``refusal-class-parity.json``: 378 cells over 42 profiles is an evidence file,
not a console log.

Authority
---------
Rust observable behavior is the semantic authority for refusal classes where
the JSON-RPC specification does not pin a class.  Parity alone is not enough
to protect that authority: a future wave could change *both* engines to a new
class and this gate would still report agreement.  ``PINNED_REFUSALS`` is
therefore the second, independent anchor -- it names
cells whose ``data.code``, and where the contract turns on it their
``data.outcome`` and publication evidence, are fixed from the Rust reference,
and the verifier requires both engines to answer the pinned class.  The table
itself is the third anchor: its committed entry count
(``PINNED_REFUSAL_COUNT``) and digest (``PINNED_REFUSALS_SHA256``) are checked
against the live table on every run, so deleting a pin, or loosening one from a
mapping to a bare class -- which silently drops the outcome and evidence
requirements while keeping the class -- fails the gate instead of weakening it.
Adding a pin is a deliberate act that updates both anchors in the same change.
``MANDATORY_PATH_KINDS`` and ``MANDATORY_ARMS`` are the fourth: they name,
literally rather than by derivation from the sweep tables, every shape and
every arm whose coverage is an obligation, and the verifier compares the two
lists against the tables in both directions, so deleting a row from either side
fails the gate instead of shrinking it.

Usage
-----
    nice python3 v4/cli/check_refusal_class_parity.py \
        --go BIN --rust BIN --fixture BIN --work EMPTY_DIR \
        --pressure routine \
        [--json-report FILE] [--sha256-ledger PATH] [--deadline 4.0] \
        [--retries 2] [--budget-seconds 55]

The verdict owes all three axes: a report is accepted only with a
well-formed ``pressure`` member, so a run without ``--pressure`` executes
the two target axes for inspection and then reports its own refusal.

    nice python3 v4/cli/check_refusal_class_parity.py --self-test

``--json-report`` defaults to ``refusal-class-parity.json`` inside the
caller's ``--work`` directory, which the caller owns and can discard: a bare
gate run never writes the tracked evidence at
``v4/cli/evidence/refusal-class-parity.json``, because an uncorroborated sweep
is not evidence and must not overwrite the evidence under review.  Pass that
path explicitly to record a run as evidence.

``--sha256-ledger`` names a ``sha256sum``-format ledger of the staged
binaries; when supplied, every binary digest the report records must appear in
it, so the verdict is attributable to the artifacts the wave staged.

``--self-test`` is offline: it fabricates a report from the tables and proves
the verifier rejects a synthetic divergence, an empty cell set, a deleted
fold shape, a dropped arm, a missing mandatory path kind, an unhashed or
mislabeled binary, a hang reported as agreement, and a broken pin; a pin
deleted from both the table and the report; a pin loosened to a bare class; a
table row whose obligation was un-named; and a binary digest the staged ledger
does not list.  Its own length is an obligation: the run fails unless it
executes exactly ``SELF_TEST_CASES_TOTAL`` controls, so a control cannot be
removed to leave a shorter self-test that still reports PASS.  The live run
additionally enforces a per-attempt deadline (a cell that never returns is a
hang, not a divergence) and a whole-run budget.
"""

import argparse
import copy
import hashlib
import json
import os
import platform
import selectors
import shutil
import socket
import subprocess
import sys
import queue
import threading
import tempfile
import time

sys.dont_write_bytecode = True

_HERE = os.path.dirname(os.path.abspath(__file__))
if _HERE not in sys.path:
    sys.path.insert(0, _HERE)

from command_sanitize import (  # noqa: E402
    audit_report_writers,
    profile_path,
    personal_path_in_report,
    report_provenance,
    require_paths_outside_profile,
    run_shared_self_test,
    sanitized_path_value,
    write_committed_report,
)

REPORT_SCHEMA = "iprange-cli-refusal-class-parity-report-v1"
# The committed evidence file is written only when a caller names it. A bare
# gate run must not overwrite tracked evidence: the run that found this wrote
# over evidence/refusal-class-parity.json while that file was under review, so
# the default is a scratch report inside the caller's --work directory, which
# is itself required to be empty and is the caller's to discard.
DEFAULT_REPORT_NAME = "refusal-class-parity.json"
EVIDENCE_REPORT = os.path.join(_HERE, "evidence", "refusal-class-parity.json")


def resolve_report_path(args):
    """Where one run writes its report: the explicit path, else scratch."""

    if args.json_report:
        return args.json_report
    if args.work:
        return os.path.join(args.work, DEFAULT_REPORT_NAME)
    return os.path.join(tempfile.gettempdir(), f"{os.getpid()}-"
                        + DEFAULT_REPORT_NAME)

# Transport code for a product-level refusal.  -32602 is the params
# validator's answer, which happens before any path is opened and is pinned
# by the corpus instead of here.
PRODUCT_ERROR = -32010
# A reply later than this is a hang with a tail: the open reached the kernel
# and waited.  Every arm on every path kind must answer inside it.
ATTEMPT_DEADLINE_SECONDS = 4.0
RETRIES = 2
RUN_BUDGET_SECONDS = 55.0
# Absolute ceilings for the whole run; exceeding either is a gate failure
# rather than a silently truncated grid.
MAX_CELLS = 4096

WRITER_BUDGET = {"max_heap_bytes": "16777216", "max_private_pages": "20000",
                 "max_growth_pages": "20000", "max_open_files": 4}
VALIDATION_BUDGET = {"max_heap_bytes": "16777216", "max_open_files": 4,
                     "max_scratch_bytes": "0", "max_scratch_files": 0}
RECOVERY_BUDGET = {"max_heap_bytes": "16777216", "max_open_files": 4,
                   "max_output_pages": "20000", "max_scratch_bytes": "0",
                   "max_scratch_files": 0}
SNAPSHOT_BUDGET = {"max_heap_bytes": "16777216", "max_output_pages": "512",
                   "max_open_files": 32}
RESULT_BUDGET = {"max_rows": "64", "max_output_bytes": "65536",
                 "max_open_files": 3}
IMMUTABLE_FEED_BUDGET = {"max_heap_bytes": "16777216", "max_output_pages":
                         "20000", "max_workspace_pages": "20000",
                         "max_open_files": 3}
CANDIDATE = {"label": "newest", "meta_page": 1,
             "source_identity": {"volume": "1", "file": "2"},
             "database_id": "000102030405060708090a0b0c0d0e0f",
             "transaction_id": "3",
             "commit_nonce": "000102030405060708090a0b0c0d0e0f"}
TEXT_INPUT = {"family": "ipv4", "fix_network": False, "default_prefix": 32,
              "dns": {"threads": 1, "silent": True}, "expand_at_paths": False,
              "max_line_bytes": 1024, "max_expanded_paths": 16}

# Host files whose ``st_size`` lies: a procfs file reports 0 while serving
# bytes, a sysfs file reports 4096 while serving far fewer.  A reader sized by
# ``st_size`` fabricates or drops bytes on exactly these files, so the class
# each engine answers here is a contract term, not an implementation detail.
PROCFS_SHAPE = "/proc/self/net/route"
SYSFS_SHAPE = "/sys/class/net/lo/mtu"

# Names the grid materializes inside the per-cell work directory.  Anything an
# arm may create is removed before each attempt so a leftover from an earlier
# arm cannot change the refusal being measured.
_EPHEMERAL = ("dest.iprange", "report.jsonl", "findings.jsonl",
              "snapshot-out.iprange", "published.iprange")
_AUXILIARY = ("template-live.iprange", "template-live.iprange.readers",
              "template-membership.iprange", "data.csv", "list.txt",
              "real.db", "real-dir")


def _findings(work):
    return {"path": os.path.join(work, "findings.jsonl"), "format": "jsonl",
            "publication_policy": "replace_existing",
            "result_budget": dict(RESULT_BUDGET)}


def _report_output(work):
    return {"format": "jsonl", "path": os.path.join(work, "report.jsonl"),
            "publication_policy": "replace_existing",
            "result_budget": dict(RESULT_BUDGET)}


def _fresh_refresh_target(ctx):
    """Copy the first-seen refresh database fresh for this attempt.

    The refresh commits a transaction, so a database shared across cells
    would make each attempt start from a different generation and let a
    class depend on sweep order rather than on the shape under test.  Both
    engines therefore begin every cell from byte-identical material.
    """

    base = os.path.join(ctx["work"], "refresh-main.iprange")
    _remove_any(base)
    _remove_any(base + ".readers")
    shutil.copyfile(ctx["refresh_main"], base)
    shutil.copyfile(ctx["refresh_sidecar"], base + ".readers")
    return base


# --------------------------------------------------------------------------
# Arm table.  ``slot`` records, for the evidence file, which params member
# receives the probed path; ``build`` returns the full params object.  The
# grid is the cross product of this table and PATH_KINDS -- nothing here is
# enumerated by hand, so a new arm is automatically swept over every shape.
# --------------------------------------------------------------------------
ARMS = [
    # Read-only opens.
    ("reader.open", "iprange.v1.reader.open", "source.path",
     lambda t, c: {"source": {"path": t, "mode": "immutable"}}),
    ("database.info", "iprange.v1.database.info", "source.path",
     lambda t, c: {"source": {"path": t, "mode": "immutable"}}),
    ("database.metadata.get", "iprange.v1.database.metadata.get",
     "source.path",
     lambda t, c: {"source": {"path": t, "mode": "immutable"},
                   "delivery": {"mode": "inline"}}),
    # Live-sidecar validation: the quiescent path folds a sidecar open failure
    # through live_coordination_error, which is what the sidecar-folded shape
    # pins below.
    ("validate.live", "iprange.v1.validate", "path",
     lambda t, c: {"path": t, "mode": {"kind": "live_current"},
                   "validation_budget": dict(VALIDATION_BUDGET),
                   "findings_output": _findings(c["work"])}),
    # Recovery arms: the live and offline paths take different opens, so both
    # are swept.
    ("recovery.inspect.live", "iprange.v1.recovery.inspect", "path",
     lambda t, c: {"path": t, "mode": "live",
                   "validation_budget": dict(VALIDATION_BUDGET)}),
    ("recovery.inspect.offline", "iprange.v1.recovery.inspect", "path",
     lambda t, c: {"path": t, "mode": "caller_certified_offline",
                   "validation_budget": dict(VALIDATION_BUDGET)}),
    ("recover.live", "iprange.v1.recover", "source_path",
     lambda t, c: {"source_path": t, "source_mode": "live",
                   "candidate": CANDIDATE,
                   "destination": os.path.join(c["work"], "dest.iprange"),
                   "recovery_budget": dict(RECOVERY_BUDGET),
                   "report_output": _report_output(c["work"])}),
    # Adapter output over a probed source.
    ("export", "iprange.v1.export", "source.path",
     lambda t, c: {"source": {"path": t, "mode": "immutable"},
                   "view": {"kind": "direct"}, "format": "ranges",
                   "destination": os.path.join(c["work"], "export.ranges"),
                   "publication_policy": "replace_existing",
                   "result_budget": dict(RESULT_BUDGET)}),
    # Writer targets and writer inputs.  ``direct.replace`` is swept twice:
    # once with the probed path as the database being replaced, once with it
    # as the CSV the writer must read.
    ("direct.replace", "iprange.v1.direct.replace", "path",
     lambda t, c: {"path": t,
                   "input": {"path": c["data_csv"], "max_line_bytes": 1024},
                   "metadata": {"mode": "keep"},
                   "writer_budget": dict(WRITER_BUDGET)}),
    ("direct.csv_input", "iprange.v1.direct.replace", "input.path",
     lambda t, c: {"path": c["live_main"],
                   "input": {"path": t, "max_line_bytes": 1024},
                   "metadata": {"mode": "keep"},
                   "writer_budget": dict(WRITER_BUDGET)}),
    ("metadata_source_read", "iprange.v1.database.metadata.replace",
     "metadata.path",
     lambda t, c: {"path": c["live_main"],
                   "metadata": {"mode": "replace_file", "path": t},
                   "writer_budget": dict(WRITER_BUDGET)}),
    ("database.metadata.replace", "iprange.v1.database.metadata.replace",
     "path",
     lambda t, c: {"path": t,
                   "metadata": {"mode": "replace_utf8", "text": "parity"},
                   "writer_budget": dict(WRITER_BUDGET)}),
    # Feed lifecycle arms: the database being mutated is the probed path for
    # create/delete/rename, and the imported source for import.
    ("feeds.create", "iprange.v1.feeds.create", "path",
     lambda t, c: {"path": t, "feed": "gamma",
                   "current": {"source": {"path": c["membership_main"],
                                          "mode": "immutable"},
                               "feed": "alpha"},
                   "metadata": {"mode": "keep"},
                   "writer_budget": dict(WRITER_BUDGET)}),
    ("feeds.delete", "iprange.v1.feeds.delete", "path",
     lambda t, c: {"path": t, "feed": "nosuch",
                   "metadata": {"mode": "keep"},
                   "writer_budget": dict(WRITER_BUDGET)}),
    ("feeds.rename", "iprange.v1.feeds.rename", "path",
     lambda t, c: {"path": t, "old_feed": "nosuch", "new_feed": "other",
                   "metadata": {"mode": "keep"},
                   "writer_budget": dict(WRITER_BUDGET)}),
    # The imported source is swept by ``feeds.import``; the database being
    # imported INTO is swept by ``feeds.import_target``. The two slots are
    # decided by different owners -- the source open is a read-side
    # classification, while the target is opened by the live writer and its
    # value kind and value tag are checked against the source -- so pinning
    # only the source leaves the whole target side of the workflow unswept.
    ("feeds.import", "iprange.v1.feeds.import", "source.path",
     lambda t, c: {"path": c["membership_main"],
                   "source": {"path": t, "mode": "immutable"},
                   "metadata": {"mode": "keep"},
                   "writer_budget": dict(WRITER_BUDGET)}),
    ("feeds.import_target", "iprange.v1.feeds.import", "path",
     lambda t, c: {"path": t,
                   "source": {"path": c["membership_main"],
                              "mode": "immutable"},
                   "metadata": {"mode": "keep"},
                   "writer_budget": dict(WRITER_BUDGET)}),
    # Bounded maintenance over a probed database.
    ("database.reclaim", "iprange.v1.database.reclaim", "path",
     lambda t, c: {"path": t, "max_transactions": "64", "max_pages": "64",
                   "writer_budget": dict(WRITER_BUDGET)}),
    # Publication destination: a destination that is not a regular file is
    # refused by the publication namespace, not by an open.
    ("snapshot.publish", "iprange.v1.snapshot", "destination",
     lambda t, c: {"source": {"path": c["live_main"], "mode": "live"},
                   "destination": t,
                   "publication_policy": "replace_existing",
                   "snapshot_budget": dict(SNAPSHOT_BUDGET)}),
    # The @file-list expansion arm: the probed path is the list a publisher
    # reads and expands, which is a separate owner from the CSV input.
    ("at_file_list", "iprange.v1.current.publish", "input.paths[0]",
     lambda t, c: {"input": dict(TEXT_INPUT, paths=["@" + t],
                                 expand_at_paths=True),
                   "feed": "at", "value_tag": {"text": "at"},
                   "metadata": {"mode": "clear"},
                   "destination": os.path.join(c["work"], "published.iprange"),
                   "publication_policy": "fail_if_exists",
                   "immutable_feed_budget": dict(IMMUTABLE_FEED_BUDGET)}),
    # Adapter-owned output publication.  These three arms put the probed path
    # in the *destination* slot, so what the grid measures is each output
    # owner's durability path: the metadata file delivery of
    # ``database.metadata.get``, the removals collector of the first-seen
    # refresh, and the export writer.  The existing ``export`` arm probes the
    # source slot only, so the destination surface needs its own arm.
    # ``publication_policy`` is ``fail_if_exists`` on all three, which is what
    # separates the two boundaries in one sweep: a collision before the
    # destination name appears is a definite refusal that removes the private
    # temporary and carries no publication facts, and a durability failure
    # after it appears is ``outcome_unknown`` with the facts of what was
    # delivered.  The ``missing`` path kind is the third cell of that
    # triple: a normal parent with no collision publishes, and answers RESULT.
    ("metadata.file_delivery", "iprange.v1.database.metadata.get",
     "delivery.path",
     lambda t, c: {"source": {"path": c["fixture"], "mode": "immutable"},
                   "delivery": {"mode": "file", "path": t,
                                "publication_policy": "fail_if_exists",
                                "max_output_bytes": "4096",
                                "max_open_files": 1}}),
    ("export.destination", "iprange.v1.export", "destination",
     lambda t, c: {"source": {"path": c["fixture"], "mode": "immutable"},
                   "view": {"kind": "direct"}, "format": "ranges",
                   "destination": t,
                   "publication_policy": "fail_if_exists",
                   "result_budget": dict(RESULT_BUDGET)}),
    ("removals_output", "iprange.v1.retention.first_seen.refresh",
     "removals_output.path",
     lambda t, c: {"path": _fresh_refresh_target(c),
                   "current": {"source": {"path": c["membership_main"],
                                          "mode": "immutable"},
                               "feed": "alpha"},
                   "refresh_value": 123456,
                   "removals_output": {"path": t,
                                       "publication_policy": "fail_if_exists",
                                       "result_budget": dict(RESULT_BUDGET)},
                   "metadata": {"mode": "keep"},
                   "writer_budget": dict(WRITER_BUDGET)}),
]

# --------------------------------------------------------------------------
# Path-kind table.  ``materialize`` is the single owner of what each name
# means; the grid is the cross product of this table and ARMS, so a new shape
# is swept over every arm without any cell being listed by hand.
# --------------------------------------------------------------------------
PATH_KINDS = (
    "regular-valid",
    "regular-valid-membership",
    "live-membership",
    # The live database whose value kind is direct, i.e. the one that cannot
    # carry a named feed at all. Sweeping the named-feed arms over it is what
    # pins the value-kind refusal, and it is a distinct shape from
    # ``live-membership`` because the refusal happens after the writer opened
    # a healthy database rather than while classifying a node.
    "live-direct",
    "missing",
    "dir",
    "unix-socket",
    "fifo",
    "symlink-regular",
    "symlink-dir",
    "symlink-fifo",
    "symlink-live",
    "hardlink-live",
    "procfs",
    "sysfs",
    "zero-length-live",
    "junk",
    "sidecar-folded",
    # Destination durability shapes: the probed path is the destination of an
    # adapter-owned output, and these two names separate the pre-visibility
    # refusal from the post-visibility unknown outcome.
    "dest-parent-unreadable",
    "dest-collision",
    # A destination node on a filesystem the durability proof refuses.
    # ``replace_existing`` publication must answer the durability class here
    # at every node shape, because the exchange cannot be atomic on such a
    # filesystem at all; classifying the node first would answer the
    # namespace collision class of a node that merely happens to sit at the
    # destination name and downgrade that refusal. The node is a FIFO so the
    # two answers are distinguishable: a FIFO is exactly the shape an
    # open-then-inspect probe classifies as a collision.
    "dest-fifo-crossfs",
)

# Shapes whose coverage is an obligation, stated independently of the table
# above: ``sidecar-folded`` carries the ratified quiescent-validate fold,
# ``fifo`` carries the never-block contract, ``symlink-live`` carries the
# symlinked writer-target ruling, ``hardlink-live`` and ``zero-length-live``
# carry the live-ownership and unprovable-generation rulings, and
# ``symlink-regular`` and ``live-membership`` carry the writer and named-feed
# outcome rulings.  Removing one here is a design change the gate reports, not
# a way to pass.
MANDATORY_PATH_KINDS = (
    # Every shape is an obligation, listed literally rather than derived
    # from ``PATH_KINDS``: a table entry that is not also an obligation can
    # be deleted, and deleting a row is the cheapest way to shrink this
    # gate to nothing. ``verify_report`` checks the two lists against each
    # other in both directions, so adding a shape without naming it here and
    # deleting a shape named here both fail.
    "regular-valid", "regular-valid-membership", "live-membership",
    "live-direct", "missing", "dir", "unix-socket", "fifo",
    "symlink-regular", "symlink-dir", "symlink-fifo", "symlink-live",
    "hardlink-live", "procfs", "sysfs", "zero-length-live", "junk",
    "sidecar-folded", "dest-parent-unreadable", "dest-collision",
    "dest-fifo-crossfs")

MANDATORY_ARMS = (
    # The same obligation on the other axis, stated independently of
    # ``ARMS``: each arm is a distinct product surface, and deleting one
    # would delete every cell that measures it.
    "reader.open", "database.info", "database.metadata.get", "validate.live",
    "recovery.inspect.live", "recovery.inspect.offline", "recover.live",
    "export", "direct.replace", "direct.csv_input", "metadata_source_read",
    "database.metadata.replace", "feeds.create", "feeds.delete",
    "feeds.rename", "feeds.import", "feeds.import_target",
    "database.reclaim", "snapshot.publish", "at_file_list",
    "metadata.file_delivery", "export.destination", "removals_output")

# Cells whose class is fixed from the Rust reference implementation, beyond
# what Go-vs-Rust parity can prove.  Each entry is (arm, path-kind) ->
# data.code; the verifier requires both engines to answer it.
PINNED_REFUSALS = {
    # Read-only opens refuse a non-regular source with invalid_argument
    # (open_read_only / require_regular_file), and a socket, which is neither
    # a database nor a refusal-class special case, with the io class.
    ("reader.open", "fifo"): "invalid_argument",
    ("reader.open", "dir"): "invalid_argument",
    ("reader.open", "unix-socket"): "io",
    ("database.info", "fifo"): "invalid_argument",
    ("database.metadata.get", "fifo"): "invalid_argument",
    ("export", "fifo"): "invalid_argument",
    ("validate.live", "fifo"): "invalid_argument",
    ("recovery.inspect.live", "fifo"): "invalid_argument",
    # A quiescent/offline open is read-write, and the state check precedes the
    # regular-file check, so the same shape classifies differently here.
    ("recovery.inspect.offline", "fifo"): "wrong_state",
    ("recovery.inspect.live", "symlink-dir"): "io",
    # The ratified quiescent-validate fold: a live database whose sidecar
    # cannot be opened answers the coordination class, not the underlying
    # io/format class, because the caller cannot tell the two apart.
    ("validate.live", "sidecar-folded"):
        "live_recovery_coordination_unavailable",
    # A hard link of a live database is not the live file this process owns.
    ("validate.live", "hardlink-live"): "wrong_state",
    # Content that cannot establish a live current generation is not a
    # format failure: the live arms must say the generation is unprovable.
    ("recovery.inspect.live", "junk"):
        "live_recovery_current_generation_unprovable",
    ("recovery.inspect.live", "zero-length-live"):
        "live_recovery_current_generation_unprovable",
    ("recover.live", "junk"): "recovery_candidate_changed",
    # Writer targets: a symlinked database is a live-ownership change, which
    # is a state refusal, not an io failure and not a wrong-value refusal.
    ("direct.replace", "symlink-live"): "wrong_state",
    ("feeds.create", "symlink-live"): "wrong_state",
    # Writer inputs and metadata sources are refused by a pre-open stat, so
    # they classify before any open decides the shape.
    ("direct.csv_input", "fifo"): "invalid_path",
    ("at_file_list", "fifo"): "invalid_path",
    ("metadata_source_read", "fifo"): "invalid_path",
    ("database.reclaim", "fifo"): "invalid_path",
    # A named-feed workflow over a live membership database that lacks the
    # feed performed no write, and the read-only refusal must say so.
    ("feeds.delete", "live-membership"): {
        "data_code": "name_not_found", "outcome": "read_only_failure"},
    ("feeds.rename", "live-membership"): {
        "data_code": "name_not_found", "outcome": "read_only_failure"},
    # A named-feed workflow over a live database whose value kind cannot
    # carry feeds opened that database, read its header, and refused: the
    # refusal is a read-only failure of a started operation, and an arm that
    # labels it ``not_started`` claims no work happened when a writer is
    # open. Both the value-kind refusal (a direct database cannot hold a
    # feed) and the value-tag refusal (an import whose source tag differs
    # from the target's) are pinned here, on both the target slot
    # (``feeds.import_target``) and the create/delete/rename arms.
    ("feeds.create", "live-direct"): {
        "data_code": "wrong_value_kind", "outcome": "read_only_failure"},
    ("feeds.delete", "live-direct"): {
        "data_code": "wrong_value_kind", "outcome": "read_only_failure"},
    ("feeds.rename", "live-direct"): {
        "data_code": "wrong_value_kind", "outcome": "read_only_failure"},
    ("feeds.import_target", "live-direct"): {
        "data_code": "wrong_value_kind", "outcome": "read_only_failure"},
    ("feeds.import_target", "live-membership"): {
        "data_code": "wrong_value_tag", "outcome": "read_only_failure"},
    # The destination durability boundary: a node sitting at the
    # destination name on a filesystem the durability proof refuses is the
    # durability refusal, never that node's own namespace class, and it
    # arrives before any output is constructed.
    ("snapshot.publish", "dest-fifo-crossfs"): {
        "data_code": "durability_unsupported", "outcome": "not_started"},
    ("snapshot.publish", "procfs"): {
        "data_code": "durability_unsupported", "outcome": "not_started"},
    # Post-visibility durability of an adapter-owned output.  Once the
    # destination name is visible, a failure to establish durability is the
    # unknown outcome of a delivered file: the class is ``io``, the outcome is
    # ``outcome_unknown``, the reply must carry the publication facts, and the
    # implementation must not remove the destination.  For the auxiliary
    # removals output of a committed first-seen refresh, the reply outcome
    # belongs to the transaction (``committed``) and the auxiliary file's own
    # ``outcome_unknown`` travels inside its publication facts; the two facts
    # are reported separately on purpose.
    ("metadata.file_delivery", "dest-parent-unreadable"): {
        "data_code": "io", "outcome": "outcome_unknown", "facts": "required"},
    ("export.destination", "dest-parent-unreadable"): {
        "data_code": "io", "outcome": "outcome_unknown", "facts": "required"},
    ("removals_output", "dest-parent-unreadable"): {
        "data_code": "io", "outcome": "committed", "facts": "required"},
    # Pre-visibility control: the same policy with the destination already
    # present refuses definitely, before any destination name appears, so the
    # class stays ``name_exists`` and the reply must NOT carry publication
    # facts for a file it never published.  The outcome is the one the arm
    # owed before it ever reached the destination: ``not_started`` for the
    # export writer, and ``read_only_failure`` for the metadata delivery,
    # which had already read the database.
    ("metadata.file_delivery", "dest-collision"): {
        "data_code": "name_exists", "outcome": "read_only_failure",
        "facts": "absent"},
    ("export.destination", "dest-collision"): {
        "data_code": "name_exists", "outcome": "not_started",
        "facts": "absent"},
    ("removals_output", "dest-collision"): {
        "data_code": "name_exists", "outcome": "committed",
        "facts": "absent"},
}

# The pinned-refusal table is itself an obligation. Parity proves the two
# engines agree; the pins prove they agree on the class the Rust reference
# chose. A table entry can therefore purchase a PASS by being deleted (the
# cell stops being checked) or by being loosened from a mapping to a bare
# class (the outcome and publication-evidence requirements silently
# disappear). Both edits leave every executed cell self-consistent, so only a
# check against the committed table can see them: the count and digest below
# are that table's committed identity, and the verifier refuses any run whose
# live table differs from them. Adding a pin is a deliberate act: update both
# constants in the same change that states the new obligation.
PINNED_REFUSAL_COUNT = 36
PINNED_REFUSALS_SHA256 = (
    "f1734d17ead1b70a4538333631fd8870079026df0f70d91931e8b392650611ab")


# ---------------------------------------------------------------------------
# Third axis: descriptor pressure (SOW-0028 wave-19.25 design section 14).
#
# The two axes above change the *target* an arm is given. Descriptor pressure
# changes the *environment*: the target stays valid and the operation stays
# valid, and what is under test is which operation notices that it cannot
# claim a descriptor and with what class it answers. That is a different
# failure mode -- a per-request probe invented `io`/`not_started` for arms
# that needed no descriptor at all, refused the releasing operation
# `reader.close`, and still left the process able to die inside the runtime --
# so it gets its own tables instead of being smuggled in as another path kind.
#
# A profile names the environment, not an expectation: the soft AND hard
# RLIMIT_NOFILE the launcher installs before execve, how many descriptors it
# additionally holds on a file outside the work directory, whether the session
# completed an initializing valid request first, and what the null device is.
# The expectation for a cell is `PINNED_PRESSURE_CLASSES`, fixed from the
# measured Rust reference (design section 7's tables) and anchored by
# committed count and digest exactly like `PINNED_REFUSALS`, so loosening a pin
# from a mapping to a bare class -- dropping its wedge, poller, or coverage
# obligation while keeping the class -- fails the gate instead of weakening it.
#
# Cells whose environment the launcher itself cannot build are host state and
# are never refusals or coverage. `host_floor` says the minimum band at which a
# cell is a product cell at all: the launcher needs 3 + held descriptors for
# itself, and the delivered dynamically linked Rust artifact needs three more
# for its loader (design section 13.1's measured exit 127).

PRESSURE_BANDS = (3, 4, 5, 6, 7, 8, 9, 10, 11, 12)
PRESSURE_HOLDS = (0, 3)
PRESSURE_RUNTIMES = ("before", "after")
PRESSURE_HOSTILE_NULL = ("fifo", "absent")
PRESSURE_GENEROUS_BAND = 64

PRESSURE_SUCCESS = ("RESULT", "RESULT")

# The band at which the reader.open/reader.close warm-up that establishes the
# "after" runtime state first completes on a fresh table (design section 7).
# It is the immutable-reader arm's own minimum, so the occupied-table floor is
# that plus the held count.
WARMUP_MINIMUM_BAND = 5

# The delivered Rust artifact is dynamically linked and its loader needs one
# free descriptor to start (measured: exit 127 at zero free, success at one,
# with and without the launcher's holds). Design section 13.1.
RUST_LOADER_FLOOR = 4

# The arms the pressure matrix defines. Each is a valid operation against a
# valid target, so a refusal can only come from the environment. The names are
# the pressure vocabulary of v4/cli/fd_pressure_harness.py, which owns the
# pressured launcher and the per-arm frame scripts; one authoritative
# implementation of each, composed here.
PRESSURE_ARMS = (
    "system.describe",              # claims no descriptor of its own
    "reader-open-close-immutable",  # one reader
    "reader-open-close-live",       # reader plus the coordination sidecar
    "direct.replace",               # the writer family
    "current.publish",              # the adapter publish family
    "current.publish.hostname",     # the resolver arm (design section 6)
    "maintenance.remove",           # the releasing family
    "validate(worker)",             # the colocated worker family
    "recovery.inspect(worker)",     # the colocated worker family
)

# Per-arm law, taken from design section 7's measured reference columns: the
# band at which the operation completes on a fresh table (Rust `minimum`, and
# the owned Go floor `go_minimum`), the class it must answer below that, the
# classes that may never appear for it, and the terms that make a cell coverage
# rather than an answer. Sections 11 and 13.3 record the writer and worker
# families as staying two bands behind the reference: the classes must match,
# the bands may not, and this table encodes that by pinning both numbers
# separately instead of declaring parity.
ARM_PRESSURE_LAW = {
    "system.describe": {
        "minimum": 4, "go_minimum": 3, "below": None,
        "never": [("io", "not_started")], "releasing": False,
    },
    "reader-open-close-immutable": {
        "minimum": 5, "go_minimum": 5, "below": ("io", "read_only_failure"),
        "never": [("io", "not_started")], "releasing": True,
    },
    "reader-open-close-live": {
        "minimum": 6, "go_minimum": 7, "below": ("io", "read_only_failure"),
        "never": [("io", "not_started")], "releasing": True,
    },
    "direct.replace": {
        "minimum": 6, "go_minimum": 8, "below": ("io", "not_started"),
        # Measured one band under the owned Go floor: the transaction has
        # begun and an open inside it yields EMFILE, so the writer answers
        # its own abort class with the cleanup facts. The reference completes
        # at 6 and has no measurement there to copy, so the class is pinned
        # from the owned build's behaviour rather than declared as parity
        # (design sections 11 and 13.3).
        "go_below_extra": [("transaction_aborted", "not_committed")],
        "never": [], "releasing": False,
    },
    "current.publish": {
        # The reference is not monotone across bands 5 and 6 (io/not_published
        # at 5, io/not_started at 6), so below the minimum either of the
        # handler's own classes is the answer: what is pinned is the
        # justification of the class, not the band ordering.
        "minimum": 7, "go_minimum": 8, "below": None,
        "below_any": [("io", "not_started"), ("io", "not_published")],
        "never": [], "releasing": False,
    },
    "current.publish.hostname": {
        # The same publish whose list carries a host name, so below the
        # publish minimum it is refused by the publish path's own opens and
        # answers the publish family's classes; design section 6 only decides
        # what happens once the operation reaches the resolver, and there the
        # answer is the reference class of a lookup that could not be
        # performed. Whether the poller was created is a separate assertion
        # (the arm is section 10's only exemption from poller freedom, and it
        # is exempt, not required).
        "minimum": 7, "go_minimum": 8, "below": None,
        "below_any": [("io", "not_started"), ("io", "not_published")],
        "success": ("input_format", "not_started"),
        "never": [], "releasing": False,
    },
    "maintenance.remove": {
        # The SDK removal class is unchanged by pressure and must never be a
        # probe invention. The entry removed comes from a maintenance.list in
        # the same session; a cell whose list is empty is not coverage
        # (sections 10 and 13.4).
        "minimum": None, "go_minimum": None, "below": None,
        "never": [("io", "not_started")], "releasing": True,
        "list_required": True,
    },
    "validate(worker)": {
        # A worker that cannot be resourced is the io class, not a protocol
        # conflict: conflict is the class of a worker that started and
        # misbehaved, and inventing it for an exhausted table is the fold this
        # wave removes.
        "minimum": 8, "go_minimum": 10, "below": ("io", "read_only_failure"),
        "never": [("conflict", None)], "releasing": False, "worker": True,
        # Below the reference's launchable band the handler's own pre-attempt
        # class is allowed (design section 13.1: no measured class to copy).
        "pre_reference": [("io", "not_started")],
    },
    "recovery.inspect(worker)": {
        "minimum": 7, "go_minimum": 9, "below": ("io", "read_only_failure"),
        "never": [("conflict", None)], "releasing": False, "worker": True,
        "pre_reference": [("io", "not_started")],
    },
}


def pressure_profile_name(band, held, runtime_state, null_device_state):
    """Stable identity of one pressure environment.

    The name carries every parameter so a report reader can reconstruct the
    environment from the cell alone; the band is zero-padded so the table
    prints in sweep order.
    """

    return f"b{band:02d}h{held}-{runtime_state}-{null_device_state}"


def _build_pressure_profiles():
    """Bands 3..12 x fresh and occupied tables x before and after runtime
    initialization, plus each hostile null device at a generous band."""

    profiles = {}
    for band in PRESSURE_BANDS:
        for held in PRESSURE_HOLDS:
            for runtime in PRESSURE_RUNTIMES:
                name = pressure_profile_name(band, held, runtime, "normal")
                profiles[name] = {
                    "name": name, "limit": band, "held": held,
                    "runtime_state": runtime, "null_device_state": "normal",
                    # The warm-up that defines the "after" state is itself
                    # the immutable-reader arm, so below its own minimum the
                    # state cannot be established for any engine: host state,
                    # per the same rule design section 13.2 applies to the
                    # launcher's held count.
                    "host_floor": max(3 + held,
                                      WARMUP_MINIMUM_BAND + held
                                      if runtime == "after" else 3 + held),
                    # The loader's need is one descriptor on top of what the
                    # process already holds, so it moves with the occupied
                    # count: measured as exit 127 with nothing free above
                    # stdio+holds, and success one band later.
                    "rust_host_floor": max(RUST_LOADER_FLOOR + held, 3 + held,
                                           WARMUP_MINIMUM_BAND + held
                                           if runtime == "after" else 0),
                    "expected_minimum_band": band,
                }
    for null_state in PRESSURE_HOSTILE_NULL:
        name = pressure_profile_name(PRESSURE_GENEROUS_BAND, 0, "before",
                                     null_state)
        profiles[name] = {
            "name": name, "limit": PRESSURE_GENEROUS_BAND, "held": 0,
            "runtime_state": "before", "null_device_state": null_state,
            "host_floor": 3, "rust_host_floor": 4,
            "expected_minimum_band": PRESSURE_GENEROUS_BAND,
        }
    return tuple(profiles[name] for name in sorted(profiles))


PRESSURE_PROFILES = _build_pressure_profiles()
PRESSURE_PROFILE_BY_NAME = {profile["name"]: profile
                            for profile in PRESSURE_PROFILES}

# Every environment is an obligation, named literally rather than derived from
# the table above: deleting a row of PRESSURE_PROFILES is the cheapest way to
# shrink this axis, so the verifier compares the two lists in both directions
# exactly as it does for path kinds and arms.
MANDATORY_PRESSURE_PROFILES = (
    "b03h0-after-normal", "b03h0-before-normal", "b03h3-after-normal",
    "b03h3-before-normal", "b04h0-after-normal", "b04h0-before-normal",
    "b04h3-after-normal", "b04h3-before-normal", "b05h0-after-normal",
    "b05h0-before-normal", "b05h3-after-normal", "b05h3-before-normal",
    "b06h0-after-normal", "b06h0-before-normal", "b06h3-after-normal",
    "b06h3-before-normal", "b07h0-after-normal", "b07h0-before-normal",
    "b07h3-after-normal", "b07h3-before-normal", "b08h0-after-normal",
    "b08h0-before-normal", "b08h3-after-normal", "b08h3-before-normal",
    "b09h0-after-normal", "b09h0-before-normal", "b09h3-after-normal",
    "b09h3-before-normal", "b10h0-after-normal", "b10h0-before-normal",
    "b10h3-after-normal", "b10h3-before-normal", "b11h0-after-normal",
    "b11h0-before-normal", "b11h3-after-normal", "b11h3-before-normal",
    "b12h0-after-normal", "b12h0-before-normal", "b12h3-after-normal",
    "b12h3-before-normal", "b64h0-before-absent", "b64h0-before-fifo",
)

# The routine subset, for the cost budget in AGENTS.md: the band boundaries of
# each family on a fresh table, one occupied-table probe, one after-init probe,
# and both hostile null devices. The full PRESSURE_PROFILES product runs at a
# milestone.
ROUTINE_PRESSURE_PROFILES = (
    "b03h0-before-normal", "b04h0-before-normal", "b05h0-before-normal",
    "b06h0-before-normal", "b07h0-before-normal", "b08h0-before-normal",
    "b10h0-before-normal", "b12h0-before-normal", "b08h3-before-normal",
    "b08h0-after-normal", "b64h0-before-fifo", "b64h0-before-absent",
)

# The axis a pressure sweep executed, recorded in its report.  The routine gate
# sweeps a 12-profile subset and a milestone sweeps all 42, so the report has to
# say which of the two it is: the committed pin table covers the whole product,
# and a record that does not name its own coverage cannot be read as evidence of
# the one it does not hold.  ``PRESSURE_FABRICATED_MODE`` labels the offline
# section ``--self-test`` builds from the tables without executing anything; a
# live sweep cannot produce it.
PRESSURE_MODE_ROUTINE = "routine"
PRESSURE_MODE_FULL = "full"
PRESSURE_FABRICATED_MODE = "fabricated"
PRESSURE_AXIS_MODES = (PRESSURE_MODE_ROUTINE, PRESSURE_MODE_FULL)

# The poller-free promise binds the Go engine, which owns a runtime network
# poller whose creation has no failure path: every arm except the resolver must
# answer with neither anon_inode:[eventpoll] nor anon_inode:[eventfd] in its
# table (design section 10). The resolver arm alone may hold the one pair the
# poller-readiness decision created deliberately.
# The hostile-null-device law (design section 9), defined once, here, where the
# committed class maps live. The cells replace /dev/null with a FIFO, or remove
# it, inside a private mount namespace, and what section 9 requires is an answer
# in both engines with the same class and no control file left behind: the
# descriptor-owning spawn refuses a node that is not the null character device
# rather than handing an undrained pipe to the child, so the worker arms answer
# the class section 9.4 pins for a worker that cannot be resourced, and the file
# arms are unaffected. The pressure harness grades the same table, so the sweep
# and the verifier cannot drift apart.
PRESSURE_HOSTILE_CLASSES = {
    "system.describe": {"fifo": ("RESULT", "RESULT"), "absent": ("RESULT", "RESULT")},
    "direct.replace": {"fifo": ("RESULT", "RESULT"), "absent": ("RESULT", "RESULT")},
    "reader-open-close-immutable": {"fifo": ("RESULT", "RESULT"),
                                    "absent": ("RESULT", "RESULT")},
    "reader-open-close-live": {"fifo": ("RESULT", "RESULT"),
                               "absent": ("RESULT", "RESULT")},
    "validate(worker)": {"fifo": ("io", "read_only_failure"),
                         "absent": ("io", "read_only_failure")},
    "recovery.inspect(worker)": {"fifo": ("io", "read_only_failure"),
                                 "absent": ("io", "read_only_failure")},
    "current.publish": {"fifo": ("RESULT", "RESULT"), "absent": ("RESULT", "RESULT")},
    "current.publish.hostname": {"fifo": ("input_format", "not_started"),
                                 "absent": ("input_format", "not_started")},
    # Section 13.4: this arm has no producer of a listable artifact, so the
    # hostile cells are the blocked cells, which are reported and never scored.
    "maintenance.remove": {"fifo": None, "absent": None},
}

BLOCKED_NAME = "blocked"          # the launcher's section 13.4 verdict
HOST_UNSUPPORTED_NAME = "host-unsupported"   # the launcher's section 13.2 verdict

RESOLVER_PRESSURE_ARM = "current.publish.hostname"
POLLER_FREE_ARMS = tuple(a for a in PRESSURE_ARMS if a != RESOLVER_PRESSURE_ARM)

# poller_demand_free mirrors the engine's constant in
# v4/go/internal/calleropen/poller_readiness.go: the poller allocates two
# descriptors and the resolver's datagram socket takes a third, so four free
# slots cover the creation plus the request that provokes it. The launcher
# hands over 3 stdio + held descriptors and nothing else, which is what makes
# the authorized boundary computable from the cell.
POLLER_DEMAND_FREE = 4
POLLER_STDIO_FLOOR = 3


def poller_authorized(profile):
    """Whether the engine's poller-readiness decision authorized the poller."""

    record = PRESSURE_PROFILE_BY_NAME[profile]
    return (record["limit"] - (POLLER_STDIO_FLOOR + record["held"])
            >= POLLER_DEMAND_FREE)


def pinned_pressure_expectation(arm, profile):
    """One cell's obligation, derived from the arm's law and the environment.

    Returned as a mapping rather than a bare class so the obligation carries
    the wedge, poller, and coverage terms with the class: a pin that names only
    a class cannot fail a cell that answered that class while wedging, leaking
    the poller, or never reaching an open.
    """

    law = ARM_PRESSURE_LAW[arm]
    record = PRESSURE_PROFILE_BY_NAME[profile]
    null_state = record.get("null_device_state", "normal")
    if null_state != "normal":
        # The hostile cells are not a band question: at a generous band every
        # arm can complete, and what is under test is the spawn's and the
        # open's handling of a /dev/null that is not the null device. The class
        # is therefore the section 9 table, and a None entry is section 13.4's
        # blocked cell rather than an expectation.
        want = PRESSURE_HOSTILE_CLASSES[arm][null_state]
        state = "hostile" if want is not None else "blocked"
        return {
            "data_code": None if want is None else want[0],
            "outcome": None if want is None else want[1],
            "band_state": state,
            "below": (list(law["below"]) if law.get("below") else None),
            "success": list(law.get("success", PRESSURE_SUCCESS)),
            "hostile_class": (list(want) if want is not None else None),
            "go_below_extra": [list(item) for item in law.get("go_below_extra", [])],
            "pre_reference": [list(item) for item in law.get("pre_reference", [])],
            "below_any": [list(item) for item in law.get("below_any", [])],
            "never": [list(item) for item in law.get("never", [])],
            "wedge": "never",
            "poller": ("authorized-pair-or-none" if arm == RESOLVER_PRESSURE_ARM
                       else "absent"),
            # An empty list for the blocked arm means "no removal was possible",
            # which is the blocked shape, not coverage (section 14).
            "vacuous": "fail",
            "host_unsupported": ("allowed" if record["limit"] < record["host_floor"]
                                 else "forbidden"),
            "rust_host_unsupported": ("allowed" if record["limit"]
                                      < record["rust_host_floor"] else "forbidden"),
            "releasing": bool(law.get("releasing")),
            "list_required": bool(law.get("list_required")),
            "minimum_band": law["minimum"],
            "go_minimum_band": law["go_minimum"],
        }
    if law.get("anywhere"):
        want, state = law["anywhere"], "band-independent"
    elif law["minimum"] is None:
        want, state = None, "factual"
    else:
        minimum = law["minimum"] + record["held"]
        if record["limit"] >= minimum:
            want, state = law.get("success", PRESSURE_SUCCESS), "at-or-above-minimum"
        else:
            state = "below-minimum"
            if law.get("below"):
                want = law["below"]
            elif law.get("below_any"):
                want = law["below_any"][0]
            else:
                want = None
    return {
        "data_code": None if want is None else want[0],
        "outcome": None if want is None else want[1],
        # band_state records which side of the arm minimum the
        # environment sits on. It is stored, not re-derived at verdict
        # time: without it a pin would accept success or the
        # below-minimum class in either band, so a band regression
        # stayed invisible while the class still matched.
        "band_state": state,
        "hostile_class": None,
        # The exhaustion class is a property of the arm, not of the band the
        # reference minimum happens to sit in, so it is stored here. Without
        # it an engine judged below its own floor would be read as owing the
        # class the *other* engine's band_state selected, which for the writer
        # family is a success.
        "below": (list(law["below"]) if law.get("below") else None),
        # What the arm owes once it is at or above its own minimum. Most arms
        # complete; the host-name arm's completion is the resolver class,
        # because its list can never resolve in the harness environment.
        "success": list(law.get("success", PRESSURE_SUCCESS)),
        "go_below_extra": [list(item) for item in law.get("go_below_extra", [])],
        # Design section 13.1: below the band the dynamic loader can start in
        # there is no measured reference class to copy, so the arm's own
        # pre-attempt class is allowed. The never-lists and the poller
        # assertion keep binding, and the obligation is unchanged from the
        # first band where the reference exists.
        "pre_reference": [list(item) for item in law.get("pre_reference", [])],
        "below_any": [list(item) for item in law.get("below_any", [])],
        "never": [list(item) for item in law.get("never", [])],
        "wedge": "never",
        "poller": ("authorized-pair-or-none" if arm == RESOLVER_PRESSURE_ARM
                   else "absent"),
        "vacuous": "fail",
        "host_unsupported": ("allowed" if record["limit"] < record["host_floor"]
                             else "forbidden"),
        "rust_host_unsupported": ("allowed" if record["limit"]
                                  < record["rust_host_floor"] else "forbidden"),
        "releasing": bool(law.get("releasing")),
        "list_required": bool(law.get("list_required")),
        "minimum_band": law["minimum"],
        "go_minimum_band": law["go_minimum"],
    }


def _build_pinned_pressure_classes():
    return {(arm, profile["name"]): pinned_pressure_expectation(
                arm, profile["name"])
            for arm in PRESSURE_ARMS for profile in PRESSURE_PROFILES}


PINNED_PRESSURE_CLASSES = _build_pinned_pressure_classes()

# The committed identity of the pressure table, exactly like the pinned-refusal
# anchors: deleting a cell, adding one, or loosening one's mapping changes the
# digest, and the count catches a shrink that a re-derivation would hide.
# Literal, not derived: a count computed from the swept tables would stay
# self-consistent when an arm or a profile row was deleted, which is the
# shrink this anchor exists to catch.
PINNED_PRESSURE_CLASS_COUNT = 378
PINNED_PRESSURE_CLASSES_SHA256 = (
    "314d8be5618e9775d9dd386ad6d7e521ff27ee9ba63d7c9501746de941ad1456")


def pinned_pressure_fingerprint(table=None):
    """The digest of the pressure-pinned table in a stable encoding.

    JSON with sorted keys and no optional whitespace over the
    ``((arm, profile), value)`` pairs, so a bare class and a mapping that
    requires the same class cannot digest alike: the encoding records the shape
    of the obligation, not only its class.
    """

    source = PINNED_PRESSURE_CLASSES if table is None else table
    encoded = json.dumps([[[arm, profile], value]
                          for (arm, profile), value in sorted(source.items())])
    return hashlib.sha256(encoded.encode("utf-8")).hexdigest()


def pressure_cell_from_records(arm, profile, records, attempts):
    """Fold the pressured launcher's per-engine records into one cell.

    ``records`` maps engine -> the record v4/cli/fd_pressure_harness.py
    produced for that (arm, environment, attempt); ``attempts`` is how many
    runs the cell had. The launcher, the per-arm frame scripts, and the
    descriptor-table sampler stay in that file (one authoritative
    implementation of each); this function only translates them into the
    shape the parity verifier compares.
    """

    record = PRESSURE_PROFILE_BY_NAME[profile]
    cell = {"arm": arm, "profile": profile,
            "band": record["limit"], "held": record["held"],
            "runtime": record["runtime_state"],
            "null_device": record["null_device_state"],
            "attempts": attempts, "agreed": True, "hung": False,
            "band_gap": False,
            "flaky": False, "vacuous": False, "list_empty": False,
            "close_refused": False, "go_poller": [None, None]}
    for engine in ("go", "rust"):
        source = records.get(engine)
        if source is None:
            cell[engine] = {"kind": "missing"}
            continue
        answer = source.get("answer")
        verdict = source.get("verdict")
        kind, transport_code, data_code, outcome = None, None, None, None
        exit_class = None
        if verdict == BLOCKED_NAME:
            # Section 13.4: an arm with no removable entry is reported blocked.
            # It answered nothing, and that is the honest outcome, so it must
            # not be folded into the wedge term.
            kind, exit_class = "blocked", None
        elif verdict == HOST_UNSUPPORTED_NAME:
            # Sections 13.1 and 13.2: the launcher could not build this
            # environment (its own descriptor floor, the dynamic loader, or the
            # warm-up the "after" state needs). No product code ran, so there
            # is no answer to read -- that is host state, never a wedge.
            kind, exit_class = "host", "host"
        elif verdict == "wedge" or answer is None:
            kind, exit_class = "no-answer", None
        else:
            parsed = json.loads(answer) if isinstance(answer, str) else answer
            if isinstance(parsed, dict) and parsed.get("error"):
                error = parsed["error"]
                data = error.get("data") or {}
                kind, transport_code = "error", error.get("code")
                data_code, outcome = data.get("code"), data.get("outcome")
                exit_class = "refused"
            elif isinstance(parsed, dict) and "result" in parsed:
                kind, transport_code, data_code, outcome = (
                    "answered", 0, "RESULT", "RESULT")
                exit_class = "success"
            else:
                kind = "no-answer"
        # The launcher's three no-answer verdicts stay distinct here: a host
        # state (sections 13.1-13.2) or a blocked arm (section 13.4) answered
        # nothing for a stated reason, and folding either into "no-answer"
        # would report an unbuildable environment or an unproducible artifact
        # as an engine wedge.
        cell[engine] = {"kind": "answered" if kind in ("answered", "error")
                        else "host" if kind == "host"
                        else "blocked" if kind == "blocked"
                        else "no-answer",
                        "transport_code": transport_code,
                        "data_code": data_code, "outcome": outcome,
                        "result": kind == "answered",
                        "exit_class": ("host" if kind == "host" else
                                       "success" if kind == "answered" else
                                       "refused" if kind == "error" else "none"),
                        "verdict": verdict, "why": source.get("why")}
        if verdict == "disagreement":
            cell["flaky"] = True
        if kind == "no-answer":
            cell["hung"] = True
        if source.get("close_class") not in (None, ("result", "RESULT", "RESULT")):
            cell["close_refused"] = True
        if verdict == "blocked" or source.get("verdict") == "blocked":
            cell["list_empty"] = True
        if verdict == "wrong-class" and kind == "error":
            # A "valid" request refused by validation before any open is not
            # coverage; the launcher's own below-minimum expectations are
            # graded by the pin, so a wrong class that is also a validation
            # refusal is reported as vacuous and fails.
            if data_code in ("invalid_argument", "invalid_params",
                             "not_supported"):
                cell["vacuous"] = True
    if (cell["go"].get("kind") == "answered"
            and cell["rust"].get("kind") == "answered"):
        cell["agreed"] = _pressure_answer(cell, "go") == _pressure_answer(
            cell, "rust")
    poller = records.get("go")
    if poller is not None and poller.get("poller_ever") is not None:
        cell["go_poller"] = list(poller["poller_ever"])
    return cell


def run_pressure_sweep(go, rust, fixture, work, profiles, mode, runs=2,
                       jobs=1):
    """Execute the pressure axis with the launcher of design section 10.

    One fresh process per cell per engine, soft and hard RLIMIT_NOFILE set by
    the launcher before execve, the table pre-occupied by descriptors held on a
    file outside the work directory, everything nice'd, an 8 s per-cell budget
    so a cell that needs the whole budget is a wedge, the child's descriptor
    table sampled throughout, stderr scanned for the runtime's fatal tokens, and
    every cell executed at least twice with any disagreement failing. All of
    that lives in v4/cli/fd_pressure_harness.py, which owns the launcher; this
    import is deferred because that module imports this one for its own
    materialization helpers, and neither side may duplicate the other.

    ``mode`` is the name of the axis the caller asked the operator to pay for
    (``routine`` or ``full``) and is recorded verbatim in the report.  It is a
    required positional argument because a section that cannot say which of the
    two it swept is the defect this closes, and refusing before the first cell
    costs nothing against the 1,071 s the full axis measured on this host.
    """

    if mode not in PRESSURE_AXIS_MODES:
        raise SystemExit(
            f"--pressure must sweep the axis named "
            f"{' or '.join(repr(name) for name in PRESSURE_AXIS_MODES)}, "
            f"not {mode!r}")

    import fd_pressure_harness  # noqa: PLC0415  (deferred: circular by design)

    bins = {"go": go, "rust": rust}
    # Read into distinct names first: a class body resolves a name it also
    # binds against the class namespace and then the module globals, so it
    # cannot see the enclosing function's parameter of the same name.
    go_dir = os.path.dirname(os.path.abspath(go))
    rust_dir = os.path.dirname(os.path.abspath(rust))
    fixture_bin = os.path.abspath(fixture)
    scratch_dir = os.path.join(work, "fd-pressure")

    class _LauncherArgs:           # the harness CLI's shape, filled directly
        go_bin = go_dir
        rust_bin = rust_dir
        fixture = fixture_bin
        scratch = scratch_dir
        keep_work = True

    _, contexts, hold_path = fd_pressure_harness.prepare_ctx(_LauncherArgs(),
                                                             bins,
                                                             slots=max(1, jobs))
    jobs = max(1, min(int(jobs), len(PRESSURE_ARMS) * len(profiles)))
    # Each slot is an independent materialization prepared by the launcher
    # owner for exactly this purpose ("so slots can be swept in parallel
    # without one cell ever observing another's targets",
    # fd_pressure_harness.prepare_ctx), and the held-descriptor target is a
    # read-only file every cell may open concurrently.
    #
    # A context is LEASED through a queue, never chosen by `index % len(
    # contexts)`.  Cell durations here differ by orders of magnitude (a
    # host-state cell answers in milliseconds; a pressured cell runs to its
    # 8 s ceiling and then repeats), so an index-derived assignment can hand
    # the same work directory to two live workers -- the second one then
    # removes a target the first is still using and the sweep dies inside
    # materialize_targets() with a FileNotFoundError that looks like a
    # product defect.  The queue makes exclusivity a property of the
    # schedule rather than of the arithmetic.
    cells = [None] * (len(PRESSURE_ARMS) * len(profiles))
    failures_box = [0]
    lock = threading.Lock()
    leases: "queue.Queue" = queue.Queue()
    for ctx in contexts[:jobs]:
        leases.put(ctx)

    def one_cell(index, arm, profile):
        ctx = leases.get()
        try:
            _one_pressure_cell(index, arm, profile, ctx)
        finally:
            leases.put(ctx)

    def _one_pressure_cell(index, arm, profile, ctx):
        record = PRESSURE_PROFILE_BY_NAME[profile]
        attempts = max(2, runs)
        per_engine = {}
        for engine in ("go", "rust"):
            # Host state (design sections 13.1-13.2) is decided by the
            # launcher inside run_cell, which owns that classification and
            # answers it without starting a doomed process; the sweep
            # reads the verdict back rather than duplicating the rule.
            best = None
            for _attempt in range(attempts):
                candidate = fd_pressure_harness.run_cell(
                    engine, bins, ctx, hold_path, arm, record["limit"],
                    record["held"], record["runtime_state"],
                    record["null_device_state"])
                if best is None or (candidate.get("verdict") == "pass"):
                    best = candidate
                if candidate.get("verdict") != best.get("verdict"):
                    best["verdict"] = "disagreement"
                    best["why"] = "cell disagreed across runs"
            per_engine[engine] = best
        cell = pressure_cell_from_records(arm, profile, per_engine, attempts)
        pin = PINNED_PRESSURE_CLASSES.get((arm, profile))
        failed = 0
        if pin is None:
            failed = 1
        else:
            _, cell["band_gap"] = _pressure_cell_divergence(pin, cell)
            for engine in ("go", "rust"):
                problems = pressure_cell_problems(cell, pin, engine)
                if problems:
                    failed = 1
                    cell.setdefault("problems", []).extend(problems)
                    break
        cells[index] = cell
        if failed:
            with lock:
                failures_box[0] += 1

    jobs_list = [(index, arm, profile)
                 for index, (arm, profile) in enumerate(
                     (arm, profile) for arm in PRESSURE_ARMS for profile in profiles)]
    started = time.monotonic()
    # The main grid's elapsed_seconds never covers this axis, so the section
    # times itself: measured on this host, the full product is 1,071 s of
    # pressured processes against a 34 s grid, and a record that hides that
    # difference makes a milestone step look like a routine one.
    if jobs == 1:
        for index, arm, profile in jobs_list:
            one_cell(index, arm, profile)
    else:
        # A cell owns two pressured child processes and its own
        # materialization, so the threads spend their time inside wait(2)
        # and the sweep is process-bound, not interpreter-bound.
        threads = []
        pending = list(jobs_list)

        def worker():
            while True:
                with lock:
                    if not pending:
                        return
                    index, arm, profile = pending.pop(0)
                one_cell(index, arm, profile)

        for _ in range(jobs):
            thread = threading.Thread(target=worker, daemon=True)
            thread.start()
            threads.append(thread)
        for thread in threads:
            thread.join()
        missing = [jobs_list[i] for i, cell in enumerate(cells) if cell is None]
        if missing:
            # A worker that died with its exception swallowed by Thread would
            # otherwise be a pressure section with fewer cells than the axis
            # owes -- which verify_pressure_report reports as a coverage
            # problem instead of the harness failure it actually is.
            raise SystemExit(
                f"pressure workers did not produce {len(missing)} cell(s): "
                f"{missing[:4]}")
    failures = failures_box[0]
    elapsed = round(time.monotonic() - started, 3)
    rollup = pressure_rollup(cells, profiles)
    rollup["failed"] = failures
    return {"mode": mode, "cells": cells, "runs": max(2, runs),
            "elapsed_seconds": elapsed, **rollup}


def expected_pressure_cells(profiles=None):
    """Every (arm, profile) cell the pressure axis owes, derived here."""

    chosen = (MANDATORY_PRESSURE_PROFILES if profiles is None else tuple(profiles))
    return [(arm, profile) for arm in PRESSURE_ARMS for profile in chosen]


def pressure_table_integrity_problems():
    """Every way the pressure axis can be shrunk without being noticed.

    Checked against the committed constants rather than the report, so this is
    the term that turns a deleted profile, a deleted arm, a deleted pin, or a
    loosened pin into a gate failure instead of a smaller gate.
    """
    problems = []
    names = [profile["name"] for profile in PRESSURE_PROFILES]
    for entry in MANDATORY_PRESSURE_PROFILES:
        if entry not in names:
            problems.append(
                f"pressure profile {entry!r} is an obligation but is missing "
                f"from PRESSURE_PROFILES; deleting an environment is not a "
                f"way to pass")
    for entry in names:
        if entry not in MANDATORY_PRESSURE_PROFILES:
            problems.append(
                f"pressure profile {entry!r} is swept but is not an "
                f"obligation, so deleting it would shrink the gate unnoticed; "
                f"name it in MANDATORY_PRESSURE_PROFILES")
    if not ROUTINE_PRESSURE_PROFILES:
        problems.append("the routine pressure subset is empty; a gate that "
                        "sweeps no pressure profile verifies nothing")
    for entry in ROUTINE_PRESSURE_PROFILES:
        if entry not in names:
            problems.append(
                f"routine pressure profile {entry!r} is not in "
                f"PRESSURE_PROFILES; the routine subset must be a subset")
    if len(PINNED_PRESSURE_CLASSES) != PINNED_PRESSURE_CLASS_COUNT:
        problems.append(
            f"the pinned-pressure-class table has {len(PINNED_PRESSURE_CLASSES)} "
            f"entries, not the committed {PINNED_PRESSURE_CLASS_COUNT}; a cell "
            f"cannot be deleted to make the gate pass")
    digest = pinned_pressure_fingerprint()
    if digest != PINNED_PRESSURE_CLASSES_SHA256:
        problems.append(
            f"the pinned-pressure-class table digests to {digest}, not the "
            f"committed {PINNED_PRESSURE_CLASSES_SHA256}; deleting a cell, "
            f"adding one, or loosening one from a mapping to a bare class "
            f"(dropping its wedge, poller, or coverage obligation) all read "
            f"as tampering with the authority this gate is anchored to")
    for arm in PRESSURE_ARMS:
        if arm not in ARM_PRESSURE_LAW:
            problems.append(f"pressure arm {arm!r} has no law entry")
    for arm in ARM_PRESSURE_LAW:
        if arm not in PRESSURE_ARMS:
            problems.append(
                f"pressure law entry {arm!r} is not a swept arm; a law without "
                f"an arm cannot be exercised")
    for (arm, profile) in expected_pressure_cells():
        if (arm, profile) not in PINNED_PRESSURE_CLASSES:
            problems.append(
                f"pressure cell ({arm!r}, {profile!r}) is owed by the derived "
                f"product but has no pinned class")
    return problems


def _pressure_answer(cell, engine):
    """The (data_code, outcome) one engine answered in a pressure cell."""

    record = cell.get(engine) or {}
    if record.get("kind") == "answered" and record.get("result"):
        return PRESSURE_SUCCESS
    return (record.get("data_code"), record.get("outcome"))


def _pressure_band_state(pin, engine, profile):
    """Which side of *this engine's* arm minimum the cell sits on.

    ``pinned_pressure_expectation`` stores ``band_state`` from the Rust
    reference minimum, which design section 7 makes the authority. Go carries
    its own measured floor separately: design sections 11 and 13.3 record the
    writer and worker families as staying behind the reference on purpose, so
    the table pins both numbers instead of declaring band parity. Re-deriving
    the state against the engine's own minimum is what lets a Go cell that
    completes two bands early read as a success while a Go cell that lost a
    band still reads as a regression. A cell whose arm has no minimum at all
    (a factual or band-independent arm) keeps the stored state, which is not
    band-shaped.
    """

    if pin["band_state"] not in ("at-or-above-minimum", "below-minimum"):
        # hostile, blocked, factual and band-independent cells are not a band
        # question, so the stored state is the answer.
        return pin["band_state"]
    minimum = (pin["minimum_band"] if engine == "rust"
               else pin["go_minimum_band"])
    if minimum is None:
        return pin["band_state"]
    record = PRESSURE_PROFILE_BY_NAME[profile]
    return ("at-or-above-minimum"
            if record["limit"] >= minimum + record["held"]
            else "below-minimum")


def _pressure_expected_answer(pin, engine, profile):
    """The class one engine owes in one cell, from its own arm minimum.

    The obligation is symmetric across the engines only in its *classes*: the
    writer and worker families sit behind the Rust reference bands on purpose
    (design sections 11 and 13.3), so each engine is judged against the minimum
    the table measured for it. This is the single derivation of that answer;
    both the verdict and the fabricated report use it, so a report cannot be
    built from one rule and judged by another.
    """

    state = _pressure_band_state(pin, engine, profile)
    if state == "hostile":
        # Section 9's class, identical in both engines: the hostile cells exist
        # to prove the two engines react to a poisoned /dev/null the same way.
        return tuple(pin["hostile_class"])
    if state == "blocked":
        return None
    if state == "at-or-above-minimum":
        # The completion class is the arm's own: the publish family completes,
        # and the host-name arm, whose list can never resolve in this
        # environment, completes into the resolver's reference class.
        return tuple(pin["success"])
    if state == "below-minimum":
        if pin["below"] is not None:
            return tuple(pin["below"])
        if pin["below_any"]:
            return tuple(pin["below_any"][0])
        # An arm whose reference names no exhaustion class: the operation
        # claims no descriptor of its own, so the honest answer in a band the
        # loader can still start in is the completion.
        return PRESSURE_SUCCESS
    return tuple(pin["success"])


def _pressure_divergence_is_band_gap(pin, cell, go_answer, rust_answer):
    """Whether a cross-engine difference is the gap design 13.3 expects.

    In the band window between the Rust minimum and the owned-Go minimum one
    engine completes and the other answers the exhaustion class of the same
    arm. That pair is the expected result of a sweep, not a parity failure.
    Every other difference is: two refusals naming different classes, a
    success beside a class that is not this arm's exhaustion refusal, or a
    difference while both engines stand on the same side of their own minimum.
    """

    if go_answer == rust_answer:
        return True
    states = {engine: _pressure_band_state(pin, engine, cell["profile"])
              for engine in ("go", "rust")}
    answers = {"go": go_answer, "rust": rust_answer}
    if sorted(states.values()) == ["below-minimum", "below-minimum"]:
        # Neither engine stands on a band where it owes a completion, so the
        # obligation is each engine's own pinned below-minimum class and there
        # is no cross-engine equality to assert. The per-engine verdict applies
        # the same list, so this admits nothing the table does not already pin.
        return all(answers[engine] in _pressure_accepted_answers(
            pin, engine, cell["profile"]) for engine in ("go", "rust"))
    if sorted(states.values()) != ["at-or-above-minimum", "below-minimum"]:
        return False
    winner = min(("go", "rust"), key=lambda engine: states[engine]
                 != "at-or-above-minimum")
    loser = "rust" if winner == "go" else "go"
    law = ARM_PRESSURE_LAW[cell["arm"]]
    if answers[winner] != tuple(law.get("success", PRESSURE_SUCCESS)):
        return False
    allowed = [tuple(law["below"])] if law.get("below") else []
    allowed += [tuple(item) for item in law.get("below_any", [])]
    if loser == "go":
        allowed += [tuple(item) for item in law.get("go_below_extra", [])]
    allowed.append(tuple(law.get("success", PRESSURE_SUCCESS)))
    allowed.append((None, None))
    return answers[loser] in allowed


def _pressure_cell_divergence(pin, cell):
    """Derive ``(divergent, band_gap)`` for one executed pressure cell.

    One derivation, called by the sweep that stamps the cell, by the rollup
    that counts it, and by the verifier that judges a report: the pair comes
    from the cell's own recorded answers and the committed pin, never from a
    flag a report carries. Two engines that both answered owe either the same
    class or the band gap design section 13.3 records; anything else is a
    divergence, so a band gap cannot be claimed by relabelling a cell whose
    classes the table does not support for each engine's own outcome.
    """

    for engine in ("go", "rust"):
        if (cell.get(engine) or {}).get("kind") != "answered":
            # A host-unsupported or blocked side answered nothing to compare
            # (design sections 13.2 and 13.4), and a missing answer is the
            # wedge term's business, not a class question.
            return False, False
    go_answer, rust_answer = (_pressure_answer(cell, "go"),
                              _pressure_answer(cell, "rust"))
    if go_answer == rust_answer:
        return False, False
    if not isinstance(pin, dict):
        # Nothing supports an allowance for a cell the table does not define,
        # so a difference there stays a divergence (the missing pin is itself a
        # verdict problem, named by verify_pressure_report).
        return True, False
    if _pressure_divergence_is_band_gap(pin, cell, go_answer, rust_answer):
        return False, True
    return True, False


def _pressure_accepted_answers(pin, engine, profile):
    """Every class this engine may answer below its own arm minimum.

    One derivation, used by the per-engine verdict and by the cross-engine
    divergence term: below both minima neither engine owes the other a class,
    because the committed table pins each engine's own exhaustion answer.
    """

    want = _pressure_expected_answer(pin, engine, profile)
    accepted = ([want] if want is not None else [])
    accepted += [tuple(item) for item in pin.get("below_any", [])]
    if engine == "go":
        accepted += [tuple(item) for item in pin.get("go_below_extra", [])]
    record = PRESSURE_PROFILE_BY_NAME[profile]
    if record["limit"] < RUST_LOADER_FLOOR + record["held"]:
        accepted += [tuple(item) for item in pin.get("pre_reference", [])]
    return list(dict.fromkeys(accepted))


def pressure_cell_problems(cell, pin, engine):
    """Verdict problems for one engine's half of one pressure cell.

    The order is the one the parity report already uses: answered at all (a
    wedge is its own verdict, never a class), transport code, data.code,
    data.outcome, exit-status class, and the descriptor-set assertion. Host
    state is consulted first, because a cell whose environment the launcher
    could not build is neither a refusal nor coverage (design sections 10 and
    13.2), and a vacuous cell -- one that never reached a real open -- is not
    coverage either (design section 14).
    """

    problems = []
    where = f"pressure cell ({cell['arm']}, {cell['profile']}, {engine})"
    record = cell.get(engine) or {}
    answered = record.get("kind") in ("answered", "host", "blocked")
    if record.get("kind") == "host":
        allowed = (pin["host_unsupported"] if engine == "go"
                   else pin["rust_host_unsupported"])
        if allowed != "allowed":
            problems.append(
                f"{where}: reported host state where the environment was "
                f"buildable (host_floor allows {allowed!r}); host state is "
                f"never a substitute for an answer")
        return problems
    if not answered:
        problems.append(f"{where}: {pin['wedge']} rule violated -- the arm "
                        f"never answered within the cell budget (a wedge is a "
                        f"failure, not a class)")
        return problems
    if record.get("transport_code") not in (None, PRODUCT_ERROR, 0):
        problems.append(f"{where}: unexpected transport code "
                        f"{record.get('transport_code')!r}")
    if (record.get("exit_class") not in ("success", "refused", "host")
            and record.get("kind") != "blocked"):
        # A blocked cell (section 13.4) answered nothing by design, so it has
        # no exit-status class to name; the wedge term above already decided
        # that this is the honest outcome rather than a missing answer.
        problems.append(f"{where}: missing exit-status class")
    if pin["releasing"] and cell.get("close_refused"):
        problems.append(
            f"{where}: the releasing operation was refused after a successful "
            f"open (design section 7: reader.close is never refused)")
    if cell.get("list_blocked"):
        if cell.get("agreed"):
            problems.append(
                f"{where}: claimed coverage although maintenance.list reported "
                f"no removable entry; a removal that removed nothing is not "
                f"coverage, it is the blocked cell of design section 13.4 and "
                f"must be reported as such")
        return problems
    if cell.get("vacuous"):
        problems.append(
            f"{where}: vacuous -- the request was refused by validation before "
            f"any open, which is not coverage (design section 14)")
    for banned in pin["never"]:
        if _pressure_answer(cell, engine) == tuple(banned):
            problems.append(
                f"{where}: answered {tuple(banned)}, a class this arm may never "
                f"produce (design section 7)")
    got = _pressure_answer(cell, engine)
    want = _pressure_expected_answer(pin, engine, cell["profile"])
    state = _pressure_band_state(pin, engine, cell["profile"])
    if state == "blocked":
        # Section 13.4: this arm has no producer of a removable entry, so the
        # honest verdict for the cell is blocked. An engine that reports a real
        # answer here either removed something it cannot remove or is counting
        # a refusal as coverage; both are the failure this term exists for. The
        # vacuous and list_blocked terms above catch the softer variants.
        if record.get("kind") == "answered" and record.get("result"):
            problems.append(
                f"{where}: reported coverage for an arm design section 13.4 "
                f"records as blocked (no removable entry); a removal that "
                f"removed nothing is not coverage")
        return problems
    if state == "hostile":
        # Section 9 grades the hostile null-device cells: the answer must be
        # the pinned class and it must be the same class in both engines. The
        # band rules below do not apply (the cells run at a generous band), but
        # the poller assertion at the end of this function still does, so the
        # result is not returned early.
        if got != want:
            problems.append(
                f"{where}: hostile null device answered {got}, want {want}; the "
                f"class section 9 pins is the same in both engines")
    minimum = (pin["minimum_band"] if engine == "rust"
               else pin["go_minimum_band"])
    profile_record = PRESSURE_PROFILE_BY_NAME[cell["profile"]]
    extra = (" or one of " + str(pin["below_any"])) if pin.get("below_any") else ""
    if state == "at-or-above-minimum" and got != want:
        problems.append(
            f"{where}: answered {got} at or above the arm minimum band "
            f"{minimum} plus the held count; a valid operation must complete "
            f"there (design section 7)")
    elif state == "below-minimum":
        accepted = _pressure_accepted_answers(pin, engine, cell["profile"])
        if got not in accepted:
            problems.append(
                f"{where}: answered {got} below the arm minimum band {minimum}; "
                f"the reference pins {want}{extra} (design section 7)")
    elif state == "band-independent" and got != want:
        problems.append(
            f"{where}: answered {got}; the reference pins {want} in every band "
            f"(design sections 6 and 7)")
    if engine == "go":
        counts = cell.get("go_poller")
        if pin["poller"] == "absent":
            if counts != [0, 0]:
                problems.append(
                    f"{where}: the process held {counts[0]} eventpoll and "
                    f"{counts[1]} eventfd descriptors; every arm except the "
                    f"resolver must be poller-free (design section 10)")
        elif counts not in ([0, 0], [1, 1]):
            problems.append(
                f"{where}: the resolver's poller is {counts}; the authorized "
                f"state is the deliberate pair (1, 1) or, when the readiness "
                f"decision refused it, none (design sections 6 and 10)")
    return problems


def pressure_rollup(cells, profiles=None):
    """Counts the verifier and the report reader need beside each other."""

    executed = {(cell.get("arm"), cell.get("profile")) for cell in cells}
    # The obligation follows the profiles this section swept (defaulting
    # to the mandatory list), so a routine sweep is judged against the
    # routine obligation and a milestone sweep against the whole product;
    # neither can quietly report fewer cells than it owes.
    swept = tuple(profiles) if profiles else MANDATORY_PRESSURE_PROFILES
    expected = set(expected_pressure_cells(swept))
    coverage = [cell for cell in cells
                if not cell.get("list_blocked") and not cell.get("vacuous")]
    return {
        "arms": list(PRESSURE_ARMS),
        "profiles": list(swept),
        "mandatory_profiles": list(MANDATORY_PRESSURE_PROFILES),
        "cells_expected": len(expected),
        "cells_executed": len({(cell.get("arm"), cell.get("profile"))
                               for cell in coverage} & expected),
        "cells_missing": sorted(f"{arm}/{profile}"
                                for (arm, profile) in (expected - executed)),
        "pinned_table_count": len(PINNED_PRESSURE_CLASSES),
        "pinned_table_sha256": pinned_pressure_fingerprint(),
        "agreements": sum(1 for cell in cells if cell.get("agreed")),
        # The band gap design section 13.3 expects is reported as a band gap,
        # never scored as a divergence: the divergence term stays what the
        # verdict fails on, and a cell only leaves it when the pinned classes
        # of each engine's own answer support the gap (_pressure_cell_
        # divergence, derived from the table rather than from the cell).
        "divergences": sum(1 for cell in cells
                           if _pressure_cell_divergence(
                               PINNED_PRESSURE_CLASSES.get(
                                   (cell.get("arm"), cell.get("profile"))),
                               cell)[0]),
        "band_gap": sum(1 for cell in cells
                        if _pressure_cell_divergence(
                            PINNED_PRESSURE_CLASSES.get(
                                (cell.get("arm"), cell.get("profile"))),
                            cell)[1]),
        "hangs": sum(1 for cell in cells if cell.get("hung")),
        "flaky": sum(1 for cell in cells if cell.get("flaky")),
        "vacuous": sum(1 for cell in cells if cell.get("vacuous")),
        "blocked": sum(1 for cell in cells if cell.get("list_blocked")),
        "host_state": sum(1 for cell in cells
                          if (cell.get("go") or {}).get("kind") == "host"
                          or (cell.get("rust") or {}).get("kind") == "host"),
    }


def _pressure_mode_in_command(report):
    """The ``--pressure`` mode the recorded command line asked for, or None.

    ``command`` is stamped by ``command_sanitize.write_committed_report`` from
    the operator's own argv, so in a committed report it is not a field the
    author of the record chose.  Returns None when the command does not name the
    option (a report built offline, or one produced before the option existed),
    which leaves the check to the swept profile set alone.
    """

    command = report.get("command")
    if not isinstance(command, list):
        return None
    items = [str(item) for item in command]
    for index, item in enumerate(items):
        if item == "--pressure":
            value = items[index + 1] if index + 1 < len(items) else ""
        elif item.startswith("--pressure="):
            value = item.split("=", 1)[1]
        else:
            continue
        return value if value in PRESSURE_AXIS_MODES else None
    return None


def verify_pressure_report(report):
    """Verify one pressure sweep against the committed tables and pins.

    assess_report requires the section; this function judges what the
    section claims against the committed profile and pin tables. The grid is
    re-derived here, independently of the report, so the executed cell set is
    an obligation rather than a choice.
    """

    # Table-level integrity is assessed by table_integrity_problems(), on every
    # run, whether or not this report claims the axis. Here only the report is
    # judged: coverage re-derived from the committed tables, the pinned class of
    # each executed cell, the wedge/poller/vacuous terms, and the rollup.
    problems = []
    pressure = report.get("pressure")
    if not isinstance(pressure, dict):
        return [f"pressure section is {type(pressure).__name__}"]
    cells = pressure.get("cells")
    if not isinstance(cells, list):
        return problems + ["pressure section has no cell list"]
    for name in ("mode", "arms", "profiles", "mandatory_profiles",
                 "cells_expected", "cells_executed", "cells_missing",
                 "pinned_table_count", "pinned_table_sha256", "agreements",
                 "divergences", "band_gap", "hangs", "flaky", "vacuous",
                 "blocked", "host_state"):
        if name not in pressure:
            problems.append(f"pressure rollup is missing {name!r}")
    executed = {}
    for cell in cells:
        key = (cell.get("arm"), cell.get("profile"))
        if key in executed:
            problems.append(f"pressure cell {key} is reported twice")
            continue
        executed[key] = cell
    for arm, profile in expected_pressure_cells(pressure.get("profiles")
                                                or MANDATORY_PRESSURE_PROFILES):
        if (arm, profile) not in executed:
            problems.append(
                f"pressure cell ({arm!r}, {profile!r}) was never executed; the "
                f"product of arms and profiles is an obligation")
            continue
        pin = PINNED_PRESSURE_CLASSES.get((arm, profile))
        if pin is None:
            problems.append(
                f"pressure cell ({arm!r}, {profile!r}) has no pinned class")
            continue
        cell = executed[(arm, profile)]
        if not isinstance(pin, dict):
            # A loosened pin is tampering, not an obligation: judged any
            # further it would be read as a class with no wedge, poller, or
            # coverage term attached, which is exactly the meaning the
            # loosening discards. The table-level digest anchor names the
            # change; here the cell is reported and left unjudged.
            problems.append(
                f"pressure cell ({arm}, {profile}) has a pinned obligation "
                f"that is {type(pin).__name__}, not a mapping: a pressure pin "
                f"must carry its wedge, poller, and coverage terms")
            continue
        for engine in ("go", "rust"):
            problems.extend(pressure_cell_problems(cell, pin, engine))
        divergent, band_gap = _pressure_cell_divergence(pin, cell)
        if bool(cell.get("band_gap")) != band_gap:
            # The per-cell flag is a report about the table, so it is judged
            # against the table: a cell cannot join the reported band gaps by
            # asserting one, and a genuine band gap cannot be pushed back into
            # the divergence count by denying it.
            problems.append(
                f"pressure cell ({arm}, {profile}) records band_gap="
                f"{bool(cell.get('band_gap'))!r} where the pinned classes of "
                f"its own answers derive band_gap={band_gap}; whether a cell "
                f"is design section 13.3's gap is what the committed table "
                f"says for each engine's own outcome, not a label")
        if divergent:
            go_answer, rust_answer = (_pressure_answer(cell, "go"),
                                       _pressure_answer(cell, "rust"))
            problems.append(
                f"pressure cell ({arm}, {profile}) diverged between engines: "
                f"go {go_answer} vs rust {rust_answer}; the classes of a "
                f"descriptor-pressure cell must match across the engines, and "
                f"the only expected difference is one engine completing inside "
                f"the band gap design section 13.3 records (design section 14)")
    rollup = pressure_rollup(list(executed.values()),
                             pressure.get("profiles"))
    for name in ("cells_expected", "cells_executed", "agreements",
                 "divergences", "band_gap", "hangs", "flaky", "vacuous",
                 "blocked", "host_state"):
        if pressure.get(name) != rollup[name]:
            problems.append(
                f"pressure rollup {name} says {pressure.get(name)!r}, the "
                f"executed cells say {rollup[name]!r}")
    if pressure.get("pinned_table_sha256") != pinned_pressure_fingerprint():
        problems.append("pressure rollup digests the pinned table differently "
                        "than the committed table does")
    if pressure.get("pinned_table_count") != len(PINNED_PRESSURE_CLASSES):
        problems.append("pressure rollup counts the pinned table differently "
                        "than the committed table does")
    # The axis label is a claim about what was paid for.  A label that is
    # present must agree with the profile set it swept and with the option the
    # shared writer recorded in the command line, so a 108-cell routine sweep
    # cannot be filed as the milestone's 378-cell coverage.  A report produced
    # before the member existed carries null and is judged by the cell set
    # alone, exactly as it was before the label existed; the produced artifact
    # is what rotates that hole shut, not a check that could be satisfied by
    # writing any of three words.
    mode = pressure.get("mode")
    if mode is not None and mode != PRESSURE_FABRICATED_MODE:
        swept = list(pressure.get("profiles") or ())
        if swept == list(ROUTINE_PRESSURE_PROFILES):
            expected_mode = PRESSURE_MODE_ROUTINE
        elif swept == list(MANDATORY_PRESSURE_PROFILES):
            expected_mode = PRESSURE_MODE_FULL
        else:
            expected_mode = None
        if expected_mode is None:
            problems.append(
                f"pressure axis records mode={mode!r} over a profile set that "
                f"is neither the routine subset nor the full product, so no "
                f"axis this gate knows was swept")
        elif mode != expected_mode:
            problems.append(
                f"pressure axis records mode={mode!r} but the swept profiles "
                f"are the {expected_mode!r} set; the label must name the axis "
                f"that was executed")
        declared = _pressure_mode_in_command(report)
        if declared is not None and mode != declared:
            problems.append(
                f"pressure axis records mode={mode!r} where the recorded "
                f"command line asked for --pressure {declared!r}; the label "
                f"and the option the shared writer stamped cannot disagree")
    return problems


def pinned_table_fingerprint(table=None):
    """The digest of the pinned-refusal table in a stable encoding.

    JSON with sorted keys and no optional whitespace, over the
    ``(key, value)`` pairs. The value is encoded as it is written, so a bare
    class and a mapping that pins the same class cannot digest alike: the
    encoding records the shape of the obligation, not only its class.
    """

    source = PINNED_REFUSALS if table is None else table
    encoded = json.dumps([[[arm, kind], value]
                          for (arm, kind), value in sorted(source.items())])
    return hashlib.sha256(encoded.encode("utf-8")).hexdigest()


def table_integrity_problems():
    """Every way the committed tables can be shrunk without being noticed.

    Checked against the committed constants rather than against the report,
    so this is the term that turns a deleted pin, a loosened pin, a deleted
    arm, or a deleted path kind into a gate failure instead of a smaller gate.
    """

    problems = []
    if len(PINNED_REFUSALS) != PINNED_REFUSAL_COUNT:
        problems.append(
            f"the pinned-refusal table has {len(PINNED_REFUSALS)} entries, "
            f"not the committed {PINNED_REFUSAL_COUNT}; a pin cannot be "
            f"deleted to make the gate pass")
    digest = pinned_table_fingerprint()
    if digest != PINNED_REFUSALS_SHA256:
        problems.append(
            f"the pinned-refusal table digests to {digest}, not the committed "
            f"{PINNED_REFUSALS_SHA256}; deleting a pin, adding one, or "
            f"loosening one to a bare class (dropping its outcome or "
            f"publication-evidence requirement) all read as tampering with "
            f"the authority this gate is anchored to")
    for name, table, mandatory in (("path kind", list(PATH_KINDS),
                                    MANDATORY_PATH_KINDS),
                                   ("arm", list(ARM_NAMES), MANDATORY_ARMS)):
        for entry in mandatory:
            if entry not in table:
                problems.append(
                    f"{name} {entry!r} is an obligation but is missing from "
                    f"the table; deleting a swept shape is not a way to pass")
        for entry in table:
            if entry not in mandatory:
                problems.append(
                    f"{name} {entry!r} is swept but is not an obligation, so "
                    f"deleting it would shrink the gate unnoticed; name it in "
                    f"the mandatory table")
    # The third axis is anchored with the same term, so deleting a pressure
    # profile, an arm, or a pinned class fails every gate run rather than
    # producing a quieter one.
    problems.extend(pressure_table_integrity_problems())
    return problems


def _sha256_ledger(path):
    """Read a sha256sum-format ledger: ``{digest: {recorded path, ...}}``.

    The format is the one ``sha256sum`` writes: digest, two spaces, then the
    staged path. A parity verdict is only as good as the artifacts that
    produced it, so the committed report names each binary's digest and the
    ledger is the reviewer-side list of what the wave staged. It is an
    additional binder, consulted only when supplied, exactly like the other
    harnesses of this battery.
    """

    if not path:
        return None
    resolved = os.path.realpath(path)
    if not os.path.isfile(resolved):
        raise SystemExit(f"--sha256-ledger {path} does not exist")
    entries = {}
    with open(resolved, encoding="utf-8") as stream:
        for number, line in enumerate(stream, start=1):
            line = line.rstrip("\n")
            if not line.strip():
                continue
            digest, _, recorded = line.partition("  ")
            digest = digest.strip()
            recorded = recorded.strip()
            if len(digest) != 64 or not all(
                    character in "0123456789abcdef" for character in digest) \
                    or not recorded:
                raise SystemExit(
                    f"--sha256-ledger {path}:{number} is not a "
                    f"'<sha256>  <path>' line")
            entries.setdefault(digest, set()).add(recorded)
    if not entries:
        raise SystemExit(f"--sha256-ledger {path} lists no entries")
    return entries


def pin_expectation(value):
    """Normalize one ``PINNED_REFUSALS`` entry to a comparable expectation.

    Returns ``(data_code, outcome, facts)``.  A bare string pins the class
    only; a mapping may also pin ``data.outcome``, which is where a read-only
    refusal is distinguished from a refusal that performed a write, and
    ``facts``, which is where the publication path is distinguished from any
    other refusal of the same class: ``"required"`` means the reply must
    carry the complete publication evidence, ``"absent"`` means it must not
    carry any, and ``None`` (the default) leaves the evidence unpinen.
    """

    if isinstance(value, dict):
        facts = value.get("facts")
        if facts not in (None, "required", "absent"):
            raise SystemExit(f"pin facts {facts!r} is not required or absent")
        return (value.get("data_code"), value.get("outcome"), facts)
    return value, None, None


ARM_NAMES = [entry[0] for entry in ARMS]
ARM_BY_NAME = {entry[0]: entry for entry in ARMS}
def kind_names():
    """The path kinds of the grid, read live from ``PATH_KINDS``.

    Deriving the grid from the table on every call (rather than from a
    snapshot) is what makes deleting a shape observable: the obligation
    check and the executed-cell comparison both move with the table, so a
    reviewer or a later wave cannot shrink the sweep by removing a row and
    still call the result a PASS.
    """

    return list(PATH_KINDS)


def expected_cells():
    """The mechanically derived grid: every arm over every path kind."""
    return [(arm, kind) for kind in kind_names() for arm in ARM_NAMES]


# --------------------------------------------------------------------------
# Publication evidence.  The durability contract is that an unresolved
# failure after the destination name appeared states what is known, so the
# reply must carry these members (see the outcome-ambiguity boundary in
# .agents/sow/specs/binary-format-v4.md).  Only their presence and wire shape
# are compared; their values are recorded for the reader, because "which
# stage failed" is prose and "which facts exist" is the contract.
# --------------------------------------------------------------------------
REQUIRED_PUBLICATION_FACTS = ("outcome", "publication_policy", "path", "stage",
                              "destination_visible", "temporary_removed",
                              "sha256")


def _is_hex64(value):
    return (isinstance(value, str) and len(value) == 64
            and all(c in "0123456789abcdef" for c in value))


def _fact_shape(facts):
    """Reduce one ``publication`` evidence object to its comparable shape.

    Returns ``None`` when the object does not carry the complete fact set, so
    a reply that omits a member fails the obligation instead of comparing
    equal to a reply that carries it.
    """

    if not isinstance(facts, dict):
        return None
    if any(name not in facts for name in REQUIRED_PUBLICATION_FACTS):
        return None
    if not (isinstance(facts.get("stage"), str) and facts["stage"]):
        return None
    if not _is_hex64(facts.get("sha256")):
        return None
    if facts.get("destination_visible") is not True:
        return None
    if not isinstance(facts.get("temporary_removed"), bool):
        return None
    if not (isinstance(facts.get("publication_policy"), str)
            and facts["publication_policy"]):
        return None
    if facts.get("outcome") != "outcome_unknown":
        return None
    return {"complete": True, "destination_visible": True,
            "temporary_removed": facts["temporary_removed"],
            "sha256_well_formed": True, "outcome_unknown": True}


def publication_facts(response):
    """Extract the publication evidence of one reply, as a comparable shape.

    Two containers carry it on this surface and both engines spell them the
    same way: ``error.data.details.publication`` for the export writer and
    the metadata delivery, and
    ``error.data.details.removals_publication_failure.publication`` for the
    auxiliary removal output of a committed first-seen refresh (whose own
    reply outcome belongs to the transaction, not to the auxiliary file).
    """

    if not isinstance(response, dict):
        return None, None
    error = response.get("error")
    if not isinstance(error, dict):
        return None, None
    data = error.get("data")
    if not isinstance(data, dict):
        return None, None
    details = data.get("details")
    if not isinstance(details, dict):
        return None, None
    containers = [("publication", details.get("publication"))]
    auxiliary = details.get("removals_publication_failure")
    if isinstance(auxiliary, dict):
        containers.append(("removals_publication_failure",
                           auxiliary.get("publication")))
    for name, candidate in containers:
        shape = _fact_shape(candidate)
        if shape is not None:
            return dict(shape, container=name), candidate
    return None, None


def sha256_file(path):
    digest = hashlib.sha256()
    with open(path, "rb") as stream:
        for chunk in iter(lambda: stream.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


class Product:
    """One ``--jsonrpc`` child process speaking newline-delimited frames.

    One process per attempt is deliberate: a wedge or a crash must be
    attributable to exactly one cell, and a shared session would let a hung
    arm poison every later cell.
    """

    def __init__(self, binary, work):
        self.proc = subprocess.Popen(
            [binary, "--jsonrpc"], stdin=subprocess.PIPE,
            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, cwd=work,
            env={"PATH": os.environ.get("PATH", "/usr/bin:/bin")},
            start_new_session=True)
        self.buffer = b""

    def call(self, request, timeout):
        try:
            self.proc.stdin.write(
                (json.dumps(request) + "\n").encode("utf-8"))
            self.proc.stdin.flush()
        except Exception as exc:  # noqa: BLE001 - recorded, never raised
            return "write-error", {"error": repr(exc)}
        deadline = time.monotonic() + timeout
        fd = self.proc.stdout.fileno()
        selector = selectors.DefaultSelector()
        selector.register(fd, selectors.EVENT_READ)
        try:
            while time.monotonic() < deadline:
                index = self.buffer.find(b"\n")
                if index >= 0:
                    raw = self.buffer[:index + 1]
                    self.buffer = self.buffer[index + 1:]
                    try:
                        return "answered", json.loads(raw)
                    except ValueError:
                        return "bad-json", {"error": raw[:200].decode(
                            "utf-8", "replace")}
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    break
                if not selector.select(min(remaining, 0.2)):
                    continue
                try:
                    chunk = os.read(fd, 65536)
                except OSError:
                    chunk = b""
                if not chunk:
                    break
                self.buffer += chunk
        finally:
            selector.close()
        return "timeout", None

    def close(self, timeout=5.0):
        try:
            self.proc.stdin.close()
        except Exception:  # noqa: BLE001
            pass
        try:
            return self.proc.wait(timeout=timeout)
        except subprocess.TimeoutExpired:
            try:
                os.killpg(self.proc.pid, 9)
            except OSError:
                pass
            return self.proc.wait()


def _answer(response):
    """Reduce one JSON-RPC reply to the compared triple.

    Only ``(kind, transport code, data.code, data.outcome)`` is compared;
    message text is a diagnostic and is recorded, not compared.
    """

    if not isinstance(response, dict):
        return (None, None, None)
    error = response.get("error")
    if isinstance(error, dict):
        data = error.get("data")
        data = data if isinstance(data, dict) else {}
        return (error.get("code"), data.get("code"), data.get("outcome"))
    if "result" in response:
        return ("RESULT", "RESULT", "RESULT")
    return (None, None, None)


def _clean(work):
    for name in _EPHEMERAL:
        try:
            os.unlink(os.path.join(work, name))
        except FileNotFoundError:
            pass


def _remove_any(path):
    if os.path.islink(path):
        os.unlink(path)
    elif os.path.isdir(path):
        shutil.rmtree(path)
    elif os.path.exists(path):
        os.unlink(path)


def _drop_dir(path):
    """Remove a directory the harness may itself have made unreadable.

    ``dest-parent-unreadable`` leaves owner mode 0311 behind, which is enough
    to publish into but not to list, so the mode must be restored before
    removal or the next cell could not clear the previous one.
    """

    if os.path.isdir(path):
        os.chmod(path, 0o0755)
    _remove_any(path)


def prepare_work(work, fixture_tool, authority_binary):
    """Materialize the shapes every cell copies from, once per sweep.

    The live and membership databases are created by the Rust authority
    binary so both engines read byte-identical inputs; creating them per
    engine would fold an engine's own writer behavior into a test of its
    reader behavior.
    """

    os.makedirs(work, exist_ok=True)
    with open(os.path.join(work, "data.csv"), "w", encoding="utf-8") as out:
        out.write("from,to,value\n192.0.2.1,192.0.2.2,7\n")
    with open(os.path.join(work, "list.txt"), "w", encoding="utf-8") as out:
        out.write(os.path.join(work, "data.csv") + "\n")
    fixture = os.path.join(work, "real.db")
    if os.path.exists(fixture):
        os.unlink(fixture)
    subprocess.run([fixture_tool, "direct-v4", fixture], check=True,
                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    membership = os.path.join(work, "template-membership.iprange")
    if os.path.exists(membership):
        os.unlink(membership)
    subprocess.run([fixture_tool, "membership-v4", membership], check=True,
                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    live = os.path.join(work, "template-live.iprange")
    live_membership = os.path.join(work, "template-live-membership.iprange")
    for path in (live, live + ".readers", live_membership,
                 live_membership + ".readers"):
        _remove_any(path)
    for path, value_kind in ((live, "direct"),
                             (live_membership, "membership")):
        product = Product(authority_binary, work)
        try:
            kind, response = product.call({
                "jsonrpc": "2.0", "id": 1,
                "method": "iprange.v1.database.create",
                "params": {"path": path, "family": "ipv4",
                           "value_kind": value_kind,
                           "structure_kind": "none",
                           "value_tag": {"text": "parity"},
                           "reader_capacity": 8}}, timeout=10.0)
        finally:
            product.close(timeout=10.0)
        if kind != "answered" or not response.get("result"):
            raise SystemExit(f"cannot create the {value_kind} live template: "
                             f"{kind} {response}")
        if not os.path.exists(path + ".readers"):
            raise SystemExit(f"the {value_kind} live template has no sidecar")
    # The first-seen refresh needs a live database that already holds ranges,
    # so that its transaction genuinely commits and the removals collector
    # reaches the durability step the destination cells measure.  Built once
    # here by the authority binary and copied fresh per attempt.
    refresh = os.path.join(work, "template-refresh-live.iprange")
    for path in (refresh, refresh + ".readers"):
        _remove_any(path)
    product = Product(authority_binary, work)
    try:
        kind, response = product.call({
            "jsonrpc": "2.0", "id": 1,
            "method": "iprange.v1.database.create",
            "params": {"path": refresh, "family": "ipv4",
                       "value_kind": "direct", "structure_kind": "none",
                       # The first-seen workflow refuses any other tag, so
                       # the template carries the one its own contract names.
                       "value_tag": {"text": "first_seen"},
                       "reader_capacity": 8}}, timeout=10.0)
        if kind != "answered" or not response.get("result"):
            raise SystemExit(f"cannot create the refresh template: "
                             f"{kind} {response}")
        kind, response = product.call({
            "jsonrpc": "2.0", "id": 2,
            "method": "iprange.v1.direct.replace",
            "params": {"path": refresh,
                       "input": {"path": os.path.join(work, "data.csv"),
                                 "max_line_bytes": 1024},
                       "metadata": {"mode": "keep"},
                       "writer_budget": dict(WRITER_BUDGET)}}, timeout=10.0)
        if kind != "answered" or not response.get("result"):
            raise SystemExit(f"cannot seed the refresh template: "
                             f"{kind} {response}")
    finally:
        product.close(timeout=10.0)
    if not os.path.exists(refresh + ".readers"):
        raise SystemExit("the refresh template has no sidecar")
    return {"work": work, "fixture": fixture, "data_csv":
            os.path.join(work, "data.csv"), "list_txt":
            os.path.join(work, "list.txt"), "live_main": live,
            "live_sidecar": live + ".readers", "membership_main": membership,
            "live_membership_main": live_membership,
            "live_membership_sidecar": live_membership + ".readers",
            "refresh_main": refresh, "refresh_sidecar": refresh + ".readers"}


def materialize(kind_name, ctx):
    """Create the target of one path kind and return its path."""

    work = ctx["work"]
    target = os.path.join(work, "PROBE-" + kind_name)
    # _drop_dir, not _remove_any: the destination cells own a directory the
    # harness itself made unreadable, and every cell must start clean or the
    # second engine would measure the first engine's leftovers.
    _drop_dir(target)
    if kind_name not in PATH_KINDS:
        raise SystemExit(f"unknown path kind {kind_name!r}")
    if kind_name == "regular-valid":
        shutil.copyfile(ctx["fixture"], target)
    elif kind_name == "regular-valid-membership":
        shutil.copyfile(ctx["membership_main"], target)
    elif kind_name == "live-membership":
        base = os.path.join(work, "lm-main.iprange")
        _remove_any(base)
        _remove_any(base + ".readers")
        shutil.copyfile(ctx["live_membership_main"], base)
        shutil.copyfile(ctx["live_membership_sidecar"], base + ".readers")
        return base
    elif kind_name == "live-direct":
        # Copied fresh per attempt like the refresh target: a named-feed
        # arm commits a transaction, so a shared database would make each
        # attempt start from a different generation and let a class depend
        # on sweep order instead of on the shape under test.
        base = os.path.join(work, "ld-main.iprange")
        _remove_any(base)
        _remove_any(base + ".readers")
        shutil.copyfile(ctx["live_main"], base)
        shutil.copyfile(ctx["live_sidecar"], base + ".readers")
        return base
    elif kind_name == "missing":
        pass
    elif kind_name == "dir":
        os.makedirs(target)
    elif kind_name == "unix-socket":
        listener = socket.socket(socket.AF_UNIX)
        listener.bind(target)
        listener.close()
    elif kind_name == "fifo":
        os.mkfifo(target, 0o600)
    elif kind_name == "symlink-regular":
        real = os.path.join(work, "sym-real.db")
        if not os.path.exists(real):
            shutil.copyfile(ctx["fixture"], real)
        os.symlink(real, target)
    elif kind_name == "symlink-dir":
        real = os.path.join(work, "sym-dir")
        os.makedirs(real, exist_ok=True)
        os.symlink(real, target)
    elif kind_name == "symlink-fifo":
        real = os.path.join(work, "sym-fifo")
        if not os.path.exists(real):
            os.mkfifo(real, 0o600)
        os.symlink(real, target)
    elif kind_name == "symlink-live":
        base = os.path.join(work, "sl-main.iprange")
        _remove_any(base)
        _remove_any(base + ".readers")
        shutil.copyfile(ctx["live_main"], base)
        shutil.copyfile(ctx["live_sidecar"], base + ".readers")
        os.symlink(base, target)
    elif kind_name == "hardlink-live":
        base = os.path.join(work, "hl-main.iprange")
        _remove_any(base)
        _remove_any(base + ".readers")
        shutil.copyfile(ctx["live_main"], base)
        shutil.copyfile(ctx["live_sidecar"], base + ".readers")
        os.link(base, target)
    elif kind_name == "procfs":
        if not os.path.exists(PROCFS_SHAPE):
            raise SystemExit(f"procfs shape {PROCFS_SHAPE} is unavailable")
        return PROCFS_SHAPE
    elif kind_name == "sysfs":
        if not os.path.exists(SYSFS_SHAPE):
            raise SystemExit(f"sysfs shape {SYSFS_SHAPE} is unavailable")
        return SYSFS_SHAPE
    elif kind_name == "zero-length-live":
        base = os.path.join(work, "zl-main.iprange")
        _remove_any(base)
        _remove_any(base + ".readers")
        shutil.copyfile(ctx["live_main"], base)
        shutil.copyfile(ctx["live_sidecar"], base + ".readers")
        with open(base, "wb"):
            pass
        return base
    elif kind_name == "dest-parent-unreadable":
        # Owner mode 0311 keeps write and search, so the output owner can
        # create its private temporary and publish onto the destination, and
        # denies read, so opening that parent to synchronize it fails with
        # EACCES.  That is a deterministic post-visibility durability failure
        # on both engines, without a filesystem, a device, or a fault frame.
        os.makedirs(target)
        os.chmod(target, 0o0311)
        return os.path.join(target, "out.iprange")
    elif kind_name == "dest-collision":
        # The pre-visibility control: the same destination shape and the same
        # ``fail_if_exists`` policy, with a normal parent and a destination
        # that already exists, so the refusal is definite and no destination
        # name ever becomes visible.
        os.makedirs(target)
        os.chmod(target, 0o0755)
        destination = os.path.join(target, "out.iprange")
        with open(destination, "wb") as stream:
            stream.write(b"pre-existing destination content\n")
        return destination
    elif kind_name == "dest-fifo-crossfs":
        # /dev/shm is tmpfs, which the Linux durability whitelist rejects for
        # both engines (the whitelist is shared: internal/fslocal and Rust
        # require_local_filesystem name the same filesystems). The work
        # directory stays on the qualified filesystem, so this cell is the
        # cross-filesystem pair the finding names rather than a uniform
        # refusal of every destination. The parent is rebuilt from scratch on
        # every attempt so neither engine measures the other's leftovers.
        crossfs = "/dev/shm"
        if not os.path.isdir(crossfs) or not os.access(crossfs, os.W_OK):
            raise SystemExit(f"{crossfs} is not a writable directory; the "
                             f"cross-filesystem destination cell cannot be "
                             f"materialized")
        parent = os.path.join(crossfs,
                             "iprange-parity-" + os.path.basename(work))
        _drop_dir(parent)
        os.makedirs(parent)
        destination = os.path.join(parent, "out.iprange")
        os.mkfifo(destination, 0o600)
        return destination
    elif kind_name == "junk":
        with open(target, "wb") as stream:
            stream.write(b"\x00" * 4096)
    elif kind_name == "sidecar-folded":
        base = os.path.join(work, "fold-main.iprange")
        _remove_any(base)
        _remove_any(base + ".readers")
        shutil.copyfile(ctx["live_main"], base)
        shutil.copyfile(ctx["live_sidecar"], base + ".readers")
        # The fold: the sidecar exists as a directory, so the quiescent
        # live-sidecar open fails and the arm must fold through the
        # coordination class.
        os.unlink(base + ".readers")
        os.makedirs(base + ".readers")
        return base
    else:  # pragma: no cover - PATH_KINDS and this dispatcher share one table
        raise SystemExit(f"path kind {kind_name!r} has no materializer")
    return target


def run_cell(engine, binary, arm_name, kind_name, ctx, deadline):
    """Execute one (arm, path-kind, engine) attempt and record its answer."""

    _clean(ctx["work"])
    target = materialize(kind_name, ctx)
    _name, method, slot, build = ARM_BY_NAME[arm_name]
    request = {"jsonrpc": "2.0", "id": 1, "method": method,
               "params": build(target, ctx)}
    product = Product(binary, ctx["work"])
    started = time.monotonic()
    try:
        kind, response = product.call(request, timeout=deadline)
    except Exception as exc:  # noqa: BLE001 - an unwedged harness, never a wedge
        kind, response = "harness-error", {"error": repr(exc)}
    elapsed = time.monotonic() - started
    exit_status = product.close(timeout=deadline)
    code, data_code, outcome = _answer(response)
    facts, evidence = publication_facts(response)
    return {"engine": engine, "arm": arm_name, "path_kind": kind_name,
            "method": method, "slot": slot, "kind": kind,
            "transport_code": code, "data_code": data_code,
            "outcome": outcome, "exit_status": exit_status,
            "elapsed_ms": round(elapsed * 1000, 1),
            "publication_facts": facts,
            "publication_evidence": evidence,
            "request": json.dumps(request, separators=(",", ":"))}


def compare(a, b):
    """True when two engine records agree on code, class, outcome, evidence.

    The compared value of ``publication_facts`` is the canonical shape built
    by ``_fact_shape`` (which members exist and whether each has its contract
    wire form), never the prose or the digest value, so agreement on
    "published, and here is what was published" is a measured property and not
    an assumption.
    """

    return (a["kind"] == b["kind"] == "answered"
            and (a["transport_code"], a["data_code"], a["outcome"])
            == (b["transport_code"], b["data_code"], b["outcome"])
            and a.get("publication_facts") == b.get("publication_facts"))


def _sweep_cell(go, rust, arm_name, kind_name, ctx, deadline, retries):
    """One grid cell: both engines, with the flake-vs-divergence retry policy."""

    attempts = 0
    go_record = rust_record = None
    agreed = False
    hung = False
    while attempts <= retries:
        attempts += 1
        go_record = run_cell("go", go, arm_name, kind_name, ctx, deadline)
        rust_record = run_cell("rust", rust, arm_name, kind_name, ctx, deadline)
        if go_record["kind"] != "answered" \
                or rust_record["kind"] != "answered":
            hung = True
        agreed = compare(go_record, rust_record)
        if agreed:
            break
    return {
        "arm": arm_name, "path_kind": kind_name,
        "attempts": attempts, "flaky": bool(attempts > 1 and agreed),
        "hung": bool(hung and not agreed),
        "agreed": bool(agreed),
        "go": go_record, "rust": rust_record}


def sweep(go, rust, ctx, deadline, retries, jobs=1, ctx_factory=None):
    """Execute every grid cell on both engines with a flake-vs-divergence policy.

    A first-attempt disagreement is retried with fresh processes up to
    ``retries`` times.  Any retry that agrees marks the cell flaky and keeps it
    out of the divergence count; a cell that disagrees on every attempt is a
    divergence.  A reply that never arrives is a hang and is reported as such
    rather than as a class disagreement, because the two have different owners.

    ``jobs`` schedules independently runnable cells concurrently.  A cell owns
    one request, two engine processes and the target shape it copies from, so
    two cells share nothing except the sweep's read-only templates -- but they
    do share the sweep's *work directory* if one context is used, where
    ``_clean()`` and ``materialize()`` would delete and recreate each other's
    target.  Each concurrent worker therefore gets its own work directory from
    ``ctx_factory``, which is ``prepare_work`` on a private subdirectory; the
    templates it seeds are byte-identical because they are authored by the Rust
    authority binary either way.

    The returned list stays in (arm, path-kind) order regardless of completion
    order: the report's cell sequence is part of its shape, and a sweep that
    reordered cells would make two runs of the same revision incomparable.
    """

    pairs = [(arm_name, kind_name) for arm_name in ARM_NAMES
             for kind_name in kind_names()]

    if jobs <= 1 or ctx_factory is None:
        return [_sweep_cell(go, rust, arm, kind, ctx, deadline, retries)
                for arm, kind in pairs]

    import queue                 # noqa: PLC0415  (only the concurrent path needs it)
    import threading             # noqa: PLC0415

    slots = queue.Queue()
    for index in range(jobs):
        slots.put(ctx_factory(index))

    results = [None] * len(pairs)
    lock = threading.Lock()
    next_index = [0]

    def worker():
        while True:
            with lock:
                index = next_index[0]
                if index >= len(pairs):
                    return
                next_index[0] = index + 1
            arm, kind = pairs[index]
            slot = slots.get()
            try:
                results[index] = _sweep_cell(go, rust, arm, kind, slot,
                                             deadline, retries)
            finally:
                slots.put(slot)

    threads = [threading.Thread(target=worker, daemon=True)
               for _ in range(jobs)]
    for thread in threads:
        thread.start()
    for thread in threads:
        thread.join()
    missing = [pairs[i] for i, cell in enumerate(results) if cell is None]
    if missing:
        # A worker that died with the exception swallowed by Thread would
        # otherwise show up as a grid with fewer cells than the table, which
        # assess_report reports as a coverage problem rather than the harness
        # failure it is.
        raise SystemExit(
            f"grid workers did not produce {len(missing)} cell(s): "
            f"{missing[:4]}")
    return results


def pin_engine_problems(key, engine, record, expected_class,
                        expected_outcome, expected_facts):
    """Every way one engine's answer to one pinned cell misses the pin.

    Shared by the report builder (which counts satisfied pins) and the
    verifier (which must not be able to disagree with it), so a pin can never
    be satisfied for the summary and violated for the verdict.
    """

    problems = []
    got = record.get("data_code")
    if got != expected_class:
        problems.append(
            f"pinned cell {key!r}: {engine} answered {got!r}, the "
            f"Rust authority pins {expected_class!r}")
    elif expected_outcome is not None \
            and record.get("outcome") != expected_outcome:
        problems.append(
            f"pinned cell {key!r}: {engine} answered outcome "
            f"{record.get('outcome')!r}, the Rust authority pins "
            f"{expected_outcome!r}")
    facts = record.get("publication_facts")
    if expected_facts == "required" and facts is None:
        problems.append(
            f"pinned cell {key!r}: {engine} answered "
            f"{got!r}/{record.get('outcome')!r} without the publication "
            f"evidence ({', '.join(REQUIRED_PUBLICATION_FACTS)}); a refusal "
            f"whose destination may be published must state what is known")
    elif expected_facts == "absent" and facts is not None:
        problems.append(
            f"pinned cell {key!r}: {engine} carried publication evidence for "
            f"a reply that must be definite; claiming an unknown outcome for "
            f"a destination that never appeared is as wrong as omitting it "
            f"once it did")
    return problems


def build_report(go, rust, fixture, work, deadline, retries, budget,
                 argv, cells, started_at, ended_at, provenance=None):
    """Assemble the evidence report for one sweep."""

    divergences = [c for c in cells if not c["agreed"] and not c["hung"]]
    hangs = [c for c in cells if c["hung"] and not c["agreed"]]
    pins = []
    for (arm_name, kind_name), value in sorted(PINNED_REFUSALS.items()):
        want_code, want_outcome, want_facts = pin_expectation(value)
        record = next((c for c in cells if c["arm"] == arm_name
                       and c["path_kind"] == kind_name), None)
        pins.append({"arm": arm_name, "path_kind": kind_name,
                     "expected_data_code": want_code,
                     "expected_outcome": want_outcome,
                     "expected_facts": want_facts,
                     "executed": record is not None,
                     "go_data_code": None if record is None
                     else record["go"]["data_code"],
                     "go_outcome": None if record is None
                     else record["go"]["outcome"],
                     "go_facts": None if record is None
                     else bool(record["go"].get("publication_facts")),
                     "rust_data_code": None if record is None
                     else record["rust"]["data_code"],
                     "rust_outcome": None if record is None
                     else record["rust"]["outcome"],
                     "rust_facts": None if record is None
                     else bool(record["rust"].get("publication_facts"))})
    # The identity members come from the shared owner (``report_provenance``)
    # rather than from this file, and ``write_committed_report`` overwrites them
    # with the same values at the write.  They are present before the verdict
    # because ``assess_report`` judges the report in the shape it will be
    # committed in: a field the gate requires but the writer had not yet added
    # would be a gate failure instead of an evidence defect.
    return {
        "schema": REPORT_SCHEMA,
        **report_provenance(argv),
        "platform": {"system": platform.system(),
                     "release": platform.release(),
                     "machine": platform.machine(),
                     "processor": platform.processor(),
                     "python": platform.python_version()},
        "binaries": {
            "go": {"path": sanitized_path_value(go),
                   "sha256": sha256_file(go), "implementation": "go"},
            "rust": {"path": sanitized_path_value(rust),
                     "sha256": sha256_file(rust), "implementation": "rust"},
            "fixture_tool": {"path": sanitized_path_value(fixture),
                             "sha256": sha256_file(fixture),
                             "implementation": "rust"}},
        "grid": {"arms": ARM_NAMES, "path_kinds": kind_names(),
                 "mandatory_path_kinds": list(MANDATORY_PATH_KINDS),
                 "mandatory_arms": list(MANDATORY_ARMS),
                 "pinned_table_count": len(PINNED_REFUSALS),
                 "pinned_table_sha256": pinned_table_fingerprint(),
                 "cells_expected": len(expected_cells())},
        "method": {"attempt_deadline_seconds": deadline, "retries": retries,
                   "run_budget_seconds": budget,
                   "compared": ["kind", "transport_code", "data_code",
                                "outcome", "publication_facts"],
                   "note": "message text is a diagnostic and is not compared "
                           "(recorded parity acceptance ruling); Rust is the "
                           "authority for classes PINNED_REFUSALS names"},
        "elapsed_seconds": round(ended_at - started_at, 3),
        "provenance": provenance,
        "cells": cells,
        "pins": pins,
        "summary": {"cells_expected": len(expected_cells()),
                    "cells_executed": len(cells),
                    "agreements": sum(1 for c in cells if c["agreed"]),
                    "divergences": len(divergences),
                    "hangs": len(hangs),
                    "flaky": sum(1 for c in cells if c["flaky"]),
                    "pins_expected": len(PINNED_REFUSALS),
                    "pins_satisfied": sum(
                        1 for p in pins
                        if p["executed"]
                        and p["go_data_code"] == p["rust_data_code"]
                        == p["expected_data_code"]
                        and (p["expected_outcome"] is None
                             or (p["go_outcome"] == p["rust_outcome"]
                                 == p["expected_outcome"]))
                        and _pin_facts_ok(p["expected_facts"],
                                          p["go_facts"], p["rust_facts"]))},
        "durability": durability_rollup(cells),
        "divergences": [{"arm": c["arm"], "path_kind": c["path_kind"],
                         "go": [c["go"]["kind"], c["go"]["transport_code"],
                                c["go"]["data_code"], c["go"]["outcome"]],
                         "rust": [c["rust"]["kind"],
                                  c["rust"]["transport_code"],
                                  c["rust"]["data_code"],
                                  c["rust"]["outcome"]]}
                        for c in divergences + hangs],
    }


def durability_cells():
    """The (arm, path-kind) cells that measure publication durability."""

    return [(arm_name, kind_name) for (arm_name, kind_name), value
            in sorted(PINNED_REFUSALS.items())
            if isinstance(value, dict) and value.get("facts")]


def durability_rollup(cells):
    """Summarize the executed durability cells for the report reader.

    A plain count of the durability cells, kept beside the divergence
    counters, so a report that silently stopped exercising the publication
    path is visible without re-deriving the tables.
    """

    expected = durability_cells()
    executed = {(cell.get("arm"), cell.get("path_kind")): cell
                for cell in cells}
    return {"cells_expected": len(expected),
            "cells_executed": sum(1 for key in expected if key in executed),
            "cells_with_evidence": sum(
                1 for key in expected
                if key in executed and all(
                    (executed[key][engine].get("publication_facts"))
                    for engine in ("go", "rust")))}


def _pin_facts_ok(expected_facts, go_facts, rust_facts):
    """True when one pin's evidence obligation is met by both engines."""

    if expected_facts == "required":
        return bool(go_facts) and bool(rust_facts)
    if expected_facts == "absent":
        return not go_facts and not rust_facts
    return True


def assess_report(report, deadline=ATTEMPT_DEADLINE_SECONDS,
                  sha256_ledger=None):
    """Verify one refusal-class parity report against the committed tables.

    The grid is re-derived here, independently of the report, so the executed
    cell set is an obligation rather than a choice: an empty report, a report
    that silently drops arms or path kinds, a report that calls a hang an
    agreement, and a report whose pinned cell answers another class are each a
    problem.  This is the function ``--self-test`` attacks.
    """

    problems = []
    problems.extend(table_integrity_problems())
    if not isinstance(report, dict):
        return [f"report is {type(report).__name__}, not an object"]
    # The third axis is an obligation of the verdict, not a block a report
    # may drop: the pressure member must be present and well-formed, and it
    # is judged against the committed profile and pin tables -- executed-cell
    # coverage, the pinned class of every cell, the rollup counts, and the
    # axis label against the swept profile set and the recorded command line.
    # Deleting the section from a report swept with --pressure is a forgery,
    # not a smaller claim: a gate that can be passed by deleting one of the
    # axes it attests gates nothing.
    pressure = report.get("pressure")
    if not isinstance(pressure, dict):
        problems.append(
            "report carries no pressure member; the descriptor-pressure axis "
            "is an obligation of this gate's verdict, so a passing sweep "
            "records it -- run with --pressure routine")
    else:
        problems.extend(verify_pressure_report(report))
    if report.get("schema") != REPORT_SCHEMA:
        problems.append(f"unexpected schema {report.get('schema')!r}")
    for member in ("git_head", "checkout_root", "command", "platform",
                   "binaries", "grid", "cells", "pins", "summary",
                   "divergences", "durability"):
        if member not in report:
            problems.append(f"report is missing {member!r}")
    git_head = report.get("git_head")
    if not (isinstance(git_head, str) and len(git_head) == 40
            and all(c in "0123456789abcdef" for c in git_head)
            and len(set(git_head)) > 1):
        problems.append(
            f"git_head {git_head!r} is not a 40-hex revision; the parity "
            f"verdict must name the source it measured")
    binaries = report.get("binaries") or {}
    for label in ("go", "rust"):
        record = binaries.get(label)
        if not isinstance(record, dict):
            problems.append(f"binary {label!r} is missing")
            continue
        if record.get("implementation") != label:
            problems.append(
                f"binary {label!r} records implementation "
                f"{record.get('implementation')!r}; the class verdict must be "
                f"attributable to the engine it names")
        digest = record.get("sha256")
        if not (isinstance(digest, str) and len(digest) == 64
                and all(c in "0123456789abcdef" for c in digest)):
            problems.append(
                f"binary {label!r} has no measured sha256")
        elif sha256_ledger is not None and digest not in sha256_ledger:
            problems.append(
                f"binary {label!r} digest {digest} is not in the staged "
                f"--sha256-ledger; the verdict would otherwise be "
                f"attributable to an artifact no one staged")
    if sha256_ledger is not None:
        grid = report.get("grid") or {}
        if grid.get("pinned_table_sha256") != pinned_table_fingerprint():
            problems.append(
                "the report records a pinned-refusal table digest other than "
                "the committed table; the binaries that were swept must be "
                "bound to the obligations they were measured against")

    # Re-derive the obligation independently of the report.
    expected = set(expected_cells())
    observed = {}
    for index, cell in enumerate(report.get("cells") or []):
        if not isinstance(cell, dict):
            problems.append(f"cells[{index}] is not an object")
            continue
        key = (cell.get("arm"), cell.get("path_kind"))
        if key not in expected:
            problems.append(
                f"cells[{index}]: cell {key!r} is not defined by the "
                f"committed arm and path-kind tables")
            continue
        if key in observed:
            problems.append(
                f"cells[{index}]: duplicate record for {key!r}")
            continue
        observed[key] = cell
        go, rust = cell.get("go"), cell.get("rust")
        if not isinstance(go, dict) or not isinstance(rust, dict):
            problems.append(f"{key}: missing an engine record")
            continue
        for engine, record in (("go", go), ("rust", rust)):
            if record.get("engine") != engine:
                problems.append(
                    f"{key}: record for {engine} says {record.get('engine')!r}")
            if record.get("arm") != cell.get("arm") \
                    or record.get("path_kind") != cell.get("path_kind"):
                problems.append(f"{key}: {engine} record names a different cell")
            frame = None
            if isinstance(record.get("request"), str):
                try:
                    frame = json.loads(record["request"])
                except ValueError:
                    frame = None
            if not isinstance(frame, dict):
                problems.append(
                    f"{key}: {engine} record has no parsable request frame; "
                    f"a class claim must carry the JSON-RPC bytes that "
                    f"produced it")
            else:
                claimed = ARM_BY_NAME[cell.get("arm")][1]
                if frame.get("jsonrpc") != "2.0":
                    problems.append(
                        f"{key}: {engine} request frame is not a JSON-RPC "
                        f"2.0 request ({frame.get('jsonrpc')!r}); the class "
                        f"claim must be attributable to the protocol the "
                        f"product answers")
                if frame.get("method") != claimed:
                    problems.append(
                        f"{key}: {engine} request frame asks method "
                        f"{frame.get('method')!r} but the cell's arm asks "
                        f"{claimed!r}; a class recorded against one method "
                        f"cannot certify another method's refusal class")
                if "method" in record and record["method"] != claimed:
                    problems.append(
                        f"{key}: {engine} record names method "
                        f"{record['method']!r} where the committed arm "
                        f"table asks {claimed!r}; the record's own claim "
                        f"and the grid's obligation are one fact")
            if not isinstance(record.get("elapsed_ms"), (int, float)):
                problems.append(f"{key}: {engine} record has no elapsed time")
            elif record["elapsed_ms"] >= deadline * 1000 \
                    and record.get("kind") == "answered":
                problems.append(
                    f"{key}: {engine} answered after the per-attempt deadline "
                    f"({record['elapsed_ms']} ms) but recorded kind=answered")
        agrees = compare(go, rust)
        if bool(cell.get("agreed")) != agrees:
            problems.append(
                f"{key}: agreed={cell.get('agreed')!r} contradicts the "
                f"recorded answers (code/class/outcome "
                f"go={[go.get('transport_code'), go.get('data_code'), go.get('outcome')]} "
                f"rust={[rust.get('transport_code'), rust.get('data_code'), rust.get('outcome')]}); "
                f"a divergence cannot be recorded as an agreement")
    missing = sorted(expected - set(observed))
    if missing:
        shown = ", ".join(f"{a}/{k}" for a, k in missing[:6])
        problems.append(
            f"{len(missing)} of {len(expected)} grid cells were not executed "
            f"(for example: {shown}); a parity verdict over a shrunken grid is "
            f"not a parity verdict")
    if not report.get("cells"):
        problems.append(
            "the report executed zero cells: a sweep that ran nothing cannot "
            "report parity")

    for kind_name in MANDATORY_PATH_KINDS:
        if kind_name not in PATH_KINDS:
            problems.append(
                f"path kind {kind_name!r} is an obligation but is missing "
                f"from the table; deleting a shape is not a way to pass")
        if not any(k == kind_name for _a, k in expected):
            problems.append(
                f"path kind {kind_name!r} contributes no cell to the grid")
        if any(cell.get("path_kind") != kind_name for cell in
               (report.get("cells") or [])) and not any(
                   cell.get("path_kind") == kind_name
                   for cell in (report.get("cells") or [])):
            problems.append(
                f"the report executed no cell with path kind {kind_name!r}; "
                f"the obligation is unmet")

    executed = {(cell.get("arm"), cell.get("path_kind"))
                for cell in (report.get("cells") or [])}
    pinned_keys = set(PINNED_REFUSALS)
    for pin in (report.get("pins") or []):
        key = (pin.get("arm"), pin.get("path_kind"))
        if key not in pinned_keys:
            problems.append(
                f"pin {key!r} is not defined by the committed pinned-refusal "
                f"table; an invented pin cannot purchase a PASS")
            continue
        expected_class, expected_outcome, expected_facts = \
            pin_expectation(PINNED_REFUSALS[key])
        if pin.get("expected_data_code") != expected_class \
                or pin.get("expected_outcome") != expected_outcome \
                or pin.get("expected_facts") != expected_facts:
            recorded_expectation = (pin.get("expected_data_code"),
                                    pin.get("expected_outcome"),
                                    pin.get("expected_facts"))
            pinned_expectation = (expected_class, expected_outcome,
                                  expected_facts)
            problems.append(
                f"pin {key!r} records {recorded_expectation!r} but the table "
                f"pins {pinned_expectation!r}")
            continue
        if key not in executed:
            problems.append(
                f"pin {key!r} was not executed; the pinned class is unverified")
            continue
        cell = observed.get(key)
        for engine in ("go", "rust"):
            record = (cell or {}).get(engine, {})
            for problem in pin_engine_problems(
                    key, engine, record, expected_class, expected_outcome,
                    expected_facts):
                problems.append(problem)
        recorded_facts = (bool((cell or {}).get("go", {}).get(
                              "publication_facts")),
                          bool((cell or {}).get("rust", {}).get(
                              "publication_facts")))
        if (pin.get("go_facts"), pin.get("rust_facts")) != recorded_facts:
            problems.append(
                f"pin {key!r} records engine evidence presence "
                f"{(pin.get('go_facts'), pin.get('rust_facts'))!r} but the "
                f"cell records {recorded_facts}; the evidence verdict must "
                f"match the reply that was actually captured")
    absent = sorted(pinned_keys - {
        (pin.get("arm"), pin.get("path_kind"))
        for pin in (report.get("pins") or [])})
    for key in absent:
        problems.append(
            f"pinned cell {key!r} is missing from the report; the "
            f"pinned-refusal table is an obligation, not a suggestion")

    summary = report.get("summary") or {}
    if summary.get("cells_expected") != len(expected):
        problems.append(
            f"summary cells_expected {summary.get('cells_expected')!r} "
            f"contradicts the derived grid size {len(expected)}")
    if summary.get("cells_executed") != len(report.get("cells") or []):
        problems.append(
            "summary cells_executed contradicts the cells array")
    if summary.get("divergences") != len(report.get("divergences") or []):
        problems.append(
            "summary divergences contradicts the divergences array")
    listed = {(entry.get("arm"), entry.get("path_kind"))
              for entry in (report.get("divergences") or [])}
    actually_bad = {key for key, cell in observed.items()
                    if not bool(cell.get("agreed"))}
    if listed != actually_bad:
        problems.append(
            f"the divergences list ({len(listed)} entries) does not name the "
            f"cells whose records disagree ({len(actually_bad)} entries)")
    if summary.get("divergences") != len(actually_bad):
        problems.append(
            f"summary divergences {summary.get('divergences')!r} contradicts "
            f"the executed records ({len(actually_bad)})")
    # The verdict term proper: everything above checks that the report
    # describes its own execution honestly, and a report can be perfectly
    # honest about a battery that does not conform.  Arm-exact refusal-class
    # parity is a contract term, so any divergence between the engines is a
    # failure, and a cell that hit its deadline is a failure rather than an
    # absence of evidence.  Without these two the gate scores PASS on a
    # partially divergent grid, which is the exact defect this gate exists
    # to catch: the earlier version here verified the divergence bookkeeping
    # and never required the count to be zero.
    if summary.get("divergences"):
        sample = "; ".join(
            f"{entry.get('arm')}/{entry.get('path_kind')}: "
            f"go={entry.get('go')} rust={entry.get('rust')}"
            for entry in list(report.get("divergences") or [])[:3])
        problems.append(
            f"{summary['divergences']} (arm, path-kind) cell(s) refuse "
            f"differently between the engines; parity requires zero "
            f"divergences. First: {sample}")
    if summary.get("hangs"):
        problems.append(
            f"{summary['hangs']} cell(s) reached their deadline without an "
            f"answer; a cell that cannot be answered is not a refusal class "
            f"and cannot be counted as agreement")
    durability = report.get("durability")
    if not isinstance(durability, dict):
        problems.append("durability rollup is missing or not an object")
    else:
        expected_durability = durability_cells()
        executed_keys = {(cell.get("arm"), cell.get("path_kind"))
                         for cell in (report.get("cells") or [])}
        want_executed = sum(1 for key in expected_durability
                            if key in executed_keys)
        if durability.get("cells_expected") != len(expected_durability) \
                or durability.get("cells_executed") != want_executed:
            recorded = (durability.get("cells_expected"),
                        durability.get("cells_executed"))
            problems.append(
                f"the durability rollup {recorded!r} contradicts the "
                f"{len(expected_durability)} durability cells of the tables "
                f"and the {want_executed} that were executed")

    if summary.get("flaky"):
        problems.append(
            f"{summary['flaky']} cell(s) changed their answer across retries; "
            f"a nondeterministic refusal is not parity evidence for either "
            f"engine")
    if summary.get("pins_expected") != PINNED_REFUSAL_COUNT:
        problems.append(
            f"summary pins_expected {summary.get('pins_expected')!r} is not "
            f"the committed {PINNED_REFUSAL_COUNT}; a report over fewer "
            f"pinned refusals than the table obliges is not a parity verdict")
    pins_ok = summary.get("pins_satisfied") == summary.get("pins_expected")
    if not pins_ok:
        problems.append(
            f"pinned refusals {summary.get('pins_satisfied')} of "
            f"{summary.get('pins_expected')} satisfied")
    elapsed = report.get("elapsed_seconds")
    budget = (report.get("method") or {}).get("run_budget_seconds")
    if isinstance(elapsed, (int, float)) and isinstance(budget, (int, float)) \
            and elapsed > budget:
        problems.append(
            f"the sweep took {elapsed}s, over the {budget}s budget; an "
            f"unbounded sweep is not a gate")
    return problems


def verdict(report, deadline=ATTEMPT_DEADLINE_SECONDS, sha256_ledger=None):
    """Return ``(ok, problems)`` for one report."""

    problems = assess_report(report, deadline=deadline,
                             sha256_ledger=sha256_ledger)
    return not problems, problems


def live_run_caller_paths(args):
    """The sweep's path-valued options, in the shape the shared defences take.

    The labels are the registry's screened set: dropping one stops the input
    refusal *and* fails the committed-report audit, which is the point of
    naming them in one place.
    """
    return (("--go", args.go),
            ("--rust", args.rust),
            ("--fixture", args.fixture),
            ("--work", args.work),
            ("--json-report", args.json_report))


def _reject_profile_rooted_provenance_note(note):
    """Refuse a provenance note that would name the operator's profile.

    ``--provenance-note`` is recorded verbatim, and the committed-report writer
    refuses any report carrying a profile path, so the note is screened here by
    the same owner the writer uses (``command_sanitize``). Refusing at the end
    of a run costs the whole sweep -- the routine pressure axis is about seven
    minutes -- and leaves the operator with a red step and no report; refusing
    at the start names the option that carried the path.
    """

    if isinstance(note, str) and personal_path_in_report({"provenance": note}):
        raise SystemExit(
            "--provenance-note names the operator's profile path, and it is "
            "recorded verbatim in a committed report; pass a note that refers "
            "to the staged binaries by a checkout-relative or scratch-dir "
            "spelling")


def live_run(args):
    # Durable-artifact policy, applied before any product starts: the report
    # records the measured binary, work and report paths, so a profile-rooted
    # input is how an operator-home path reaches a committed file.
    require_paths_outside_profile(live_run_caller_paths(args))
    _reject_profile_rooted_provenance_note(
        getattr(args, "provenance_note", None))
    for label, value in (("--go", args.go), ("--rust", args.rust),
                         ("--fixture", args.fixture)):
        if not isinstance(value, str) or not os.path.isfile(value) \
                or not os.access(value, os.X_OK):
            raise SystemExit(f"{label} must be an executable file: {value!r}")
    work = args.work
    if not isinstance(work, str) or not os.path.isabs(work):
        raise SystemExit("--work must be an absolute path")
    if os.path.exists(work) and os.listdir(work):
        raise SystemExit(f"--work must be empty or absent: {work}")
    os.makedirs(work, exist_ok=True)
    ctx = prepare_work(work, args.fixture, args.rust)
    started = time.monotonic()
    # The 483-cell grid is 34 s of serialized process work whose cells are
    # independent, so the standard suite pays for it once per cell, not once
    # per sweep.  Each concurrent worker owns a private work subdirectory;
    # see sweep() for why sharing one would corrupt the measurement.
    grid_jobs = max(1, int(getattr(args, "grid_jobs", 1) or 1))

    def _grid_slot(index):
        return prepare_work(os.path.join(work, f"grid-slot-{index}"),
                            args.fixture, args.rust)

    cells = sweep(args.go, args.rust, ctx, args.deadline, args.retries,
                  jobs=grid_jobs, ctx_factory=_grid_slot if grid_jobs > 1 else None)
    ended = time.monotonic()
    # Leave the work directory as removable as it was found: the destination
    # cells deliberately create an unreadable parent, and an operator
    # cleaning it up by hand should not need to know which mode to restore.
    # The destination cells deliberately create an unreadable parent, and
    # an operator cleaning the work directory by hand should not need to
    # know which mode to restore.  With a concurrent grid every worker owns
    # a subdirectory, so the restore has to visit each one: leaving them
    # behind makes the next sweep's clean() fail with EACCES on a tree the
    # harness itself made unremovable.
    for _root in [work] + [os.path.join(work, name)
                           for name in sorted(os.listdir(work))
                           if name.startswith("grid-slot-")]:
        for _name in ("PROBE-dest-parent-unreadable", "PROBE-dest-collision"):
            _drop_dir(os.path.join(_root, _name))
    report = build_report(args.go, args.rust, args.fixture, work,
                          args.deadline, args.retries, args.budget_seconds,
                          sys.argv, cells, started, ended,
                          provenance=args.provenance_note)
    # The third axis is opt-in per run because of its cost (design section 14's
    # budget clause): the routine gate sweeps the two target axes, the routine
    # pressure subset covers the descriptor boundaries, and the full
    # ARMS x PRESSURE_PROFILES product belongs to a milestone. A report that
    # carries the section is verified against the committed profile and pin
    # tables by assess_report, exactly like the other two axes.
    if getattr(args, "pressure", "off") != "off":
        profiles = (ROUTINE_PRESSURE_PROFILES if args.pressure == "routine"
                    else MANDATORY_PRESSURE_PROFILES)
        print(f"sweeping the descriptor-pressure axis: {len(PRESSURE_ARMS)} arms "
              f"x {len(profiles)} profiles x 2 engines x >=2 runs, under nice")
        report["pressure"] = run_pressure_sweep(
            args.go, args.rust, args.fixture, work, profiles, args.pressure,
            runs=args.pressure_runs, jobs=args.pressure_jobs)
        print(f"pressure: {report['pressure']['cells_executed']} of "
              f"{report['pressure']['cells_expected']} cells, "
              f"{report['pressure']['divergences']} divergences, "
              f"{report['pressure']['band_gap']} band-gap, "
              f"{report['pressure']['hangs']} hangs, "
              f"{report['pressure']['flaky']} flaky, "
              f"{report['pressure']['vacuous']} vacuous, "
              f"{report['pressure']['host_state']} host-state")
        for cell in report["pressure"]["cells"]:
            if cell.get("band_gap"):
                # Reported, not scored as agreement (design section 13.3): the
                # two engines stand on opposite sides of the Go arm's own
                # minimum, and each kept the class the table pins for it.
                print(f"  BANDGAP ({cell['arm']}, {cell['profile']}): "
                      f"go={_pressure_answer(cell, 'go')} "
                      f"rust={_pressure_answer(cell, 'rust')}")
        for cell in report["pressure"]["cells"]:
            for problem in cell.get("problems", []):
                print(f"  PRESSURE {problem}")
    ledger = _sha256_ledger(getattr(args, "sha256_ledger", None))
    ok, problems = verdict(report, deadline=args.deadline,
                           sha256_ledger=ledger)
    report["result"] = "PASS" if ok else "FAIL"
    report["sha256_ledger"] = (
        sanitized_path_value(args.sha256_ledger)
        if getattr(args, "sha256_ledger", None) else None)
    report_path = resolve_report_path(args)
    # The shared writer refuses the write -- leaving no file -- when a screened
    # input or any finished string value names the operator's profile, so a
    # sweep that ran on profile-rooted material is reported instead of
    # committed.
    text = write_committed_report(report_path, report, argv=sys.argv,
                                  caller_paths=live_run_caller_paths(args),
                                  indent=1)
    diverged = [entry for entry in report["divergences"]]
    for entry in diverged[:40]:
        print(f"DIV {entry['arm']}/{entry['path_kind']}: "
              f"go={entry['go']} rust={entry['rust']}")
    if len(diverged) > 40:
        print(f"... {len(diverged) - 40} more divergent cells")
    for problem in problems[:20]:
        print(f"FAIL: {problem}")
    print(f"report: {sanitized_path_value(report_path)}")
    print(f"cells executed: {report['summary']['cells_executed']} of "
          f"{report['summary']['cells_expected']} in "
          f"{report['elapsed_seconds']}s; divergences "
          f"{report['summary']['divergences']}, hangs "
          f"{report['summary']['hangs']}, flaky "
          f"{report['summary']['flaky']}, pins "
          f"{report['summary']['pins_satisfied']}/{report['summary']['pins_expected']}")
    if not ok:
        print("REFUSAL-CLASS PARITY: FAIL")
        return 1
    print("REFUSAL-CLASS PARITY: PASS")
    return 0


def _fabricated_pressure_engine_record(pin, engine, profile):
    """One engine's half of a fabricated pressure cell.

    It answers exactly what the pin obliges, or reports host state exactly
    where the profile says the environment cannot be built (the launcher's own
    descriptor floor, and three more for the Rust loader).
    """

    record = PRESSURE_PROFILE_BY_NAME[profile]
    floor = record["host_floor"] if engine == "go" else record["rust_host_floor"]
    if record["limit"] < floor:
        return {"kind": "host", "transport_code": None, "data_code": None,
                "outcome": None, "result": False, "exit_class": "host",
                "verdict": "host-unsupported", "why": "fabricated host state"}
    if pin["band_state"] == "blocked":
        # Section 13.4: this arm has no removable entry, so the honest
        # fabricated record is the blocked verdict with no class to name.
        return {"kind": "blocked", "transport_code": None, "data_code": None,
                "outcome": None, "result": False, "exit_class": None,
                "verdict": "blocked", "why": "fabricated blocked cell"}
    data_code, outcome = _pressure_expected_answer(pin, engine, profile)
    result = data_code == "RESULT"
    return {"kind": "answered",
            "transport_code": 0 if result else PRODUCT_ERROR,
            "data_code": data_code, "outcome": outcome, "result": result,
            "exit_class": "success" if result else "refused",
            "verdict": "pass", "why": "fabricated"}


def fabricated_pressure_section(profiles=None):
    """Public entry to the fabricated pressure axis.

    The kind gate's self-test battery consumes the same table-derived axis
    section this module's own controls attack, so the fabricator is exported
    once rather than copied: one authoritative implementation of what a
    conforming, executed-nothing pressure section says.
    """

    return _fabricated_pressure_section(profiles)


def _fabricated_pressure_section(profiles=None):
    """A pressure section derived from the committed tables, executing nothing.

    Like the two-axis fabricator this exists so ``--self-test`` can attack the
    pressure verifier with reports that are internally consistent and carry
    exactly one defect each.
    """

    chosen = tuple(profiles or MANDATORY_PRESSURE_PROFILES)
    cells = []
    for arm in PRESSURE_ARMS:
        for profile in chosen:
            pin = PINNED_PRESSURE_CLASSES[(arm, profile)]
            cell = {"arm": arm, "profile": profile, "attempts": 2,
                    "hung": False, "flaky": False, "vacuous": False,
                    "list_blocked": False, "close_refused": False,
                    "go_poller": [1, 1] if (
                        arm == RESOLVER_PRESSURE_ARM
                        and poller_authorized(profile)) else [0, 0]}
            for engine in ("go", "rust"):
                cell[engine] = _fabricated_pressure_engine_record(
                    pin, engine, profile)
            if pin["list_required"]:
                # Design section 13.4: no public-method producer of a listable
                # entry exists, so the honest cell is the blocked one. Claiming
                # it removed something would be the defect.
                cell["list_blocked"] = True
            cell["agreed"] = (not cell["list_blocked"]) and _pressure_answer(
                cell, "go") == _pressure_answer(cell, "rust")
            _, cell["band_gap"] = _pressure_cell_divergence(pin, cell)
            cells.append(cell)
    rollup = pressure_rollup(cells, chosen)
    rollup["failed"] = 0
    section = {"mode": PRESSURE_FABRICATED_MODE,
               "cells": cells, "runs": 2}
    section.update(rollup)
    return section


def _sync_pressure_summary(report):
    """Recompute the pressure rollup after a control mutated a cell.

    A pressure control that left the counters stale would be testing the
    counter check instead of the term it names, the same reason the two-axis
    reports are re-synced.
    """

    pressure = report.get("pressure")
    if not isinstance(pressure, dict):
        return report
    cells = pressure.get("cells") or []
    pressure.update(pressure_rollup(cells, pressure.get("profiles")))
    pressure["failed"] = 0
    return report


def _fabricated_report():
    """Build a report from the tables without executing anything.

    Each engine is answered with the pinned class where the table pins one and
    with the parity default otherwise; Go mirrors Rust, which is what a
    conforming pair produces.
    """

    cells = []
    for arm_name, kind_name in expected_cells():
        _name, method, slot, _build = ARM_BY_NAME[arm_name]
        pinned = PINNED_REFUSALS.get((arm_name, kind_name))
        class_name, pinned_outcome, pinned_facts = pin_expectation(
            pinned if pinned is not None else "invalid_argument")
        if pinned is None:
            pinned_outcome = "read_only_failure"
        transport = "RESULT" if pinned is None \
            and kind_name.startswith("regular-valid") else PRODUCT_ERROR
        record = {
            "arm": arm_name, "path_kind": kind_name, "attempts": 1,
            "flaky": False, "hung": False, "agreed": True}
        if transport != "RESULT" and pinned_outcome is None:
            pinned_outcome = "read_only_failure"
        # A conforming pair answers a durability cell with its evidence, so
        # the baseline carries facts exactly where the table demands them;
        # each durability control then removes or corrupts one side of that.
        facts = None
        evidence = None
        if pinned_facts == "required":
            container = ("removals_publication_failure"
                         if arm_name == "removals_output" else "publication")
            facts = {"complete": True, "destination_visible": True,
                     "temporary_removed": True, "sha256_well_formed": True,
                     "outcome_unknown": True, "container": container}
            evidence = {"publication": {
                "outcome": "outcome_unknown",
                "publication_policy": "fail_if_exists", "path": "/tmp/out",
                "stage": "sync output directory", "destination_visible": True,
                "temporary_removed": True, "sha256": "d" * 64}}
        for engine in ("go", "rust"):
            record[engine] = {
                "engine": engine, "arm": arm_name, "path_kind": kind_name,
                "method": method, "slot": slot, "kind": "answered",
                "transport_code": transport, "data_code": class_name,
                "outcome": "RESULT" if transport == "RESULT"
                else pinned_outcome,
                "exit_status": 0, "elapsed_ms": 12.0,
                "publication_facts": None if facts is None else dict(facts),
                "publication_evidence": None if evidence is None
                else json.loads(json.dumps(evidence)),
                "request": json.dumps({"jsonrpc": "2.0", "method": method},
                                      separators=(",", ":"))}
        cells.append(record)
    pins = []
    for (arm_name, kind_name), value in sorted(PINNED_REFUSALS.items()):
        want_code, want_outcome, want_facts = pin_expectation(value)
        want_facts_present = want_facts == "required"
        pins.append({"arm": arm_name, "path_kind": kind_name,
                     "expected_data_code": want_code,
                     "expected_outcome": want_outcome,
                     "expected_facts": want_facts, "executed": True,
                     "go_data_code": want_code, "rust_data_code": want_code,
                     "go_outcome": want_outcome,
                     "rust_outcome": want_outcome,
                     "go_facts": want_facts_present,
                     "rust_facts": want_facts_present})
    return {
        "schema": REPORT_SCHEMA, "git_head": "91ae2a4211111111111111111111111111111111",
        "checkout_root": None, "command": ["v4/cli/check_refusal_class_parity.py"],
        "platform": {"system": "Linux"},
        "binaries": {
            "go": {"path": "/tmp/go/iprange", "sha256": "a" * 64,
                   "implementation": "go"},
            "rust": {"path": "/tmp/rust/iprange", "sha256": "b" * 64,
                     "implementation": "rust"},
            "fixture_tool": {"path": "/tmp/rust/v4-fixture",
                             "sha256": "c" * 64, "implementation": "rust"}},
        "grid": {"arms": ARM_NAMES, "path_kinds": kind_names(),
                 "mandatory_path_kinds": list(MANDATORY_PATH_KINDS),
                 "mandatory_arms": list(MANDATORY_ARMS),
                 "pinned_table_count": len(PINNED_REFUSALS),
                 "pinned_table_sha256": pinned_table_fingerprint(),
                 "cells_expected": len(expected_cells())},
        "method": {"attempt_deadline_seconds": ATTEMPT_DEADLINE_SECONDS,
                   "retries": RETRIES,
                   "run_budget_seconds": RUN_BUDGET_SECONDS},
        "elapsed_seconds": 12.0,
        "cells": cells, "pins": pins,
        "summary": {"cells_expected": len(expected_cells()),
                    "cells_executed": len(cells), "agreements": len(cells),
                    "divergences": 0, "hangs": 0, "flaky": 0,
                    "pins_expected": len(PINNED_REFUSALS),
                    "pins_satisfied": len(PINNED_REFUSALS)},
        "durability": durability_rollup(cells),
        "pressure": _fabricated_pressure_section(ROUTINE_PRESSURE_PROFILES),
        "divergences": [],
    }


def _sync_summary(report):
    """Recompute the counters a doctored report may have left behind."""

    cells = report.get("cells") or []
    bad = [c for c in cells if not c.get("agreed")]
    report["summary"]["cells_executed"] = len(cells)
    report["summary"]["agreements"] = len(cells) - len(bad)
    report["summary"]["divergences"] = len(
        [c for c in cells if not c.get("agreed") and not c.get("hung")])
    # A doctored report must be internally consistent, or a control meant to
    # isolate one defect is really testing a counter mismatch.
    report["summary"]["hangs"] = len(
        [c for c in cells if c.get("hung")])
    report["summary"]["flaky"] = sum(1 for c in cells if c.get("flaky"))
    report["summary"]["divergences"] = len(bad)
    report["summary"]["cells_expected"] = len(expected_cells())
    report["grid"]["cells_expected"] = len(expected_cells())
    report["durability"] = durability_rollup(cells)
    report["divergences"] = [
        {"arm": c["arm"], "path_kind": c["path_kind"],
         "go": [c["go"]["kind"], c["go"]["transport_code"],
                c["go"]["data_code"], c["go"]["outcome"]],
         "rust": [c["rust"]["kind"], c["rust"]["transport_code"],
                  c["rust"]["data_code"], c["rust"]["outcome"]]}
        for c in bad]
    return report


# The number of controls this harness runs, committed so that a control cannot
# be deleted to leave a shorter self-test that still looks green. It covers the
# doctored-report cases and every control that assesses a report or mutates the
# tables directly; adding or removing one changes this constant in the same
# change, and a run whose total drifts from it fails.
SELF_TEST_CASES_TOTAL = 66


def _self_test():
    """Prove the verifier rejects doctored parity reports."""

    # Declared with the control list so every control helper, including the
    # ones that assess a report directly, can record a failure.
    failures = 0
    cases = []
    # Controls that assess a report directly (rather than through the case
    # list) count toward the published total, so the reported number cannot
    # drift below the controls that actually ran.
    tally = {"controls": 0}

    baseline = _fabricated_report()
    cases.append(("genuine report passes", baseline, False))

    def one(mutate, description, expect_problem):
        report = copy.deepcopy(baseline)
        mutate(report)
        cases.append((description, _sync_summary(report), expect_problem))

    def inject_divergence(report):
        """A synthetic Go/Rust disagreement dressed up as a passing cell."""
        for cell in report["cells"]:
            if cell["arm"] == "reader.open" and cell["path_kind"] == "junk":
                cell["rust"]["data_code"] = "wrong_state"
                cell["rust"]["transport_code"] = PRODUCT_ERROR
                return

    one(inject_divergence, "injected synthetic divergence must FAIL", True)

    # The control above is caught by the bookkeeping checks, because it leaves
    # the cell claiming agreement.  A report can be perfectly honest about a
    # divergent grid, and only the parity term of the verdict can catch that;
    # these controls build self-consistent reports whose sole defect is the
    # one under test.
    def one_clean(mutate, description, needle):
        nonlocal failures
        report = copy.deepcopy(baseline)
        mutate(report)
        _sync_summary(report)
        problems = assess_report(report)
        hit = [problem for problem in problems if needle in problem]
        ok = bool(hit) and len(problems) == 1
        tally["controls"] += 1
        print(f"{'ok  ' if ok else 'BAD '} {description:58} "
              f"problems={len(problems)} needle_hit={bool(hit)}")
        if not ok:
            failures += 1
            for problem in problems[:3]:
                print(f"       {problem}")

    def honest_divergence(report):
        """One cell genuinely refuses differently, reported truthfully."""
        for cell in report["cells"]:
            if cell["arm"] == "database.reclaim" \
                    and cell["path_kind"] == "zero-length-live":
                cell["rust"]["data_code"] = "checksum_mismatch"
                cell["agreed"] = False
                return

    one_clean(honest_divergence,
              "honest single divergence must FAIL on parity alone",
              "parity requires zero")

    def honest_hang(report):
        for cell in report["cells"]:
            if cell["arm"] == "export" and cell["path_kind"] == "fifo":
                # Only the hang flag changes: touching the recorded answers
                # would add a second defect and the control would no longer
                # isolate the term under test.
                cell["hung"] = True
                cell["attempts"] = 2
                return

    one_clean(honest_hang, "honest hang must FAIL, not count as agreement",
              "reached their deadline")

    def honest_flake(report):
        for cell in report["cells"]:
            if cell["arm"] == "database.info" \
                    and cell["path_kind"] == "missing":
                cell["flaky"] = True
                cell["attempts"] = 3
                return

    one_clean(honest_flake, "honest flake must FAIL as nondeterminism",
              "changed their answer across retries")

    def empty_cells(report):
        report["cells"] = []

    one(empty_cells, "report of zero executed cells must FAIL", True)

    def drop_fold_shape(report):
        report["cells"] = [c for c in report["cells"]
                           if c["path_kind"] != "sidecar-folded"]

    one(drop_fold_shape,
        "deleting the sidecar-fold shape from the executed cells must FAIL",
        True)

    def drop_fold_pin_entry(report):
        report["pins"] = [p for p in report["pins"]
                          if p["path_kind"] != "sidecar-folded"]

    one(drop_fold_pin_entry, "deleting the fold pin record must FAIL", True)

    def rewrite_fold_class(report):
        for cell in report["cells"]:
            if cell["path_kind"] == "sidecar-folded" \
                    and cell["arm"] == "validate.live":
                for engine in ("go", "rust"):
                    cell[engine]["data_code"] = "format_invalid"
        for pin in report["pins"]:
            if pin["path_kind"] == "sidecar-folded" \
                    and pin["arm"] == "validate.live":
                pin["expected_data_code"] = "format_invalid"
                pin["go_data_code"] = "format_invalid"
                pin["rust_data_code"] = "format_invalid"

    one(rewrite_fold_class, "pinned class rewritten away from the fold must FAIL",
        True)

    def drop_arm_cells(report):
        report["cells"] = [c for c in report["cells"]
                           if c["arm"] != "feeds.rename"]

    one(drop_arm_cells, "an arm dropped from the sweep must FAIL", True)

    def drop_kind_cells(report):
        report["cells"] = [c for c in report["cells"]
                           if c["path_kind"] != "hardlink-live"]

    one(drop_kind_cells, "a mandatory path kind dropped from the sweep must FAIL",
        True)

    def hang_reported_as_agreement(report):
        for cell in report["cells"]:
            if cell["arm"] == "reader.open" and cell["path_kind"] == "fifo":
                cell["go"]["kind"] = "timeout"
                cell["go"]["transport_code"] = None
                cell["go"]["data_code"] = None
                cell["go"]["outcome"] = None
                cell["go"]["elapsed_ms"] = 4100.0
                return

    one(hang_reported_as_agreement, "a hang recorded as an answer must FAIL",
        True)

    def unhashed(report):
        report["binaries"]["rust"]["sha256"] = "not-a-digest"

    one(unhashed, "binary without a measured sha256 must FAIL", True)

    def foreign_label(report):
        report["binaries"]["go"]["implementation"] = "rust"

    one(foreign_label, "a foreign implementation label must FAIL", True)

    def dead_git_head(report):
        report["git_head"] = "0" * 40

    one(dead_git_head, "git_head of all zeros must FAIL", True)

    def missing_git_head(report):
        del report["git_head"]

    one(missing_git_head, "git_head deleted must FAIL", True)

    def flip_pinned_outcome(report):
        for cell in report["cells"]:
            if cell["arm"] == "feeds.delete" \
                    and cell["path_kind"] == "live-membership":
                cell["go"]["outcome"] = "not_started"
                return
        raise AssertionError("the pinned outcome cell is missing")

    one(flip_pinned_outcome,
        "a read-only refusal reported as not_started must FAIL", True)

    def dead_pin_expectation(report):
        for pin in report["pins"]:
            if pin["arm"] == "feeds.delete":
                pin["expected_outcome"] = None
                return

    one(dead_pin_expectation,
        "a pin silently loosened against the table must FAIL", True)

    def slow_but_answered(report):
        for cell in report["cells"]:
            if cell["arm"] == "export" and cell["path_kind"] == "dir":
                cell["go"]["elapsed_ms"] = 9000.0
                return

    one(slow_but_answered,
        "a reply past the deadline recorded as answered must FAIL", True)

    # --- publication-durability controls.  Each mutation is applied to both
    # engines or to none, so the engines keep agreeing and the only verdict
    # term that can fire is the evidence term under test.  ``one_terms`` names
    # the terms it expects: an evidence control that also woke the parity or
    # bookkeeping terms would be testing something else.
    DURABILITY_CELL = ("metadata.file_delivery", "dest-parent-unreadable")
    FACTS_NEEDLE = "without the publication evidence"
    ABSENT_NEEDLE = "carried publication evidence"
    OUTCOME_NEEDLE = "answered outcome"

    def one_terms(mutate, description, *needles):
        """Require a failure whose every problem belongs to a named term."""

        nonlocal failures
        report = copy.deepcopy(baseline)
        mutate(report)
        _sync_summary(report)
        problems = assess_report(report)
        hit = all(any(needle in problem for problem in problems)
                  for needle in needles)
        unexplained = [problem for problem in problems
                       if not any(needle in problem for needle in needles)]
        ok = bool(problems) and hit and not unexplained
        tally["controls"] += 1
        print(f"{'ok  ' if ok else 'BAD '} {description:58} "
              f"problems={len(problems)} needles_hit={bool(needles) and hit}"
              f" unexplained={len(unexplained)}")
        if not ok:
            failures += 1
            for problem in (unexplained or problems)[:3]:
                print(f"       {problem}")

    def durability_cell(report):
        for cell in report["cells"]:
            if (cell["arm"], cell["path_kind"]) == DURABILITY_CELL:
                return cell
        raise AssertionError("the durability cell is missing from the grid")

    def pin_of(report, key):
        for pin in report["pins"]:
            if (pin["arm"], pin["path_kind"]) == key:
                return pin
        raise AssertionError("the durability pin is missing from the report")

    def read_only_after_visibility(report):
        """Both engines keep the class and the facts but retract the
        unknown outcome: the destination is treated as never delivered."""

        cell = durability_cell(report)
        for engine in ("go", "rust"):
            cell[engine]["outcome"] = "read_only_failure"
        pin = pin_of(report, DURABILITY_CELL)
        pin["go_outcome"] = pin["rust_outcome"] = "read_only_failure"

    one_terms(read_only_after_visibility,
              "read_only_failure after visibility must FAIL", OUTCOME_NEEDLE)

    def facts_stripped_both(report):
        """The class and outcome stay right; the facts simply are not there."""

        cell = durability_cell(report)
        for engine in ("go", "rust"):
            cell[engine]["publication_facts"] = None
            cell[engine]["publication_evidence"] = None
        pin = pin_of(report, DURABILITY_CELL)
        pin["go_facts"] = pin["rust_facts"] = False

    one_terms(facts_stripped_both, "omitting the publication facts must FAIL",
              FACTS_NEEDLE)

    def one_sided_facts(report):
        """An unpinned cell where only one engine carries the evidence.

        Using a cell the evidence table does not constrain keeps the parity
        term isolated: the sole defect is that the two engines described the
        same refusal differently.
        """

        key = ("database.reclaim", "junk")
        cell = next(c for c in report["cells"]
                    if (c["arm"], c["path_kind"]) == key)
        cell["rust"]["publication_facts"] = {
            "complete": True, "destination_visible": True,
            "temporary_removed": True, "sha256_well_formed": True,
            "outcome_unknown": True, "container": "publication"}
        cell["agreed"] = False

    one_clean(one_sided_facts,
              "facts on one engine only must FAIL on parity",
              "parity requires zero")

    def facts_added_pre_visibility(report):
        key = ("metadata.file_delivery", "dest-collision")
        cell = next(c for c in report["cells"]
                    if (c["arm"], c["path_kind"]) == key)
        for engine in ("go", "rust"):
            cell[engine]["publication_facts"] = {
                "complete": True, "destination_visible": True,
                "temporary_removed": True, "sha256_well_formed": True,
                "outcome_unknown": True, "container": "publication"}
        pin = pin_of(report, key)
        pin["go_facts"] = pin["rust_facts"] = True

    one_terms(facts_added_pre_visibility,
              "claiming unknown durability before visibility must FAIL",
              ABSENT_NEEDLE)

    def pin_loosened_evidence(report):
        """The cheapest reviewer mutation: drop the evidence obligation."""

        pin = pin_of(report, DURABILITY_CELL)
        pin["expected_facts"] = None
        pin["go_facts"] = pin["rust_facts"] = False
        cell = durability_cell(report)
        for engine in ("go", "rust"):
            cell[engine]["publication_facts"] = None
            cell[engine]["publication_evidence"] = None

    one_clean(pin_loosened_evidence,
              "a durability pin loosened against the table must FAIL",
              "but the table pins")

    def durability_cell_deleted(report):
        report["cells"] = [c for c in report["cells"]
                           if c["path_kind"] != "dest-parent-unreadable"]
        report["pins"] = [p for p in report["pins"]
                          if p["path_kind"] != "dest-parent-unreadable"]

    one(durability_cell_deleted,
        "deleting the post-visibility durability cells must FAIL", True)

    # The rollup is recomputed by the consistency pass, so the lie has to be
    # applied after it, the way a forged artifact would be written by hand.
    forged = copy.deepcopy(baseline)
    _sync_summary(forged)
    forged["durability"] = {"cells_expected": 0, "cells_executed": 0,
                            "cells_with_evidence": 0}
    problems = assess_report(forged)
    rejected = any("durability rollup" in problem for problem in problems)
    tally["controls"] += 1
    print(f"{'ok  ' if rejected else 'BAD '} a durability rollup that lies "
          f"must FAIL                             rejected={rejected} "
          f"expected_rejected=True")
    if not rejected:
        failures += 1
        for problem in problems[:3]:
            print(f"       {problem}")

    # A table-level deletion (as opposed to a doctored report) is the cheapest
    # way to make a sweep smaller, so it gets its own control: the mandatory
    # obligations must still be present in PATH_KINDS.
    global PATH_KINDS
    saved_kinds = PATH_KINDS
    try:
        PATH_KINDS = tuple(k for k in PATH_KINDS if k != "sidecar-folded")
        problems = assess_report(copy.deepcopy(baseline))
        rejected = bool(problems)
        print(f"{'ok  ' if rejected else 'BAD '} deleting the fold shape from "
              f"the path-kind table must FAIL           rejected={rejected} "
              f"expected_rejected=True")
        if not rejected:
            failures += 1
    finally:
        PATH_KINDS = saved_kinds
    # --- table-shrinking controls. Each mutates the committed tables the
    # verifier re-derives its obligations from, runs the verifier on a report
    # that still describes the full sweep, and requires the specific problem.
    # The mutations go through globals() so the tables are restored exactly,
    # and each restores before the next control can observe it.
    def with_tables(mutations, description, needle):
        """Apply table mutations, require one verifier problem, restore."""

        nonlocal failures
        saved = {name: globals()[name] for name, _value in mutations}
        try:
            for name, value in mutations:
                globals()[name] = value
            problems = assess_report(copy.deepcopy(baseline))
        finally:
            for name, value in saved.items():
                globals()[name] = value
        hit = any(needle in problem for problem in problems)
        tally["controls"] += 1
        print(f"{'ok  ' if hit else 'BAD '} {description:58} "
              f"needle_hit={hit} problems={len(problems)}")
        if not hit:
            failures += 1
            for problem in problems[:3]:
                print(f"       {problem}")

    def with_pins(mutate, dropped_key, loosened, description, needle):
        """Mutate the pinned table *and* the report's pin records.

        A pin can only leave the verdict by leaving both, which is the
        mutation the committed count and digest exist to catch; half a
        mutation is already caught by the record-versus-table check and would
        prove nothing about the anchors.
        """

        nonlocal failures
        saved = dict(PINNED_REFUSALS)
        try:
            mutate(PINNED_REFUSALS)
            report = copy.deepcopy(baseline)
            if dropped_key is not None:
                report["pins"] = [pin for pin in report["pins"]
                                  if (pin["arm"], pin["path_kind"])
                                  != dropped_key]
            for key, value in (loosened or {}).items():
                for pin in report["pins"]:
                    if (pin["arm"], pin["path_kind"]) == key:
                        code, outcome, facts = pin_expectation(value)
                        pin["expected_data_code"] = code
                        pin["expected_outcome"] = outcome
                        pin["expected_facts"] = facts
            _sync_summary(report)
            problems = assess_report(report)
        finally:
            PINNED_REFUSALS.clear()
            PINNED_REFUSALS.update(saved)
        hit = any(needle in problem for problem in problems)
        tally["controls"] += 1
        print(f"{'ok  ' if hit else 'BAD '} {description:58} "
              f"needle_hit={hit} problems={len(problems)}")
        if not hit:
            failures += 1
            for problem in problems[:3]:
                print(f"       {problem}")

    # A mapping pin is the interesting sample: it is the shape a reviewer can
    # loosen to a bare class, which silently drops the outcome and evidence
    # requirements while keeping the class.
    sample_key = max(PINNED_REFUSALS,
                     key=lambda key: isinstance(PINNED_REFUSALS[key], dict))
    sample_pin = PINNED_REFUSALS[sample_key]
    bare_pin = sample_pin["data_code"] if isinstance(sample_pin, dict) \
        else sample_pin

    def drop_pin(table, key=sample_key):
        del table[key]

    with_pins(drop_pin, sample_key, None,
              "deleting a pin (table and report) must FAIL",
              "not the committed")

    def loosen_pin(table, key=sample_key, value=bare_pin):
        table[key] = value

    with_pins(loosen_pin, None, {sample_key: bare_pin},
              "loosening a pin to a bare class must FAIL", "digests to")

    with_tables([("PATH_KINDS", tuple(k for k in PATH_KINDS
                                      if k != "live-direct"))],
                "deleting a path kind from the table must FAIL",
                "is an obligation but is missing from the table")
    with_tables([("MANDATORY_PATH_KINDS",
                  tuple(k for k in MANDATORY_PATH_KINDS
                        if k != "live-direct"))],
                "un-naming a path kind obligation must FAIL",
                "is swept but is not an obligation")
    with_tables([("ARM_NAMES", [a for a in ARM_NAMES
                                if a != "feeds.import_target"]),
                 ("ARM_BY_NAME", {k: v for k, v in ARM_BY_NAME.items()
                                  if k != "feeds.import_target"})],
                "deleting an arm from the table must FAIL",
                "is an obligation but is missing from the table")
    with_tables([("MANDATORY_ARMS", tuple(a for a in MANDATORY_ARMS
                                          if a != "feeds.import_target"))],
                "un-naming an arm obligation must FAIL",
                "is swept but is not an obligation")

    # The binary binding: the verdict must be attributable to the artifacts
    # the wave staged, so a recorded digest the ledger does not list cannot
    # carry it. The control hands the verifier a ledger that lists every
    # recorded binary except the Go product.
    def ledger_control(report):
        listed = {record["sha256"]
                  for name, record in report["binaries"].items()
                  if name != "go"}
        return assess_report(report, sha256_ledger=listed)

    report = copy.deepcopy(baseline)
    problems = ledger_control(report)
    hit = any("not in the staged --sha256-ledger" in problem
              for problem in problems)
    tally["controls"] += 1
    print(f"{'ok  ' if hit else 'BAD '} a binary digest absent from the "
          f"ledger must FAIL{'':19} needle_hit={hit} "
          f"problems={len(problems)}")
    if not hit:
        failures += 1
        for problem in problems[:3]:
            print(f"       {problem}")

    # --- pressure-axis controls. The third axis is anchored by a committed
    # count and digest and by a mandatory-profile list checked in both
    # directions, so each of those anchors gets its own control; the rest
    # attack the terms a report can lie about (vacuous, blocked, poller,
    # wedge, host state, and the band gap design section 13.3 says must not be
    # reported as parity).
    PRESSURE_SAMPLE_CELL = ("direct.replace", "b06h0-before-normal")

    def pressure_cell_in(report, key):
        for cell in report["pressure"]["cells"]:
            if (cell["arm"], cell["profile"]) == key:
                return cell
        raise AssertionError(f"the pressure cell {key} is missing")

    def with_pressure_table(mutate, description, needle):
        """Mutate the pinned pressure table, require the anchor's problem."""

        nonlocal failures
        saved = dict(PINNED_PRESSURE_CLASSES)
        try:
            mutate(PINNED_PRESSURE_CLASSES)
            problems = assess_report(copy.deepcopy(baseline))
        finally:
            PINNED_PRESSURE_CLASSES.clear()
            PINNED_PRESSURE_CLASSES.update(saved)
        hit = any(needle in problem for problem in problems)
        tally["controls"] += 1
        print(f"{'ok  ' if hit else 'BAD '} {description:58} "
              f"needle_hit={hit} problems={len(problems)}")
        if not hit:
            failures += 1
            for problem in problems[:3]:
                print(f"       {problem}")

    def with_pressure_report(mutate, description, needle):
        """Mutate one pressure cell truthfully, re-sync, require the term."""


        nonlocal failures
        report = copy.deepcopy(baseline)
        mutate(report)
        _sync_summary(report)
        _sync_pressure_summary(report)
        problems = assess_report(report)
        hit = any(needle in problem for problem in problems)
        tally["controls"] += 1
        print(f"{'ok  ' if hit else 'BAD '} {description:58} "
              f"needle_hit={hit} problems={len(problems)}")
        if not hit:
            failures += 1
            for problem in problems[:3]:
                print(f"       {problem}")

    def hostile_worker_claimed_success(report):
        # Section 9: a worker arm under a poisoned /dev/null must refuse to
        # spawn, and io/read_only_failure is the pinned class. Answering
        # success there means the spawn handed the child a stream that can
        # never drain, which is the wedge this axis exists to remove.
        target = pressure_cell_in(report, ("validate(worker)", "b64h0-before-fifo"))
        target["rust"] = {"kind": "answered", "transport_code": 0,
                          "data_code": "RESULT", "outcome": "RESULT",
                          "exit_class": "success"}

    with_pressure_report(hostile_worker_claimed_success,
                         "a hostile worker cell that claimed success must FAIL",
                         "hostile null device answered")

    def hostile_one_engine_only(report):
        # The hostile class must be the same in both engines; one engine
        # answering success is the section 9 defect, not a band gap.
        target = pressure_cell_in(report, ("validate(worker)", "b64h0-before-fifo"))
        target["go"] = {"kind": "answered", "transport_code": 0,
                        "data_code": "RESULT", "outcome": "RESULT",
                        "exit_class": "success"}

    with_pressure_report(hostile_one_engine_only,
                         "a hostile cell answered success by one engine must FAIL",
                         "hostile null device answered")

    def blocked_cell_claimed_coverage(report):
        # Section 13.4 with the softer terms disabled: an engine that reports a
        # completed removal for the arm that has nothing to remove.
        target = pressure_cell_in(report, ("maintenance.remove", "b64h0-before-fifo"))
        target["list_blocked"] = False
        target["rust"] = {"kind": "answered", "transport_code": 0,
                          "data_code": "RESULT", "outcome": "RESULT",
                          "result": True, "exit_class": "success"}

    with_pressure_report(blocked_cell_claimed_coverage,
                         "a blocked arm claiming a completed removal must FAIL",
                         "records as blocked")

    def hostile_cell_leaked_the_poller(report):
        # The hostile cells fall through to the descriptor-set assertion, so a
        # worker arm that reached the runtime poller under a poisoned
        # /dev/null is still a section 10 failure.
        target = pressure_cell_in(report, ("validate(worker)", "b64h0-before-fifo"))
        target["go_poller"] = [1, 1]

    with_pressure_report(hostile_cell_leaked_the_poller,
                         "a hostile worker cell that leaked the poller must FAIL",
                         "poller-free")


    with_pressure_table(
        lambda table: table.pop(PRESSURE_SAMPLE_CELL, None),
        "deleting a pinned pressure cell must FAIL", "not the committed")

    def loosen_pressure_pin(table, key=PRESSURE_SAMPLE_CELL):
        table[key] = [table[key]["data_code"], table[key]["outcome"]]

    with_pressure_table(loosen_pressure_pin,
                        "loosening a pressure pin to a bare class must FAIL",
                        "not a mapping")

    def drop_poller_term(table, key=(RESOLVER_PRESSURE_ARM,
                                     ROUTINE_PRESSURE_PROFILES[0])):
        table[key] = dict(table[key], poller="absent")

    with_pressure_table(drop_poller_term,
                        "dropping the resolver's poller obligation must FAIL",
                        "digests to")

    with_tables([("PRESSURE_PROFILES",
                  tuple(row for row in PRESSURE_PROFILES
                        if row["name"] != ROUTINE_PRESSURE_PROFILES[0]))],
                "deleting a pressure environment must FAIL",
                "is an obligation but is missing from PRESSURE_PROFILES")
    with_tables([("MANDATORY_PRESSURE_PROFILES",
                  tuple(name for name in MANDATORY_PRESSURE_PROFILES
                        if name != ROUTINE_PRESSURE_PROFILES[0]))],
                "un-naming a pressure obligation must FAIL",
                "is swept but is not an obligation")
    with_tables([("ROUTINE_PRESSURE_PROFILES",
                  ROUTINE_PRESSURE_PROFILES + ("b99h0-before-normal",))],
                "a routine profile outside the table must FAIL",
                "the routine subset must be a subset")
    with_tables([("PRESSURE_ARMS",
                  tuple(arm for arm in PRESSURE_ARMS
                        if arm != "recovery.inspect(worker)"))],
                "deleting a pressure arm must FAIL",
                "is not a swept arm")

    def relabel_the_routine_axis(report):
        # The 12-profile subset, claimed as the milestone's full product: the
        # cells stay honest, only the coverage sentence about them lies.
        report["pressure"]["mode"] = PRESSURE_MODE_FULL

    with_pressure_report(relabel_the_routine_axis,
                         "a routine sweep labelled full must FAIL",
                         "are the 'routine' set")

    def drop_the_axis_label(report):
        report["pressure"].pop("mode", None)

    with_pressure_report(drop_the_axis_label,
                         "a pressure section with no axis label must FAIL",
                         "is missing 'mode'")

    def label_the_command_line_denies(report):
        # ``command`` is written by the shared committed-report writer from the
        # operator's argv, so it is the record's own account of the run.
        report["pressure"]["mode"] = PRESSURE_MODE_ROUTINE
        report["command"] = ["v4/cli/check_refusal_class_parity.py",
                             "--pressure", "full"]

    with_pressure_report(label_the_command_line_denies,
                         "an axis label the command line contradicts must FAIL",
                         "asked for --pressure 'full'")

    def delete_the_pressure_axis(report):
        # The demonstrated forgery: a sweep whose own command line says
        # --pressure routine, filed with the block removed. The axis is an
        # obligation of the verdict, so its absence is the problem -- the
        # grid below is not a lesser claim the report may retreat to.
        report.pop("pressure", None)

    one_clean(delete_the_pressure_axis,
              "a deleted pressure section must FAIL on the axis alone",
              "carries no pressure member")

    def frame_asks_another_method(report):
        # The cell claims one arm and carries the request frame of another:
        # the recorded bytes must be the request that produced the answer,
        # not any string that mentions a method.
        for cell in report["cells"]:
            if cell["arm"] == "reader.open" and cell["path_kind"] == "junk":
                cell["go"]["request"] = json.dumps(
                    {"jsonrpc": "2.0", "id": 1,
                     "method": "iprange.v1.system.describe", "params": {}},
                    separators=(",", ":"))
                return

    one_clean(frame_asks_another_method,
              "a frame whose method contradicts the cell must FAIL",
              "cannot certify another method's refusal class")

    def claim_vacuous_as_coverage(report):
        cell = pressure_cell_in(report, PRESSURE_SAMPLE_CELL)
        cell["vacuous"] = True

    with_pressure_report(claim_vacuous_as_coverage,
                         "a vacuous cell reported as coverage must FAIL",
                         "not coverage (design section 14)")

    def claim_blocked_as_coverage(report):
        cell = pressure_cell_in(report, ("maintenance.remove",
                                         ROUTINE_PRESSURE_PROFILES[0]))
        cell["agreed"] = True

    with_pressure_report(claim_blocked_as_coverage,
                         "a blocked removal reported as coverage must FAIL",
                         "blocked cell of design section 13.4")

    def leak_the_poller(report):
        cell = pressure_cell_in(report, PRESSURE_SAMPLE_CELL)
        cell["go_poller"] = [1, 1]

    with_pressure_report(leak_the_poller,
                         "a poller in a non-resolver arm must FAIL",
                         "must be poller-free")

    def record_a_wedge_as_answer(report):
        cell = pressure_cell_in(report, PRESSURE_SAMPLE_CELL)
        for engine in ("go", "rust"):
            cell[engine]["kind"] = "pending"

    with_pressure_report(record_a_wedge_as_answer,
                         "a pressure wedge must FAIL, not name a class",
                         "a wedge is a failure")

    def fake_host_state(report):
        cell = pressure_cell_in(report, PRESSURE_SAMPLE_CELL)
        cell["go"] = {"kind": "host", "transport_code": None,
                      "data_code": None, "outcome": None, "result": False,
                      "exit_class": "host", "verdict": "host-unsupported",
                      "why": "claimed the table could not be built"}

    with_pressure_report(fake_host_state,
                         "host state where the band is buildable must FAIL",
                         "never a substitute for an answer")

    def hide_the_missing_cell(report):
        report["pressure"]["cells"] = [
            cell for cell in report["pressure"]["cells"]
            if (cell["arm"], cell["profile"]) != PRESSURE_SAMPLE_CELL]

    with_pressure_report(hide_the_missing_cell,
                         "a missing pressure cell must FAIL",
                         "was never executed")

    def declare_band_parity(report):
        """The Go writer answers success two bands under its own floor.

        Design section 13.3 forbids reporting the writer and worker families
        as band-parity with the reference; the cheapest forgery is to answer
        the Rust success in the Go column and keep the cell agreed.
        """

        cell = pressure_cell_in(report, PRESSURE_SAMPLE_CELL)
        cell["go"] = {"kind": "answered", "transport_code": 0,
                      "data_code": "RESULT", "outcome": "RESULT",
                      "result": True, "exit_class": "success",
                      "verdict": "pass", "why": "claimed parity"}

    with_pressure_report(declare_band_parity,
                         "Go success claimed below its own floor must FAIL",
                         "the reference pins")

    def hide_a_real_divergence(report):
        cell = pressure_cell_in(report, ("reader-open-close-immutable",
                                         "b05h0-before-normal"))
        cell["go"] = dict(cell["go"], kind="answered", transport_code=PRODUCT_ERROR,
                          data_code="io", outcome="read_only_failure",
                          result=False, exit_class="refused")

    with_pressure_report(hide_a_real_divergence,
                         "a divergence reported as agreement must FAIL",
                         "diverged between engines")

    # --- the band gap of design section 13.3. The Go writer and worker arms
    # reach success one or two bands above the reference, so a cell where
    # exactly one engine completed is the expected result of the sweep: it is
    # reported, and it is not scored as a divergence. The allowance is bounded
    # by the pinned class each engine owes for its own band -- these controls
    # pin both the allowance and the bound.

    def the_first_band_gap_cell(report):
        for candidate in report["pressure"]["cells"]:
            if candidate.get("band_gap"):
                return candidate
        raise AssertionError("the committed tables produced no band-gap cell;"
                             " the allowance has nothing to be about")

    band_gap_cells = [cell for cell in baseline["pressure"]["cells"]
                      if cell.get("band_gap")]
    scored = [problem for problem in assess_report(copy.deepcopy(baseline))
              if "diverged between engines" in problem]
    honest = (bool(band_gap_cells)
              and baseline["pressure"]["divergences"] == 0
              and baseline["pressure"]["band_gap"] == len(band_gap_cells)
              and not scored)
    tally["controls"] += 1
    print(f"{'ok  ' if honest else 'BAD '} reported band gap is not a divergence     "
          f"{'':9} band_gap={len(band_gap_cells)} scored={len(scored)}")
    if not honest:
        failures += 1
        for problem in scored[:3]:
            print(f"       {problem}")

    def deny_the_derived_band_gap(report):
        # The lagging engine kept its pinned class, so the cell is the gap; a
        # report that refuses to name it contradicts the table it copied the
        # answers from.
        the_first_band_gap_cell(report)["band_gap"] = False

    with_pressure_report(deny_the_derived_band_gap,
                         "denying a band gap the table derives must FAIL",
                         "derive band_gap=True")

    def claim_the_band_gap_for_another_class(report):
        # The bound, not the allowance: the lagging engine answers a class the
        # committed table does not pin for it, and the cell still claims the
        # gap. Blanket acceptance is the defect this control exists to catch.
        cell = the_first_band_gap_cell(report)
        loser = "go" if cell["go"].get("data_code") != "RESULT" else "rust"
        cell[loser] = {"kind": "answered", "transport_code": PRODUCT_ERROR,
                       "data_code": "denied", "outcome": "permission_denied",
                       "result": False, "exit_class": "refused",
                       "verdict": "wrong-class", "why": "forged"}

    with_pressure_report(claim_the_band_gap_for_another_class,
                         "a band gap over an unpinned class must FAIL",
                         "derive band_gap=False")

    def note_pre_flight_reports_itself():
        """The note screen runs before the sweep, on the same owner."""
        leaky = None
        try:
            _reject_profile_rooted_provenance_note(
                f"{profile_path()}/staging/SHASUMS.txt")
        except SystemExit as exit_value:
            leaky = str(exit_value)
        clean = None
        try:
            _reject_profile_rooted_provenance_note(
                "engines staged under the battery scratch directory and "
                "bound by the staging-relative ledger SHASUMS.txt")
        except SystemExit as exit_value:
            clean = str(exit_value)
        return (leaky is not None and "profile" in leaky
                and clean is None
                and _reject_profile_rooted_provenance_note(None) is None)

    screened = note_pre_flight_reports_itself()
    tally["controls"] += 1
    print(f"{'ok  ' if screened else 'BAD '} a profile-rooted note is refused up front   "
          f"{'':9} refused={screened}")
    if not screened:
        failures += 1

    forged_gap = copy.deepcopy(baseline)
    _sync_summary(forged_gap)
    forged_gap["pressure"]["divergences"] = (
        forged_gap["pressure"]["divergences"]
        + forged_gap["pressure"]["band_gap"])
    forged_gap["pressure"]["band_gap"] = 0
    problems = assess_report(forged_gap)
    rejected = any("pressure rollup divergences" in problem
                   for problem in problems)
    tally["controls"] += 1
    print(f"{'ok  ' if rejected else 'BAD '} a rollup scoring gaps as divergences must "
          f"FAIL{'':4} rejected={rejected}")
    if not rejected:
        failures += 1
        for problem in problems[:3]:
            print(f"       {problem}")

    forged_pressure = copy.deepcopy(baseline)
    _sync_summary(forged_pressure)
    forged_pressure["pressure"]["agreements"] = 0
    problems = assess_report(forged_pressure)
    rejected = any("pressure rollup agreements" in problem
                   for problem in problems)
    tally["controls"] += 1
    print(f"{'ok  ' if rejected else 'BAD '} a pressure rollup that lies "
          f"must FAIL                             rejected={rejected} "
          f"expected_rejected=True")
    if not rejected:
        failures += 1
        for problem in problems[:3]:
            print(f"       {problem}")

    forged_digest = copy.deepcopy(baseline)
    _sync_summary(forged_digest)
    forged_digest["pressure"]["pinned_table_sha256"] = "0" * 64
    problems = assess_report(forged_digest)
    rejected = any("digests the pinned table differently" in problem
                   for problem in problems)
    tally["controls"] += 1
    print(f"{'ok  ' if rejected else 'BAD '} a forged pressure table digest "
          f"must FAIL                          rejected={rejected} "
          f"expected_rejected=True")
    if not rejected:
        failures += 1
        for problem in problems[:3]:
            print(f"       {problem}")

    # The committed-report contract of this writer, measured rather than
    # asserted.  A parity report names the binaries it swept, the work
    # directory it built them in, and the operator's command line, so it is
    # exactly the artifact class that carried a home path into evidence before
    # the shared writer existed; these two controls are what keep it from
    # regressing to a hand-serialized report.
    tally["controls"] += 1
    audit = audit_report_writers(cli_dir=_HERE,
                                 writers=["check_refusal_class_parity.py"],
                                 artifacts=False)
    commits_cleanly = not audit
    print(f"{'ok  ' if commits_cleanly else 'BAD '} this writer commits only "
          f"through the shared writer          problems={len(audit)}")
    if not commits_cleanly:
        failures += 1
        for problem in audit[:3]:
            print(f"       {problem}")

    tally["controls"] += 1
    leaky = os.path.join(tempfile.gettempdir(),
                         f"{os.getpid()}-parity-provenance-leak.json")
    profile = profile_path()
    refused_and_clean = True
    if profile:
        try:
            write_committed_report(leaky, {"schema": REPORT_SCHEMA,
                                           "work": profile + "/W-parity"},
                                   caller_paths=(("--work", None),))
            refused_and_clean = False
        except SystemExit:
            refused_and_clean = not os.path.exists(leaky)
    print(f"{'ok  ' if refused_and_clean else 'BAD '} a report that carries "
          f"the profile path is refused        "
          f"refused={refused_and_clean}")
    if not refused_and_clean:
        failures += 1
    run_shared_self_test("check_refusal_class_parity")

    for description, report, expect_problem in cases:
        problems = assess_report(report)
        rejected = bool(problems)
        ok = rejected if expect_problem else not rejected
        print(f"{'ok  ' if ok else 'BAD '} {description:58} "
              f"rejected={rejected} expected_rejected={expect_problem}")
        if not ok:
            failures += 1
            for problem in problems[:3]:
                print(f"       {problem}")
    print()
    ran = len(cases) + tally["controls"]
    # The count is an obligation, not a printout. A control deleted from
    # this list leaves the harness green and the published number smaller,
    # which is indistinguishable to a reader from a harness that still
    # proves everything, so the total is compared against the committed
    # constant and a drift fails the run.
    if ran != SELF_TEST_CASES_TOTAL:
        print(f"refusal-class parity self-test FAILED: it ran {ran} cases, "
              f"not the committed {SELF_TEST_CASES_TOTAL}; a control was "
              f"added or removed without updating the obligation")
        return 1
    if failures:
        print(f"refusal-class parity self-test FAILED: {failures} case(s) "
              f"of {ran}")
        return 1
    print(f"refusal-class parity self-test PASSED: {ran} cases "
          f"(committed total {SELF_TEST_CASES_TOTAL}), "
          f"{len(ARM_NAMES)} arms x {len(kind_names())} path kinds = "
          f"{len(expected_cells())} cells x 2 engines, "
          f"{len(PINNED_REFUSALS)} pins")
    return 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go")
    parser.add_argument("--rust")
    parser.add_argument("--fixture")
    parser.add_argument("--work")
    parser.add_argument("--json-report", default=None,
                        help="where to write the report; the default is "
                             f"{DEFAULT_REPORT_NAME} inside --work, never the "
                             "tracked evidence file, which is written only "
                             "when named explicitly")
    parser.add_argument("--sha256-ledger", default=None, metavar="PATH",
                        help="a sha256sum-format ledger of the staged "
                             "binaries; every recorded binary digest must "
                             "appear in it")
    parser.add_argument("--deadline", type=float,
                        default=ATTEMPT_DEADLINE_SECONDS,
                        help="per-attempt bound in seconds; a cell that does "
                             "not answer inside it is a hang")
    parser.add_argument("--retries", type=int, default=RETRIES,
                        help="extra attempts before a disagreement counts as "
                             "a divergence (flake vs divergence)")
    parser.add_argument("--budget-seconds", type=float,
                        default=RUN_BUDGET_SECONDS,
                        help="whole-sweep ceiling recorded in the report")
    parser.add_argument("--provenance-note", default=None,
                        help="one sentence, recorded verbatim in the report, "
                             "on where the swept binaries came from")
    parser.add_argument("--pressure", choices=("off", "routine", "full"),
                        default="off",
                        help="sweep the descriptor-pressure axis (design "
                             "section 14) in addition to the two target axes: "
                             "'routine' is the boundary profile subset, 'full' "
                             "is every pressure arm x every profile and belongs "
                             "to a milestone gate. The axis is executed by the "
                             "launcher of v4/cli/fd_pressure_harness.py and "
                             "verified against PINNED_PRESSURE_CLASSES.")
    parser.add_argument("--pressure-runs", type=int, default=2,
                        help="runs per pressure cell; never below 2, because a "
                             "cell that disagrees with itself fails (design "
                             "section 10 determinism)")
    parser.add_argument("--grid-jobs", type=int, default=1,
                        help="grid cells to sweep concurrently, each over its "
                             "own work subdirectory (default 1); this is the "
                             "suite-wide budget the caller owns")
    parser.add_argument("--pressure-jobs", type=int, default=1,
                        help="pressure cells to sweep concurrently over "
                             "independent work materializations")
    parser.add_argument("--self-test", action="store_true")
    args = parser.parse_args()
    if args.self_test:
        return _self_test()
    if args.retries < 0:
        raise SystemExit("--retries must be >= 0")
    if not (0 < args.deadline <= 30):
        raise SystemExit("--deadline must be in (0, 30] seconds")
    if args.budget_seconds > 120:
        raise SystemExit("--budget-seconds must stay <= 120 (resource policy)")
    if getattr(args, "pressure_jobs", 1) < 1:
        raise SystemExit("--pressure-jobs must be >= 1")
    if getattr(args, "grid_jobs", 1) < 1:
        raise SystemExit("--grid-jobs must be >= 1")
    if len(expected_cells()) > MAX_CELLS:
        raise SystemExit(f"the derived grid is {len(expected_cells())} cells, "
                         f"over the {MAX_CELLS}-cell ceiling")
    return live_run(args)


if __name__ == "__main__":
    sys.exit(main())
