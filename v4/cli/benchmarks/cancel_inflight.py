"""Cancel a request that is still running.

Unknown-id cancel does not prove the producer honors cancel, and a
proof that closes stdin proves nothing: EOF shutdown itself requests
cancellation of active work, so a producer whose `iprange.v1.cancel`
is dead code still passes. This script keeps stdin open across the
whole discriminating window. The probe must answer while stdin is
still open, so only `iprange.v1.cancel` can have stopped the publish.

The cancel is sent only after execution is observed to have begun
(the engine's own CPU time crossed a floor), and the cancelled
request must answer with the factual cancelled outcome — the spec
answers every request exactly once, so a dropped request and a
cancelled one must not both read as a pass. A producer that ignores
cancel finishes the import and answers with a result, which fails;
the destination must never appear.
"""

import argparse
import json
import os
import signal
import sys
import tempfile
import time

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from run import JsonRpcService  # noqa: E402

from generate import generate, write_text
from measure import child_cpu_seconds
from schema.engine import ValidationError, validate
from schema.results import (
    CLEANUP, COORDINATION_CLEANUP, HOUSEKEEPING, IMMUTABLE_FEED_REPORT,
    PRIVATE_OUTPUT_ATTEMPT, PUBLICATION_RESULT,
)

PUBLISH_ID = "cancel-inflight-1"
PROBE_ID = "cancel-probe-1"


def _preparation_facts(details):
    """Refuse a preparation record whose nested facts are not the
    builders' wire (sol turn-6). Values are validated, not just keys.
    Absent output is legitimate; a present output must be a complete
    private-output attempt. Cleanup, coordination, and housekeeping
    must match the schema the engines emit.
    """
    output = details.get("output")
    if output is not None:
        try:
            validate(output, PRIVATE_OUTPUT_ATTEMPT, "$.output")
        except ValidationError as exc:
            return f"preparation output is not an attempt record ({exc})"
    for name, schema in (
            ("cleanup", CLEANUP),
            ("coordination_cleanup", COORDINATION_CLEANUP),
            ("housekeeping", HOUSEKEEPING)):
        value = details.get(name)
        if value is None:
            continue
        try:
            validate(value, schema, f"$.{name}")
        except ValidationError as exc:
            return f"preparation {name} is not the builders' wire ({exc})"
    return ""


def _factual_shape(details):
    """The factual-result shape of a `details` object (sol turn-3),
    calibrated on the engines' own wire builders (publish.rs):

    publication -> the factual publication record the error path
    carries ({report, publication}); preparation -> the factual
    PublicationPreparationFailure record ({output, cleanup,
    coordination_cleanup, housekeeping, visible_housekeeping});
    commit -> a factual CommitResult. The preparation mapping is
    authoritative for outcomes: it yields only not_started or
    not_published; published and outcome_unknown come from the
    publication record."""
    if not isinstance(details, dict):
        return None
    keys = set(details)
    if {"report", "publication"} <= keys:
        return "publication"
    if {"output", "cleanup", "coordination_cleanup",
        "housekeeping", "visible_housekeeping"} <= keys:
        return "preparation"
    if {"attempted_database_id", "attempted_transaction_id",
        "durability"} <= keys:
        return "commit"
    return None


class FrameAccumulator:
    """LF-terminated frames over non-blocking readline results (sol
    turn-3): readline may return a partial line without its newline,
    so frames are complete only at LF — and every complete frame
    routes through the shared bounded frame owner
    (JsonRpcService.decode_response_line: LF/CRLF/UTF-8/envelope/
    object-size checks) instead of a bare json.loads."""

    def __init__(self, service, read_chunk, limit=1_048_578):
        self._service = service
        self._read_chunk = read_chunk
        self._limit = limit
        self._pending = b""
        self.chunk_count = 0

    def residue(self):
        """Bytes held at end of stream: a trailing frame that never
        completed. It is evidence, not garbage (sol turn-3 wave MAH)."""
        return self._pending

    def read_response(self):
        chunk = self._read_chunk()
        if not chunk:
            return None
        self.chunk_count += 1
        if isinstance(chunk, str):
            chunk = chunk.encode("utf-8")
        if len(self._pending) + len(chunk) > self._limit:
            raise AssertionError("frame exceeds the aggregate byte ceiling")
        self._pending += chunk
        if not self._pending.endswith(b"\n"):
            return None  # partial: keep accumulating
        frame = self._pending
        self._pending = b""
        return self._service.decode_response_line(frame, None)


