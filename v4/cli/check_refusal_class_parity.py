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

Authority
---------
Rust observable behavior is the semantic authority for refusal classes where
the JSON-RPC specification does not pin a class.  Parity alone is not enough
to protect that authority: a future wave could change *both* engines to a new
class and this gate would still report agreement.  ``PINNED_REFUSALS`` is
therefore the second, independent anchor -- it names cells whose ``data.code``
is fixed from the Rust reference, and the verifier requires both engines to
answer the pinned class.  ``MANDATORY_PATH_KINDS`` is the third: it names the
path kinds whose coverage is an obligation, so deleting a shape from the table
(a reviewer's cheapest mutation) fails the gate instead of shrinking it.

Usage
-----
    nice python3 v4/cli/check_refusal_class_parity.py \
        --go BIN --rust BIN --fixture BIN --work EMPTY_DIR \
        [--json-report FILE] [--deadline 4.0] [--retries 2] \
        [--budget-seconds 55]

    nice python3 v4/cli/check_refusal_class_parity.py --self-test

``--self-test`` is offline: it fabricates a report from the tables and proves
the verifier rejects a synthetic divergence, an empty cell set, a deleted
fold shape, a dropped arm, a missing mandatory path kind, an unhashed or
mislabeled binary, a hang reported as agreement, and a broken pin.  The live
run additionally enforces a per-attempt deadline (a cell that never returns is
a hang, not a divergence) and a whole-run budget.
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
import time

sys.dont_write_bytecode = True

_HERE = os.path.dirname(os.path.abspath(__file__))
if _HERE not in sys.path:
    sys.path.insert(0, _HERE)

from command_sanitize import (  # noqa: E402
    recorded_checkout_root,
    recorded_git_identity,
    sanitized_command,
    sanitized_path_value,
)

REPORT_SCHEMA = "iprange-cli-refusal-class-parity-report-v1"
DEFAULT_REPORT = os.path.join(_HERE, "evidence", "refusal-class-parity.json")

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
    ("feeds.import", "iprange.v1.feeds.import", "source.path",
     lambda t, c: {"path": c["membership_main"],
                   "source": {"path": t, "mode": "immutable"},
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
)

# Shapes whose coverage is an obligation, stated independently of the table
# above: ``sidecar-folded`` carries the ratified quiescent-validate fold,
# ``fifo`` carries the never-block contract, ``symlink-live`` carries the
# symlinked writer-target ruling, ``hardlink-live`` and ``zero-length-live``
# carry the live-ownership and unprovable-generation rulings, and
# ``symlink-regular`` and ``live-membership`` carry the writer and named-feed
# outcome rulings.  Removing one here is a design change the gate reports, not
# a way to pass.
MANDATORY_PATH_KINDS = ("sidecar-folded", "fifo", "symlink-regular",
                        "symlink-live", "hardlink-live", "zero-length-live",
                        "live-membership",
                        # Deleting either of these would quietly drop the only
                        # cells that prove the outcome-ambiguity boundary of
                        # the publication path, so they are obligations.
                        "dest-parent-unreadable", "dest-collision")

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


def sweep(go, rust, ctx, deadline, retries):
    """Execute every grid cell on both engines with a flake-vs-divergence policy.

    A first-attempt disagreement is retried with fresh processes up to
    ``retries`` times.  Any retry that agrees marks the cell flaky and keeps it
    out of the divergence count; a cell that disagrees on every attempt is a
    divergence.  A reply that never arrives is a hang and is reported as such
    rather than as a class disagreement, because the two have different owners.
    """

    cells = []
    for arm_name in ARM_NAMES:
        for kind_name in kind_names():
            attempts = 0
            go_record = rust_record = None
            agreed = False
            hung = False
            while attempts <= retries:
                attempts += 1
                go_record = run_cell("go", go, arm_name, kind_name, ctx,
                                     deadline)
                rust_record = run_cell("rust", rust, arm_name, kind_name, ctx,
                                       deadline)
                if go_record["kind"] != "answered" \
                        or rust_record["kind"] != "answered":
                    hung = True
                agreed = compare(go_record, rust_record)
                if agreed:
                    break
            cells.append({
                "arm": arm_name, "path_kind": kind_name,
                "attempts": attempts, "flaky": bool(attempts > 1 and agreed),
                "hung": bool(hung and not agreed),
                "agreed": bool(agreed),
                "go": go_record, "rust": rust_record})
    return cells


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
    return {
        "schema": REPORT_SCHEMA,
        "git_head": recorded_git_identity(),
        "checkout_root": recorded_checkout_root(),
        "command": sanitized_command(argv),
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


def assess_report(report, deadline=ATTEMPT_DEADLINE_SECONDS):
    """Verify one refusal-class parity report against the committed tables.

    The grid is re-derived here, independently of the report, so the executed
    cell set is an obligation rather than a choice: an empty report, a report
    that silently drops arms or path kinds, a report that calls a hang an
    agreement, and a report whose pinned cell answers another class are each a
    problem.  This is the function ``--self-test`` attacks.
    """

    problems = []
    if not isinstance(report, dict):
        return [f"report is {type(report).__name__}, not an object"]
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
            if not isinstance(record.get("request"), str) \
                    or '"method"' not in record["request"]:
                problems.append(
                    f"{key}: {engine} record has no request bytes; a class "
                    f"claim must carry the frame that produced it")
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


def verdict(report, deadline=ATTEMPT_DEADLINE_SECONDS):
    """Return ``(ok, problems)`` for one report."""

    problems = assess_report(report, deadline=deadline)
    return not problems, problems


def live_run(args):
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
    cells = sweep(args.go, args.rust, ctx, args.deadline, args.retries)
    ended = time.monotonic()
    # Leave the work directory as removable as it was found: the destination
    # cells deliberately create an unreadable parent, and an operator
    # cleaning it up by hand should not need to know which mode to restore.
    _drop_dir(os.path.join(work, "PROBE-dest-parent-unreadable"))
    _drop_dir(os.path.join(work, "PROBE-dest-collision"))
    report = build_report(args.go, args.rust, args.fixture, work,
                          args.deadline, args.retries, args.budget_seconds,
                          sys.argv, cells, started, ended,
                          provenance=args.provenance_note)
    ok, problems = verdict(report, deadline=args.deadline)
    report["result"] = "PASS" if ok else "FAIL"
    text = json.dumps(report, sort_keys=True, indent=1)
    if args.json_report:
        with open(args.json_report, "w", encoding="utf-8") as stream:
            stream.write(text + "\n")
    diverged = [entry for entry in report["divergences"]]
    for entry in diverged[:40]:
        print(f"DIV {entry['arm']}/{entry['path_kind']}: "
              f"go={entry['go']} rust={entry['rust']}")
    if len(diverged) > 40:
        print(f"... {len(diverged) - 40} more divergent cells")
    for problem in problems[:20]:
        print(f"FAIL: {problem}")
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
    if failures:
        print(f"refusal-class parity self-test FAILED: {failures} case(s) "
              f"of {len(cases) + tally['controls']}")
        return 1
    print(f"refusal-class parity self-test PASSED: "
          f"{len(cases) + tally['controls']} cases, "
          f"{len(ARM_NAMES)} arms x {len(kind_names())} path kinds = "
          f"{len(expected_cells())} cells x 2 engines")
    return 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go")
    parser.add_argument("--rust")
    parser.add_argument("--fixture")
    parser.add_argument("--work")
    parser.add_argument("--json-report", default=DEFAULT_REPORT)
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
    if len(expected_cells()) > MAX_CELLS:
        raise SystemExit(f"the derived grid is {len(expected_cells())} cells, "
                         f"over the {MAX_CELLS}-cell ceiling")
    return live_run(args)


if __name__ == "__main__":
    sys.exit(main())