def cancelled_result(response, destination_exists):
    """Reason the cancelled request is not a pass, or "" if it is fine.

    The spec answers every request exactly once: a cancelled request
    must answer with the factual cancelled outcome. No answer at all
    is a dropped request — indistinguishable from a producer that
    ignored the cancel and lost the response, so it is a failure.
    A delivered answer must be the factual cancelled outcome; a
    result means the producer ignored the cancel. The outcome must
    be one the spec names (iprange-jsonrpc-v1.md) and must agree
    with the observed operation state: an outcome claiming the
    publication landed requires the destination, and one claiming it
    did not forbids it.
    """
    if response is None:
        return ("cancelled request never answered: the spec answers "
                "every request exactly once")
    if "result" in response:
        return "cancelled publish answered with a result"
    error = response.get("error", {})
    data = error.get("data", {})
    if error.get("code") != -32010:
        return f"cancelled request answered {error!r}"
    if data.get("code") != "cancelled":
        return f"cancelled outcome lost its code: {data!r}"
    outcome = data.get("outcome")
    if outcome is None:
        return f"cancelled outcome lost its state: {data!r}"
    permitted = {
        "not_started", "not_committed", "committed",
        "not_published", "published", "outcome_unknown",
        "read_only_failure",
    }
    if outcome not in permitted:
        return (f"cancelled outcome {outcome!r} is not one the spec "
                f"names: {data!r}")
    if outcome == "published" and not destination_exists:
        return ("cancelled outcome claims the publication landed, but "
                f"the destination is absent: {data!r}")
    if outcome in ("not_started", "not_published") \
            and destination_exists:
        return (f"cancelled outcome {outcome!r} claims no publication, "
                f"but the destination exists: {data!r}")
    if outcome in ("committed", "not_committed"):
        # Those are CommitResult outcomes (a commit-phase operation);
        # current.publish is a publication operation whose factual
        # outcomes are not_published / published (astra turn-3: a
        # cancelled publish never answers with a commit outcome).
        return (f"cancelled publish claimed {outcome!r} — a commit "
                f"outcome, not a publication outcome: {data!r}")
    if outcome == "read_only_failure":
        # The spec maps read_only_failure only from a read-only
        # operation; this proof cancels a publish — an operation the
        # proof already observed executing (the CPU floor) — so a
        # read_only outcome here is a method-outcome forgery.
        return ("cancelled publish claimed read_only_failure, but a "
                f"publish is never a read-only operation: {data!r}")
    # Sol turn-3: the spec owes the complete factual result whenever
    # an attempt began (iprange-jsonrpc-v1.md); an outcome carrying
    # no factual details is a half-truth the proof must refuse.
    details = data.get("details")
    if details is None:
        details = {}  # a pre-attempt outcome may omit details entirely
    if not isinstance(details, dict):
        return f"cancelled outcome lost its factual details: {data!r}"
    shape = _factual_shape(details)
    if shape == "commit":
        return (f"publication outcome carries commit facts, not a "
                f"publication record: {data!r}")
    if outcome == "not_started":
        # A failure before any durable SDK attempt owes no factual
        # result (empty details are legitimate); a preparation
        # failure carries its factual preparation record.
        if details and shape != "preparation":
            return (f"pre-attempt outcome carries non-preparation "
                    f"details: {data!r}")
    elif outcome == "published":
        # Only the publication record can claim the publication
        # landed (the preparation mapping never yields published).
        if shape != "publication":
            return (f"publication outcome carries no factual "
                    f"PublicationResult: {data!r}")
    elif outcome == "not_published":
        # Either factual record is legitimate here: the publication
        # record with status not_published, or a preparation failure
        # whose output/cleanup exist (publish.rs preparation_error).
        if shape not in ("publication", "preparation"):
            return (f"publication outcome carries no factual "
                    f"PublicationResult: {data!r}")
    elif shape != "publication":
        # outcome_unknown: only the publication record maps here.
        return (f"cancelled outcome carries no factual result: {data!r}")
    # Sol turn-3 wave MAH: the record's own facts must agree with the
    # outcome — a shaped-but-contradictory record is a forgery.
    if shape == "publication":
        # Sol turn-4: key presence is not a factual result. The
        # report and the publication record must be the complete
        # records the schema already requires.
        try:
            validate(details.get("report"), IMMUTABLE_FEED_REPORT, "$.report")
            validate(details.get("publication"), PUBLICATION_RESULT, "$.publication")
        except ValidationError as exc:
            return (f"publication details are not the complete factual "
                    f"record ({exc}): {data!r}")
        record = details["publication"]
        if record.get("publication") != outcome:
            return (f"publication record contradicts the outcome "
                    f"({record.get('publication')!r} != {outcome!r}): "
                    f"{data!r}")
        content = record.get("destination_content")
        if content not in ("desired", "previous", "absent",
                           "other", "unclassified"):
            return (f"publication record carries no factual "
                    f"destination_content: {data!r}")
        # Wave MAI: the record's content claim must agree with the
        # state the proof observed — a shaped-but-contradictory
        # record is a forgery (operations r249 F1).
        if content == "absent" and destination_exists:
            return (f"publication record claims absent content while "
                    f"the destination exists: {data!r}")
        if content in ("desired", "previous") and not destination_exists:
            return (f"publication record claims {content} content "
                    f"while the destination is absent: {data!r}")
    elif shape == "preparation":
        # Sol turn-5: the five keys are not a factual record. Output,
        # when present, is the private-output attempt the builders
        # emit; cleanup, when nonempty, is a typed artifact list.
        reason = _preparation_facts(details)
        if reason:
            return f"{reason}: {data!r}"
        output = details.get("output")
        cleanup = details.get("cleanup") or {}
        cleanup_facts = bool(cleanup.get("artifacts"))
        # not_published comes only from an output attempt or nonempty
        # publication cleanup (publish.rs:217, publish.go).
        # Coordination residue alone does not qualify.
        if outcome == "not_published" and not (output or cleanup_facts):
            return (f"not_published preparation record has no output "
                    f"or cleanup facts: {data!r}")
        # not_started accepts the preparation record as-is: both
        # engines' source-error branch emits not_started WITH facts
        # (publish.rs / publish.go), so attempt facts here are
        # legitimate (wave MAI calibration).
    return ""


def record_answer(seen, response, issued=None):
    """File one response under its id.

    The spec answers every request exactly once, and only a request
    this proof issued. A second frame for the same id, or a frame for
    an id never issued, is a failure (sol turn-5).
    """
    identifier = response.get("id")
    if issued is not None and identifier not in issued:
        raise AssertionError(
            f"unsolicited answer for {identifier!r}: the proof issued "
            f"only {issued}")
    if identifier in seen:
        raise AssertionError(
            f"duplicate answer for {identifier!r}: the spec answers "
            f"every request exactly once (first={seen[identifier]!r}, "
            f"second={response!r})")
    seen[identifier] = response




def _read_service_line(service):
    """One non-blocking stdout line from the service (or '')."""
    try:
        return service.proc.stdout.readline(1_048_578)
    except (BlockingIOError, OSError):
        return ""


def drain_terminal_answers(service, seen, read_line, *,
                           quiet_window=0.5, aggregate_cap=10.0,
                           poll_interval=0.01, issued=None):
    """Drain remaining frames after the terminal pair, both bounds:

    0.5 s of quiet resets on each frame (a busy forger cannot stretch
    it), and a 10 s aggregate cap ends the drain whatever arrives (a
    frame-per-0.4s trickle cannot pin the proof).
    """
    quiet = time.monotonic() + quiet_window
    hard_stop = time.monotonic() + aggregate_cap
    frames = FrameAccumulator(service, read_line)
    while time.monotonic() < quiet:
        if time.monotonic() >= hard_stop:
            raise AssertionError(
                f"duplicate-drain window exceeded its aggregate bound "
                f"({aggregate_cap:.0f} s)")
        before = frames.chunk_count
        extra = frames.read_response()
        if extra is not None:
            record_answer(seen, extra, issued=issued)
            quiet = time.monotonic() + quiet_window
            continue
        if frames.chunk_count != before:
            quiet = time.monotonic() + quiet_window
            continue
        if service.proc.poll() is not None:
            break
        time.sleep(poll_interval)
    residue = frames.residue()
    if residue:
        # Sol turn-3 wave MAH: a trailing unterminated frame is
        # adversarial output — pre-delta code parsed it and the
        # exactly-once check caught a duplicate; dropping it silently
        # would let a forged second answer escape. File it if it is a
        # parseable answer (restoring the duplicate discrimination),
        # and fail on anything else.
        try:
            forged = json.loads(residue)
        except ValueError:
            forged = None
        # Sol turn-4: an unterminated frame always fails the LF
        # contract. A duplicate of an id already answered is still
        # attributed first. An unsolicited id is attributed too, so
        # that failure is named, then the LF failure is raised.
        if isinstance(forged, dict) and forged.get("id") in seen:
            record_answer(seen, forged, issued=issued)
        raise AssertionError(
            f"trailing unterminated frame bytes at end of drain: "
            f"{residue[:200]!r}")


def prove(binary, work):
    text_path = os.path.join(work, "slow.txt")
    destination = os.path.join(work, "slow.iprange")
    with open(text_path, "w", encoding="utf-8") as stream:
        write_text(generate(3, 800000, 4, 4_000_000), stream)
    service = JsonRpcService([binary, "--jsonrpc"], "bench", start_new_session=True)

    def forward(signum, frame):
        del signum, frame
        service.kill_process_group()
        raise SystemExit(143)

    previous = signal.signal(signal.SIGTERM, forward)
    seen = {}
    try:
        service.submit(PUBLISH_ID, "iprange.v1.current.publish", {
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
            "feed": "slow",
            "value_tag": {"text": "feed"},
            "metadata": {"mode": "replace_utf8", "text": "slow"},
            "destination": destination,
            "publication_policy": "fail_if_exists",
            "immutable_feed_budget": {
                "max_heap_bytes": "268435456",
                "max_output_pages": "200000",
                "max_workspace_pages": "200000",
                "max_open_files": 3,
            },
        })
        # Observed start: the cancel must land while the import is
        # executing, not before admission — otherwise "cancelled"
        # proves nothing about in-flight work. The engine's own CPU
        # time is the observable that execution began.
        start_deadline = time.monotonic() + 30
        started = False
        while time.monotonic() < start_deadline:
            cpu = child_cpu_seconds(service.proc.pid)
            if cpu is not None and cpu >= 0.3:
                started = True
                break
            if service.proc.poll() is not None:
                break
            time.sleep(0.02)
        if not started:
            raise AssertionError(
                "publish never began executing; the cancel window is vacuous")
        service.notify("iprange.v1.cancel", {"request_id": PUBLISH_ID})
        # The probe is submitted before stdin closes. Until it answers,
        # the only thing that can have stopped the publish is the cancel
        # notification above. Then the publish itself must reach a
        # terminal state while stdin is still open: a producer that
        # ignores cancel finishes the import and answers with a result,
        # so the classifier below is reachable, not dead code. Killing
        # the producer first would hide exactly that answer.
        service.submit(PROBE_ID, "iprange.v1.system.describe", {})
        # Read without select: a buffered readline driven by a raw-fd
        # select can strand a co-written frame inside the Python buffer,
        # where select never sees it again. Non-blocking reads drain the
        # buffer itself.
        os.set_blocking(service.proc.stdout.fileno(), False)
        deadline = time.monotonic() + 60
        probe_at = None
        frames = FrameAccumulator(service, lambda: _read_service_line(service))
        while time.monotonic() < deadline:
            response = frames.read_response()
            if response is not None:
                record_answer(seen, response, issued=(PUBLISH_ID, PROBE_ID))
                if PROBE_ID in seen and probe_at is None:
                    probe_at = time.monotonic()
                if probe_at is not None and PUBLISH_ID in seen:
                    # Terminal pair seen. The spec answers every request
                    # exactly once: a second frame for either id would be
                    # an exactly-once violation, so drain the remaining
                    # stdout under a short quiet bound and file every
                    # frame through record_answer before closing (a
                    # duplicate hidden behind the first terminal answer
                    # used to escape with the forced teardown).
                    drain_terminal_answers(
                        service, seen,
                        read_line=lambda: _read_service_line(service),
                        issued=(PUBLISH_ID, PROBE_ID))
                    break

            else:
                if service.proc.poll() is not None:
                    break
                time.sleep(0.05)
            if probe_at is not None and time.monotonic() - probe_at > 20:
                break
        if PROBE_ID not in seen:
            raise AssertionError("the probe never answered while stdin was open")
        if "result" not in seen[PROBE_ID]:
            raise AssertionError(f"probe was not answered: {seen[PROBE_ID]!r}")
        cancelled = seen.get(PUBLISH_ID)
        # The cancelled request must reach a terminal answer within the
        # window: the spec answers every request exactly once, and the
        # engines answer a cancelled request with the factual -32010
        # outcome (verified against both staged binaries). Silence is a
        # dropped request, not a cancelled one. The outcome is checked
        # against the observed publication state (destination on disk).
        reason = cancelled_result(cancelled, os.path.exists(destination))
        if reason:
            raise AssertionError(reason)
        # The session answered everything it was asked: close strictly.
        # The ordinary close's residue and exit-status checks are part
        # of the proof; the forced teardown below is only the failure
        # path's cleanup.
        service.close()
    finally:
        signal.signal(signal.SIGTERM, previous)
        if service.proc.poll() is None:
            service.kill_process_group()
        service.close(allow_forced=True, broken_exchange=True)
    if os.path.exists(destination):
        raise AssertionError("cancelled publish left a destination")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--rust", required=True)
    parser.add_argument("--go", required=True)
    args = parser.parse_args()
    for label, binary in (("rust", args.rust), ("go", args.go)):
        with tempfile.TemporaryDirectory(prefix=f"iprange-cancel-{label}-") as work:
            prove(binary, work)
        print(f"PASS {label} in-flight publish was cancelled")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, AssertionError, json.JSONDecodeError, KeyError) as exc:
        print(f"FAIL {exc}", file=sys.stderr)
        sys.exit(1)
