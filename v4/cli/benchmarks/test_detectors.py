"""The detecting power of the two harness detectors.

`run_all.py` must not accept any exit as a field difference.
`cancel_inflight.py` must not accept a producer that ignores cancel.
Both are asserted here so the claims are committed tests, not prose.
"""

import importlib.util
import os
import sys
import threading
import time
import unittest

_HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.dirname(_HERE))
from cancel_inflight import cancelled_result  # noqa: E402
from run_all import s0_detect_verdict  # noqa: E402

_spec = importlib.util.spec_from_file_location(
    "detectors_runner", os.path.join(_HERE, "run.py"))
_bench = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(_bench)


class ExitGateTest(unittest.TestCase):
    """An engine that dies nonzero fails its own run.

    Two controls pin the two attribution paths. The die-on-first-request
    stub is dead before any close() starts, so close() exempts it and
    the runner's exit gate attributes the failure deterministically —
    the mutation log's failing control is this one. The
    answered-then-died stub races the two checks: which fires depends
    on scheduling and cannot be forced from a child process, so that
    test's pinned contract is the union — either check's message fails
    the run. Both tests also require the stub's startup marker, proving
    the wrapper exec'd the stub and the failure is the stub's own.
    """

    def test_an_engine_that_answers_then_dies_fails_its_own_run(self):
        """An answered-then-died engine fails its own run.

        The stub answers the describe correctly, flushes it, and exits
        nonzero immediately. run_calls succeeds, so only the exit
        checks remain: the runner's gate if the engine died before
        close() could poll it, or close()'s own check if it survived
        that far. Which one fires depends on scheduling — the death
        cannot be made to precede the poll deterministically from a
        child process — so this test requires either message and pins
        the contract: the run fails.
        """
        import json as _json
        import os as _os
        import subprocess
        import tempfile
        stub = _os.path.join(_HERE, "stub_engine.py")
        scenario = {
            "schema": "iprange-bench-scenario-v1",
            "name": "exit-gate-answered",
            "purpose": "The stub answers this describe correctly and then dies.",
            "calls": [{
                "method": "iprange.v1.system.describe",
                "params": {},
                "compare": ["method"],
                "expect": {"method": "iprange.v1.system.describe"},
            }],
        }
        with tempfile.TemporaryDirectory() as work:
            path = _os.path.join(work, "exit-gate.json")
            with open(path, "w", encoding="utf-8") as stream:
                _json.dump(scenario, stream)
            wrapper = _os.path.join(work, "dying_engine")
            marker = _os.path.join(work, "stub-ran")
            with open(wrapper, "w", encoding="utf-8") as stream:
                stream.write(
                    "#!/usr/bin/env python3\n"
                    "import os, sys\n"
                    "os.environ['STUB_MODE'] = 'die-after-answer'\n"
                    f"os.environ['STUB_MARKER'] = {marker!r}\n"
                    f"os.execv({stub!r}, [{stub!r}] + sys.argv[1:])\n")
            _os.chmod(wrapper, 0o755)
            completed = subprocess.run(
                [sys.executable, _os.path.join(_HERE, "run.py"),
                 "--rust", wrapper, "--go", wrapper, "--scenario", path],
                capture_output=True, text=True, check=False, cwd=_HERE,
                timeout=300)
            # The marker proves the wrapper exec'd the stub: without it,
            # a wrapper that failed before the exec would exit 1 and
            # satisfy the exit checks without the stub ever running.
            self.assertTrue(
                _os.path.exists(marker),
                "the stub never ran — the wrapper failed before the exec")
            self.assertNotEqual(completed.returncode, 0)
            self.assertTrue(
                "rust exited 1" in completed.stderr
                or "exited with status 1" in completed.stderr,
                f"neither exit check fired: {completed.stderr!r}")

    def test_a_dying_engine_fails_its_own_run(self):
        import json as _json
        import os as _os
        import subprocess
        import tempfile
        stub = _os.path.join(_HERE, "stub_engine.py")
        # The stub reads the first request and dies immediately, before
        # any close() starts, so close() sees an already-dead peer and
        # the runner's exit gate is the only check that can attribute
        # the nonzero exit deterministically.
        scenario = {
            "schema": "iprange-bench-scenario-v1",
            "name": "exit-gate",
            "purpose": "The stub reads the first request and dies without answering.",
            "calls": [{
                "method": "iprange.v1.system.describe",
                "params": {},
                "compare": ["method"],
                "expect": {"method": "iprange.v1.system.describe"},
            }],
        }
        # The runner spawns engines with an allowlisted environment, so
        # the mode cannot travel through STUB_MODE. A wrapper script
        # sets it and execs the real stub; the runner sees a normal
        # executable that accepts --jsonrpc.
        with tempfile.TemporaryDirectory() as work:
            path = _os.path.join(work, "exit-gate.json")
            with open(path, "w", encoding="utf-8") as stream:
                _json.dump(scenario, stream)
            wrapper = _os.path.join(work, "dying_engine")
            marker = _os.path.join(work, "stub-ran")
            with open(wrapper, "w", encoding="utf-8") as stream:
                stream.write(
                    "#!/usr/bin/env python3\n"
                    "import os, sys\n"
                    "os.environ['STUB_MODE'] = 'die-on-first-request'\n"
                    f"os.environ['STUB_MARKER'] = {marker!r}\n"
                    f"os.execv({stub!r}, [{stub!r}] + sys.argv[1:])\n")
            _os.chmod(wrapper, 0o755)
            completed = subprocess.run(
                [sys.executable, _os.path.join(_HERE, "run.py"),
                 "--rust", wrapper, "--go", wrapper, "--scenario", path],
                capture_output=True, text=True, check=False, cwd=_HERE,
                timeout=300)
            # Same marker duty as the answered-then-died control.
            self.assertTrue(
                _os.path.exists(marker),
                "the stub never ran — the wrapper failed before the exec")
            self.assertNotEqual(completed.returncode, 0)
            self.assertIn("rust exited 1", completed.stderr)

    def test_an_engine_that_answers_then_dies_fails_the_close(self):
        """Answered-then-died-nonzero fails at close (astra turn-2).

        The stub answers the describe correctly, flushes it, and exits
        nonzero immediately — it is already dead when the ordinary
        close begins. An intentional-crash exemption may not be inferred
        from process timing: the ordinary close must validate the exit
        status of a peer that completed its exchanges, wherever its
        death landed.
        """
        import json as _json
        import os as _os
        import subprocess
        import tempfile
        scenario = {
            "schema": "iprange-bench-scenario-v1",
            "name": "exit-gate-answered-then-died",
            "purpose": "The stub answers and exits nonzero before close.",
            "write": [{
                "method": "iprange.v1.system.describe",
                "params": {},
                "compare": ["method"],
                "expect": {"method": "iprange.v1.system.describe"},
            }],
        }
        with tempfile.TemporaryDirectory() as work:
            path = _os.path.join(work, "exit-gate.json")
            with open(path, "w", encoding="utf-8") as stream:
                _json.dump(scenario, stream)
            dying = _os.path.join(work, "answered_then_died")
            marker = _os.path.join(work, "stub-ran")
            with open(dying, "w", encoding="utf-8") as stream:
                stream.write(
                    "#!/usr/bin/env python3\n"
                    "import json, os, sys\n"
                    "with open({marker!r}, 'w') as ran:\n"
                    "    ran.write('ran\\n')\n"
                    "for line in sys.stdin:\n"
                    "    if not line.strip():\n"
                    "        continue\n"
                    "    request = json.loads(line)\n"
                    "    response = {{\"jsonrpc\": \"2.0\", \"id\": request[\"id\"],\n"
                    "                 \"result\": {{\"method\": request[\"method\"]}}}}\n"
                    "    sys.stdout.write(json.dumps(response) + \"\\n\")\n"
                    "    sys.stdout.flush()\n"
                    "    sys.exit(7)\n".format(marker=marker))
            _os.chmod(dying, 0o755)
            completed = subprocess.run(
                [sys.executable, _os.path.join(_HERE, "run.py"),
                 "--rust", dying, "--go", dying, "--scenario", path],
                capture_output=True, text=True, check=False, cwd=_HERE,
                timeout=300)
            self.assertTrue(
                _os.path.exists(marker),
                "the dying engine never ran — the wrapper failed first")
            self.assertNotEqual(completed.returncode, 0)
            self.assertIn("exited with status 7", completed.stderr)


class CancelOutcomeClassifierTest(unittest.TestCase):
    """Astra turn-2: the cancel proof refuses forged outcomes and
    duplicate terminal answers, and checks the outcome against the
    observed publication state."""

    @staticmethod
    def cancelled(outcome):
        return {"error": {"code": -32010,
                          "data": {"code": "cancelled", "outcome": outcome}}}

    def test_a_forged_outcome_is_refused(self):
        # "banana" is not an outcome the spec names.
        from cancel_inflight import cancelled_result
        reason = cancelled_result(self.cancelled("banana"), False)
        self.assertIn("not one the spec names", reason)

    def test_published_without_destination_is_refused(self):
        from cancel_inflight import cancelled_result
        reason = cancelled_result(self.cancelled("published"), False)
        self.assertIn("destination is absent", reason)

    def test_not_published_with_destination_is_refused(self):
        from cancel_inflight import cancelled_result
        reason = cancelled_result(self.cancelled("not_published"), True)
        self.assertIn("destination exists", reason)

    def test_a_state_consistent_outcome_passes(self):
        from cancel_inflight import cancelled_result
        for outcome, exists in (("not_published", False),
                                ("not_started", False),
                                ("outcome_unknown", False)):
            self.assertEqual(
                cancelled_result(self.cancelled(outcome), exists), "",
                outcome)

    def test_commit_outcomes_are_refused_for_a_publish(self):
        # current.publish is a publication operation; committed /
        # not_committed are CommitResult outcomes it never answers
        # with (astra turn-3).
        from cancel_inflight import cancelled_result
        for outcome in ("committed", "not_committed"):
            reason = cancelled_result(self.cancelled(outcome), False)
            self.assertIn("a commit outcome, not a publication outcome",
                          reason)

    def test_a_read_only_outcome_is_refused_for_a_publish(self):
        # The proof cancels a publish, which is never a read-only
        # operation: read_only_failure is a method-outcome forgery
        # (r91: the wave's classifier accepted it).
        from cancel_inflight import cancelled_result
        reason = cancelled_result(self.cancelled("read_only_failure"), False)
        self.assertIn("never a read-only operation", reason)

    def test_a_duplicate_answer_is_refused(self):
        from cancel_inflight import record_answer
        seen = {}
        record_answer(seen, {"id": "a", "result": 1})
        with self.assertRaisesRegex(AssertionError, "duplicate answer"):
            record_answer(seen, {"id": "a", "result": 2})


class ScenarioDetectorTest(unittest.TestCase):
    def test_a_crash_is_not_a_field_difference(self):
        # /bin/false as both binaries: nonzero, but no field was compared.
        self.assertIn("without naming the compared field",
                      s0_detect_verdict(1, ""))

    def test_a_missing_worker_is_not_a_field_difference(self):
        output = "FAIL rust iprange.v1.export: worker is unavailable"
        self.assertIn("without naming the compared field",
                      s0_detect_verdict(1, output))

    def test_a_named_mismatch_is_a_field_difference(self):
        output = ("FAIL s0-detect\n"
                  "iprange.v1.system.describe implementation: "
                  "rust='rust' go='go'")
        self.assertEqual(s0_detect_verdict(1, output), "")

    def test_a_pass_is_not_a_field_difference(self):
        self.assertEqual(s0_detect_verdict(0, ""), "passed; a field difference must fail")


class BoundedReadTest(unittest.TestCase):
    """The bounded frame read fails loudly instead of hanging.

    A proof must not hang when a peer never answers or has closed
    the stream; both reach the deadline. A complete frame — even a
    co-written partial-then-completed one — is returned whole.
    """

    def test_a_complete_frame_is_returned(self):
        read_fd, write_fd = os.pipe()
        with os.fdopen(write_fd, "wb") as writer, \
                os.fdopen(read_fd, "rb") as reader:
            writer.write(b'{"id": "1"}\n')
            writer.flush()
            self.assertEqual(
                _bench.readline_bounded(reader, seconds=5),
                b'{"id": "1"}\n')

    def test_a_frame_completed_after_a_partial_write_is_returned(self):
        read_fd, write_fd = os.pipe()
        with os.fdopen(write_fd, "wb") as writer, \
                os.fdopen(read_fd, "rb") as reader:
            writer.write(b'{"id":')
            writer.flush()
            delivered = []

            def complete():
                time.sleep(0.05)
                writer.write(b' "1"}\n')
                writer.flush()

            thread = threading.Thread(target=complete)
            thread.start()
            self.assertEqual(
                _bench.readline_bounded(reader, seconds=5),
                b'{"id": "1"}\n')
            thread.join()

    def test_silence_reaches_the_deadline(self):
        read_fd, write_fd = os.pipe()
        with os.fdopen(write_fd, "wb") as writer, \
                os.fdopen(read_fd, "rb") as reader:
            with self.assertRaisesRegex(
                    AssertionError, "did not arrive within"):
                _bench.readline_bounded(reader, seconds=0.2)
            writer.close()

    def test_a_closed_stream_reaches_the_deadline(self):
        read_fd, write_fd = os.pipe()
        with os.fdopen(write_fd, "wb") as writer, \
                os.fdopen(read_fd, "rb") as reader:
            writer.close()
            with self.assertRaisesRegex(
                    AssertionError, "did not arrive within"):
                _bench.readline_bounded(reader, seconds=0.2)


class SilentPeerTest(unittest.TestCase):
    """A peer that never answers fails at the service read deadline.

    The scenario runner constructs its engines with read/write
    deadlines, so the ordinary call path is wall-clock bounded the
    same way the frame reads are. This pins that mechanism against a
    stub that accepts the request and holds it.
    """

    def test_a_silent_peer_fails_at_the_read_deadline(self):
        import subprocess
        import tempfile
        from run import JsonRpcService as Service
        stub = os.path.join(_HERE, "stub_engine.py")
        with tempfile.TemporaryDirectory() as work:
            wrapper = os.path.join(work, "silent_engine")
            with open(wrapper, "w", encoding="utf-8") as stream:
                stream.write(
                    "#!/usr/bin/env python3\n"
                    "import os, sys\n"
                    "os.environ['STUB_MODE'] = 'silent'\n"
                    f"os.execv({stub!r}, [{stub!r}] + sys.argv[1:])\n")
            os.chmod(wrapper, 0o755)
            service = Service([wrapper, "--jsonrpc"], "silent",
                              read_deadline=0.5, write_deadline=5)
            try:
                started = time.monotonic()
                with self.assertRaisesRegex(
                        AssertionError, "bounded read deadline"):
                    service.call("s1", "iprange.v1.system.describe", {})
                self.assertLess(time.monotonic() - started, 10)
            finally:
                service.close(allow_forced=True, broken_exchange=True)

    def test_a_second_exchange_after_the_deadline_is_refused(self):
        # A timed-out stream is desynchronized: the late response
        # would correlate as the next reply. The service must refuse
        # with the poison message, not attempt the exchange.
        import tempfile
        from run import JsonRpcService as Service
        stub = os.path.join(_HERE, "stub_engine.py")
        with tempfile.TemporaryDirectory() as work:
            wrapper = os.path.join(work, "silent_engine")
            with open(wrapper, "w", encoding="utf-8") as stream:
                stream.write(
                    "#!/usr/bin/env python3\n"
                    "import os, sys\n"
                    "os.environ['STUB_MODE'] = 'silent'\n"
                    f"os.execv({stub!r}, [{stub!r}] + sys.argv[1:])\n")
            os.chmod(wrapper, 0o755)
            service = Service([wrapper, "--jsonrpc"], "silent",
                              read_deadline=0.5, write_deadline=5)
            try:
                with self.assertRaisesRegex(
                        AssertionError, "bounded read deadline"):
                    service.call("s1", "iprange.v1.system.describe", {})
                with self.assertRaisesRegex(
                        AssertionError, "poisoned by a bounded I/O timeout"):
                    service.call("s2", "iprange.v1.system.describe", {})
            finally:
                service.close(allow_forced=True, broken_exchange=True)

    def test_a_poisoned_sessions_close_does_not_mask_or_leak(self):
        # A poisoned session already reported its failure; teardown
        # must not re-report it (the force-termination message would
        # mask the deadline error as the sole recorded output) and
        # must close the detached fd on every path.
        import tempfile
        from run import JsonRpcService as Service
        stub = os.path.join(_HERE, "stub_engine.py")
        with tempfile.TemporaryDirectory() as work:
            wrapper = os.path.join(work, "silent_engine")
            with open(wrapper, "w", encoding="utf-8") as stream:
                stream.write(
                    "#!/usr/bin/env python3\n"
                    "import os, sys\n"
                    "os.environ['STUB_MODE'] = 'silent'\n"
                    f"os.execv({stub!r}, [{stub!r}] + sys.argv[1:])\n")
            os.chmod(wrapper, 0o755)
            service = Service([wrapper, "--jsonrpc"], "silent",
                              read_deadline=0.5, write_deadline=5)
            closed = False
            try:
                with self.assertRaisesRegex(
                        AssertionError, "bounded read deadline"):
                    service.call("p1", "iprange.v1.system.describe", {})
                # Plain close: the hung peer is force-killed, but the
                # poisoned session's failure is not re-reported.
                service.close()
                closed = True
                self.assertIsNotNone(service._raw_stdout)
                self.assertTrue(service._raw_stdout.closed)
            finally:
                if not closed:
                    service.close(allow_forced=True,
                                  broken_exchange=True)

    def test_a_hung_peer_at_eof_fails_qualification_and_closes_the_fd(self):
        # The non-poisoned forced-teardown contract: a peer that
        # answered everything but does not exit at stdin EOF must be
        # reported as a qualification failure — and the detached fd
        # must be closed even though close() raises (the structural
        # try/finally, pinned on the raising path).
        import tempfile
        from run import JsonRpcService as Service
        stub = os.path.join(_HERE, "stub_engine.py")
        with tempfile.TemporaryDirectory() as work:
            wrapper = os.path.join(work, "hung_engine")
            with open(wrapper, "w", encoding="utf-8") as stream:
                stream.write(
                    "#!/usr/bin/env python3\n"
                    "import os, sys\n"
                    "os.environ['STUB_MODE'] = 'hang-at-eof'\n"
                    f"os.execv({stub!r}, [{stub!r}] + sys.argv[1:])\n")
            os.chmod(wrapper, 0o755)
            service = Service([wrapper, "--jsonrpc"], "hung",
                              read_deadline=5, write_deadline=5)
            try:
                service.call("h1", "iprange.v1.system.describe", {})
                with self.assertRaisesRegex(
                        AssertionError, "did not terminate cleanly"):
                    service.close()
                self.assertIsNotNone(service._raw_stdout)
                self.assertTrue(service._raw_stdout.closed)
            finally:
                service.close(allow_forced=True, broken_exchange=True)

    def test_close_closes_the_detached_fd_even_when_the_impl_raises(self):
        # The structural try/finally contract itself: any raise out of
        # the teardown implementation — including ones no inner close
        # precedes — still closes the detached fd.
        import tempfile
        from run import JsonRpcService as Service
        from unittest import mock
        stub = os.path.join(_HERE, "stub_engine.py")
        with tempfile.TemporaryDirectory() as work:
            wrapper = os.path.join(work, "stub_engine")
            with open(wrapper, "w", encoding="utf-8") as stream:
                stream.write(
                    "#!/usr/bin/env python3\n"
                    "import os, sys\n"
                    f"os.execv({stub!r}, [{stub!r}] + sys.argv[1:])\n")
            os.chmod(wrapper, 0o755)
            service = Service([wrapper, "--jsonrpc"], "boom",
                              read_deadline=5, write_deadline=5)
            try:
                with mock.patch.object(service, "_close_impl",
                                       side_effect=AssertionError("boom")):
                    with self.assertRaisesRegex(AssertionError, "boom"):
                        service.close()
                self.assertIsNotNone(service._raw_stdout)
                self.assertTrue(service._raw_stdout.closed)
            finally:
                service.close(allow_forced=True, broken_exchange=True)

    def test_a_blocked_write_arms_the_poison_guard(self):
        # The write arm: a deaf peer (reads nothing) and a frame
        # larger than the pipe buffer make the write itself block;
        # the write deadline fires, the service is poisoned, and a
        # later exchange is refused instead of attempted.
        import tempfile
        from run import JsonRpcService as Service
        stub = os.path.join(_HERE, "stub_engine.py")
        with tempfile.TemporaryDirectory() as work:
            wrapper = os.path.join(work, "deaf_engine")
            with open(wrapper, "w", encoding="utf-8") as stream:
                stream.write(
                    "#!/usr/bin/env python3\n"
                    "import os, sys\n"
                    "os.environ['STUB_MODE'] = 'deaf'\n"
                    f"os.execv({stub!r}, [{stub!r}] + sys.argv[1:])\n")
            os.chmod(wrapper, 0o755)
            service = Service([wrapper, "--jsonrpc"], "deaf",
                              read_deadline=5, write_deadline=0.5)
            try:
                payload = "x" * (256 * 1024)
                with self.assertRaisesRegex(
                        AssertionError, "bounded write deadline"):
                    service.call("w1", "iprange.v1.system.describe",
                                 {"padding": payload})
                with self.assertRaisesRegex(
                        AssertionError, "poisoned by a bounded I/O timeout"):
                    service.call("w2", "iprange.v1.system.describe", {})
            finally:
                service.close(allow_forced=True, broken_exchange=True)


class CallPathWiringTest(unittest.TestCase):
    """The proof harness wires the deadlines into both deadline-bearing
    construction sites (run_engine and BenchSession).

    SilentPeerTest pins the mechanism (a deadline-bounded service
    fails on a silent peer); this pins the wiring: both construction
    sites must pass read_deadline=120/write_deadline=30, so deleting
    the deadlines from the call sites fails here even though the
    mechanism test still greens. The deadline-free constructions
    (cancel_inflight's raw drain, describe_bytes' bounded probe)
    are separate by design.
    """

    def test_run_engine_constructs_its_service_with_deadlines(self):
        import json
        import tempfile
        import types
        recorded = {}

        class FakeService:
            def __init__(self, argv, name, **kwargs):
                recorded["kwargs"] = kwargs
                self.proc = types.SimpleNamespace(returncode=0)

            def close(self, **kwargs):
                pass

        scenario = {
            "schema": "iprange-bench-scenario-v1",
            "name": "wiring",
            "calls": [{
                "method": "iprange.v1.system.describe",
                "params": {},
                "compare": ["method"],
                "expect": {"method": "iprange.v1.system.describe"},
            }],
        }
        with tempfile.TemporaryDirectory() as work:
            path = os.path.join(work, "wiring.json")
            with open(path, "w", encoding="utf-8") as stream:
                json.dump(scenario, stream)
            loaded = _bench.load_scenario(path)
            from unittest import mock
            with mock.patch.object(_bench, "JsonRpcService", FakeService):
                try:
                    _bench.run_engine("/nonexistent-engine", "rust",
                                      loaded, work,
                                      _bench.scenario_calls(loaded))
                except Exception:
                    pass  # run_calls fails on the fake; the wiring is
                    # recorded at the construction site
        self.assertEqual(recorded["kwargs"].get("read_deadline"), 120)
        self.assertEqual(recorded["kwargs"].get("write_deadline"), 30)

    def test_bench_session_constructs_its_service_with_deadlines(self):
        import types
        import client
        from unittest import mock
        recorded = {}

        class FakeService:
            def __init__(self, argv, name, **kwargs):
                recorded["kwargs"] = kwargs
                self.proc = types.SimpleNamespace(returncode=0)

            def close(self, **kwargs):
                pass

        with mock.patch.object(client, "JsonRpcService", FakeService):
            session = client.BenchSession("/nonexistent-engine")
        self.assertEqual(recorded["kwargs"].get("read_deadline"), 120)
        self.assertEqual(recorded["kwargs"].get("write_deadline"), 30)


class CancelDetectorTest(unittest.TestCase):
    def test_a_cancelled_answer_without_outcome_fails(self):
        # The cancelled code without its state must not read as a pass:
        # an answer that drops its outcome is a lost state, not the
        # factual cancelled outcome.
        response = {"id": "p", "error": {
            "code": -32010, "data": {"code": "cancelled"}}}
        self.assertIn("lost its state", cancelled_result(response, False))

    def test_a_result_for_the_cancelled_request_fails(self):
        # A producer whose cancel is dead code finishes the import and
        # answers with a result. That must not pass.
        self.assertEqual(
            cancelled_result({"id": "p", "result": {"report": {"addresses": "1"}}}, False),
            "cancelled publish answered with a result")

    def test_no_answer_is_refused(self):
        # The spec answers every request exactly once: silence is a
        # dropped request, indistinguishable from an ignored cancel.
        self.assertIn("never answered", cancelled_result(None, False))

    def test_the_factual_cancelled_outcome_is_a_pass(self):
        response = {"id": "p", "error": {
            "code": -32010,
            "data": {"code": "cancelled", "outcome": "not_started"}}}
        self.assertEqual(cancelled_result(response, False), "")

    def test_a_wrong_error_code_fails(self):
        self.assertIn("cancelled request answered",
                      cancelled_result({"id": "p", "error": {"code": -32000}}, False))

    def test_a_lost_cancelled_code_fails(self):
        response = {"id": "p", "error": {
            "code": -32010, "data": {"code": "io", "outcome": "not_started"}}}
        self.assertIn("lost its code", cancelled_result(response, False))


class CallerKillTeardownTest(unittest.TestCase):
    # tempfile imported per-test (module-level imports stay minimal).
    """The intentional-crash teardown is explicit, not timing-inferred.

    A caller that kills its peer (crash scenarios' process groups)
    must tear down with the explicit exemption and pass; a peer that
    answers then dies nonzero on its own must still fail the ordinary
    close. The distinction is the flag, not the process state.
    """

    def test_a_killed_peer_closes_with_the_explicit_exemption(self):
        import tempfile
        stub = os.path.join(_HERE, "stub_engine.py")
        from run import JsonRpcService as Service
        with tempfile.TemporaryDirectory() as work:
            wrapper = os.path.join(work, "hang_engine")
            with open(wrapper, "w", encoding="utf-8") as stream:
                stream.write(
                    "#!/usr/bin/env python3\n"
                    "import os, sys\n"
                    "os.environ['STUB_MODE'] = 'silent'\n"
                    f"os.execv({stub!r}, [{stub!r}] + sys.argv[1:])\n")
            os.chmod(wrapper, 0o755)
            service = Service([wrapper, "--jsonrpc"], "killed",
                              read_deadline=5, write_deadline=5,
                              start_new_session=True)
            try:
                # The crash-scenario pattern: kill the process group,
                # then tear down with the explicit exemption.
                service.kill_process_group()
                service.close(allow_forced=True, broken_exchange=True)
            except AssertionError as exc:
                self.fail(f"explicit-exemption teardown failed: {exc}")
            for owner in (service._raw_stdin, service._raw_stdout,
                          service.proc.stdin, service.proc.stdout,
                          service.proc.stderr):
                # Deadline-bounded services detach the buffered wrappers
                # (accessing .closed on one raises); close whichever
                # owners exist and are open, skipping detached ones.
                try:
                    if owner is not None and not owner.closed:
                        owner.close()
                except (OSError, ValueError):
                    pass

    def test_the_same_killed_peer_fails_the_ordinary_close(self):
        # The exemption really is the flag: without it the killed
        # peer's nonzero exit fails the close (nothing about the
        # process state grants it silently).
        import tempfile
        stub = os.path.join(_HERE, "stub_engine.py")
        from run import JsonRpcService as Service
        with tempfile.TemporaryDirectory() as work:
            wrapper = os.path.join(work, "hang_engine")
            with open(wrapper, "w", encoding="utf-8") as stream:
                stream.write(
                    "#!/usr/bin/env python3\n"
                    "import os, sys\n"
                    "os.environ['STUB_MODE'] = 'silent'\n"
                    f"os.execv({stub!r}, [{stub!r}] + sys.argv[1:])\n")
            os.chmod(wrapper, 0o755)
            service = Service([wrapper, "--jsonrpc"], "killed",
                              read_deadline=5, write_deadline=5,
                              start_new_session=True)
            service.kill_process_group()
            with self.assertRaisesRegex(AssertionError, "status -9"):
                service.close()
            for owner in (service._raw_stdin, service._raw_stdout,
                          service.proc.stdin, service.proc.stdout,
                          service.proc.stderr):
                # Deadline-bounded services detach the buffered wrappers
                # (accessing .closed on one raises); close whichever
                # owners exist and are open, skipping detached ones.
                try:
                    if owner is not None and not owner.closed:
                        owner.close()
                except (OSError, ValueError):
                    pass


class DuplicateAnswerTest(unittest.TestCase):
    """A duplicate terminal frame is caught end to end.

    The stub's STUB_DUPLICATE mode re-sends every answer frame 50 ms
    later — an exactly-once violation. The ordinary strict close's
    residue check must fail the session (the cancel proof's drain runs
    record_answer over the same bytes and fails with 'duplicate
    answer'; this test pins the wire-level twin).
    """

    def test_a_duplicate_frame_fails_the_strict_close(self):
        import subprocess
        import tempfile
        from run import JsonRpcService as Service
        stub = os.path.join(_HERE, "stub_engine.py")
        with tempfile.TemporaryDirectory() as work:
            wrapper = os.path.join(work, "duplicate_engine")
            with open(wrapper, "w", encoding="utf-8") as stream:
                stream.write(
                    "#!/usr/bin/env python3\n"
                    "import os, sys\n"
                    "os.environ['STUB_DUPLICATE'] = '1'\n"
                    "os.environ['STUB_EXIT'] = '0'\n"
                    f"os.execv({stub!r}, [{stub!r}] + sys.argv[1:])\n")
            os.chmod(wrapper, 0o755)
            service = Service([wrapper, "--jsonrpc"], "duplicate",
                              read_deadline=10, write_deadline=10)
            try:
                response = service.call("d1", "iprange.v1.system.describe", {})
                self.assertIn("result", response)
                with self.assertRaisesRegex(AssertionError, "trailing"):
                    service.close()
            finally:
                service.close(allow_forced=True, broken_exchange=True)
            del subprocess




class CancelDrainBoundsTest(unittest.TestCase):
    """r99: the cancel duplicate-drain is bounded both ways — a trickle
    forger (one frame per 0.4 s) cannot stretch the 0.5 s quiet window
    past the 10 s aggregate cap, and a busy stream cannot stretch it at
    all beyond the cap."""

    def test_a_trickle_stream_fails_within_the_aggregate_bound(self):
        import json as _json
        import time as _time
        from cancel_inflight import drain_terminal_answers

        class FakeProc:
            def poll(self):
                return None  # alive forever: only the bounds can end it

        class FakeService:
            proc = FakeProc()

        frames = [{"jsonrpc": "2.0", "id": f"trickle-{n}", "result": {}} for n in range(64)]
        state = {"next": 0, "last": _time.monotonic()}

        def trickle_read():
            # One frame every 0.4 s — each resets the quiet window.
            now = _time.monotonic()
            if now - state["last"] < 0.4:
                return ""
            state["last"] = now
            if state["next"] >= len(frames):
                return ""
            frame = frames[state["next"]]
            state["next"] += 1
            return _json.dumps(frame) + "\n"

        started = _time.monotonic()
        with self.assertRaisesRegex(AssertionError, "aggregate bound"):
            drain_terminal_answers(FakeService(), {}, trickle_read,
                                   quiet_window=0.5, aggregate_cap=2.0,
                                   poll_interval=0.02)
        self.assertLess(_time.monotonic() - started, 6)

    def test_a_busy_stream_also_ends_at_the_cap(self):
        import json as _json
        import time as _time
        from cancel_inflight import drain_terminal_answers

        class FakeProc:
            def poll(self):
                return None

        class FakeService:
            proc = FakeProc()

        counter = {"n": 0}

        def busy_read():
            counter["n"] += 1
            return _json.dumps({"jsonrpc": "2.0", "id": f"b{counter['n']}",
                                "result": {}}) + "\n"

        with self.assertRaisesRegex(AssertionError, "aggregate bound"):
            drain_terminal_answers(FakeService(), {}, busy_read,
                                   quiet_window=0.5, aggregate_cap=1.0,
                                   poll_interval=0.001)

    def test_quiet_ends_the_drain(self):
        import time as _time
        from cancel_inflight import drain_terminal_answers

        class FakeProc:
            def poll(self):
                return None

        class FakeService:
            proc = FakeProc()

        started = _time.monotonic()
        drain_terminal_answers(FakeService(), {}, lambda: "",
                               quiet_window=0.2, aggregate_cap=5.0,
                               poll_interval=0.01)
        self.assertLess(_time.monotonic() - started, 2)


class AstraTurn3DetectorTest(unittest.TestCase):
    """Pins for the three astra turn-3 fixes that shipped without
    detecting tests: the capability probe's close-once, the buffered
    drain bound, and the C cursor comparison pinning both directions —
    the extension (no extra ranges) and the under-yield (no short
    yield) class (source pins in the established pattern)."""

    def test_capability_probe_closes_exactly_once(self):
        import inspect
        import run as cli_run
        source = inspect.getsource(cli_run.describe_capabilities)
        self.assertNotIn("finally:\n                service.close()", source,
                         "the probe must not double-close in a finally")
        self.assertIn("close(broken_exchange=True)", source)
        self.assertIn("capability probe close failed", source)

    def test_buffered_drain_is_bounded(self):
        import inspect
        import run as cli_run
        source = inspect.getsource(cli_run.JsonRpcService._drain_trailing_stdout)
        self.assertIn("watcher.join(timeout=5.0)", source,
                      "the buffered drain must be time-bounded")
        self.assertIn("did not reach EOF within the drain bounds", source,
                      "a missing EOF must be reported explicitly")
        self.assertIn("result[\"eof\"] = False", source,
                      "reaching the byte ceiling must not count as EOF")

    def test_c_corpus_compares_reader_cursor_boundaries(self):
        path = "v4/rust/iprange-capi/tests/native/abi_cases.c"
        with open(path, encoding="utf-8") as stream:
            source = stream.read()
        self.assertIn("iprange_v4_abi1_reader_open_direct_cursor", source)
        self.assertIn("iprange_v4_abi1_reader_open_membership_cursor", source)
        self.assertIn("iprange_v4_abi1_reader_open_network_enrichment_v1_cursor", source)
        self.assertIn("CHECK(seen < count); /* the reader yields no extra ranges */", source)
        self.assertIn("CHECK(seen == count);", source,
                      "the under-yield direction must be pinned too")
        self.assertIn("CHECK(same_address(got.from, ranges[seen].from));", source)
        self.assertIn("CHECK(same_address(got.to, ranges[seen].to));", source)

class SolTurn2DetectorTest(unittest.TestCase):
    """Sol turn-2 repairs: the proof harness must not lose teardown
    failures, must require observed EOF on the trailing drain, and
    must never block without bound closing a stream whose reader is
    stuck. Each test fails the pre-repair behavior."""

    HOLDER = (
        "#!/usr/bin/env python3\n"
        "import os, sys, time\n"
        "pid = os.fork()\n"
        "if pid == 0:\n"
        "    # Retain the inherited write ends until released; drop\n"
        "    # stdout first so one pipe can be held in isolation.\n"
        "    if {hold_stderr_only}:\n"
        "        os.close(1)\n"
        "    release = {release!r}\n"
        "    start = {start!r}\n"
        "    end = time.time() + 60\n"
        "    while time.time() < end and not os.path.exists(release):\n"
        "        if {stream} and os.path.exists(start):\n"
        "            sys.stdout.write('x' * 65536)\n"
        "            sys.stdout.flush()\n"
        "            continue\n"
        "        time.sleep(0.1)\n"
        "    os._exit(0)\n"
        "os.environ['STUB_EXIT'] = '0'\n"
    )

    def _holder(self, work, hold_stderr_only=False, stream_mode=False):
        stub = os.path.join(_HERE, "stub_engine.py")
        wrapper = os.path.join(work, "holder_engine")
        with open(wrapper, "w", encoding="utf-8") as stream:
            stream.write(self.HOLDER.format(
                hold_stderr_only="True" if hold_stderr_only else "False",
                stream="True" if stream_mode else "False",
                release=os.path.join(work, "release"),
                start=os.path.join(work, "start")))
            stream.write(f"os.execv({stub!r}, [{stub!r}] + sys.argv[1:])\n")
        os.chmod(wrapper, 0o755)
        # The knob must survive the writer binding (r187 parity): a
        # shadowed parameter silently renders stream=True for every
        # holder.
        with open(wrapper, encoding="utf-8") as handle:
            rendered = handle.read()
        self.assertIn(
            "if %s and os.path.exists(start)"
            % ("True" if stream_mode else "False"), rendered)
        return wrapper

    def test_the_parallel_feed_proof_cannot_lose_a_close_failure(self):
        # A publish that produced its destination and report and then
        # failed teardown must fail the proof: the worker thread is
        # the only carrier of the close result (Thread.join eats it).
        import parallel_feeds
        box = {}

        class Closer:
            def __init__(self, binary):
                pass

            def call(self, method, params):
                return {"report": {"addresses": 1}}

            def close(self):
                raise AssertionError("teardown failed")

        saved = parallel_feeds.BenchSession
        parallel_feeds.BenchSession = Closer
        try:
            parallel_feeds.run_publish("engine", "text", "dest", "feed", box)
        finally:
            parallel_feeds.BenchSession = saved
        self.assertIn("error", box)
        self.assertEqual(str(box["error"]), "teardown failed")

    def test_the_primary_publish_error_wins_over_a_teardown_error(self):
        # The result channel carries both errors (sol turn-2): the
        # publish failure is primary, the teardown failure secondary
        # — overwriting it would misreport the failure class.
        import parallel_feeds
        box = {}

        class BothFail:
            def __init__(self, binary):
                pass

            def call(self, method, params):
                raise AssertionError("publish failed")

            def close(self):
                raise AssertionError("teardown failed")

        saved = parallel_feeds.BenchSession
        parallel_feeds.BenchSession = BothFail
        try:
            parallel_feeds.run_publish("engine", "text", "dest", "feed", box)
        finally:
            parallel_feeds.BenchSession = saved
        self.assertEqual(str(box["error"]), "publish failed")

    def test_a_non_pair_publish_failure_still_lands_in_the_result_channel(self):
        # The escape limb (r185 panel): a failure class outside
        # (AssertionError, OSError) — the JSON decode error of a dead
        # peer, a harness KeyError — must land in the result channel,
        # not die with the worker thread unseen.
        import parallel_feeds
        box = {}

        class Escaping:
            def __init__(self, binary):
                pass

            def call(self, method, params):
                raise ValueError("truncated JSON response")

            def close(self):
                pass

        saved = parallel_feeds.BenchSession
        parallel_feeds.BenchSession = Escaping
        try:
            parallel_feeds.run_publish("engine", "text", "dest", "feed", box)
        finally:
            parallel_feeds.BenchSession = saved
        self.assertEqual(str(box.get("error")), "truncated JSON response")
        # A second non-pair class (a harness KeyError) lands too —
        # a half-narrowed catch must fail this limb as well.
        box2 = {}

        class Keyed:
            def __init__(self, binary):
                pass

            def call(self, method, params):
                raise KeyError("id")

            def close(self):
                pass

        parallel_feeds.BenchSession = Keyed
        try:
            parallel_feeds.run_publish("engine", "text", "dest", "feed", box2)
        finally:
            parallel_feeds.BenchSession = saved
        self.assertEqual(str(box2.get("error")), "'id'")
        # The teardown site carries the same breadth (r187
        # operations, r189): any finite-tuple narrowing must fail —
        # ValueError, KeyError and a custom class each land in the
        # channel; narrowing it makes prove() accept a peer whose
        # teardown failed.
        class HarnessBug(Exception):
            pass

        for failure in (ValueError("teardown exploded"),
                        KeyError("teardown key"),
                        HarnessBug("teardown bug")):
            box3 = {}

            class Closing:
                def __init__(self, binary):
                    pass

                def call(self, method, params):
                    return {"report": {"addresses": 1}}

                def close(self):
                    raise failure

            parallel_feeds.BenchSession = Closing
            try:
                parallel_feeds.run_publish("engine", "text", "dest",
                                           "feed", box3)
            finally:
                parallel_feeds.BenchSession = saved
            self.assertIs(box3.get("error"), failure)
        # The call site carries the custom class too (r191): a
        # finite-tuple narrowing naming the standard classes must
        # still fail here.
        box5 = {}

        class CustomCall:
            def __init__(self, binary):
                pass

            def call(self, method, params):
                raise HarnessBug("call bug")

            def close(self):
                pass

        parallel_feeds.BenchSession = CustomCall
        try:
            parallel_feeds.run_publish("engine", "text", "dest", "feed", box5)
        finally:
            parallel_feeds.BenchSession = saved
        self.assertIsInstance(box5.get("error"), HarnessBug)
        # The constructor is inside the same channel (r189): a spawn
        # failure must not escape the worker unseen either.
        box4 = {}

        class CannotSpawn:
            def __init__(self, binary):
                raise OSError("spawn refused")

        parallel_feeds.BenchSession = CannotSpawn
        try:
            parallel_feeds.run_publish("engine", "text", "dest", "feed", box4)
        finally:
            parallel_feeds.BenchSession = saved
        self.assertEqual(str(box4.get("error")), "spawn refused")

    def test_the_trailing_drain_requires_observed_eof(self):
        # A peer that exits zero while a descendant retains stdout
        # cannot prove a complete response set: the drain must fail
        # explicitly instead of returning the accumulated bytes.
        import tempfile
        from run import JsonRpcService as Service
        with tempfile.TemporaryDirectory() as work:
            wrapper = self._holder(work)
            release = os.path.join(work, "release")
            service = Service([wrapper, "--jsonrpc"], "holder",
                              read_deadline=1, write_deadline=5)
            try:
                service.call("s1", "iprange.v1.system.describe", {})
                with self.assertRaisesRegex(
                        AssertionError, "did not reach EOF"):
                    service.close()
            finally:
                open(release, "w").close()
                service.close(allow_forced=True, broken_exchange=True)

    def test_the_buffered_trailing_drain_requires_observed_eof(self):
        import tempfile
        from run import JsonRpcService as Service
        with tempfile.TemporaryDirectory() as work:
            wrapper = self._holder(work)
            release = os.path.join(work, "release")
            service = Service([wrapper, "--jsonrpc"], "holder")
            try:
                service.call("s1", "iprange.v1.system.describe", {})
                with self.assertRaisesRegex(
                        AssertionError, "did not reach EOF"):
                    service.close()
            finally:
                open(release, "w").close()
                service.close(allow_forced=True, broken_exchange=True)

    def test_the_trailing_drain_ceiling_is_enforced(self):
        # Byte exhaustion is a named failure (sol turn-2 contract:
        # "fail explicitly on time or byte exhaustion"): a descendant
        # streaming trailing output must die at the ceiling, not
        # accumulate until the deadline.
        import tempfile
        from run import JsonRpcService as Service
        with tempfile.TemporaryDirectory() as work:
            wrapper = self._holder(work, stream_mode=True)
            start = os.path.join(work, "start")
            release = os.path.join(work, "release")
            service = Service([wrapper, "--jsonrpc"], "holder",
                              read_deadline=5, write_deadline=5)
            try:
                service.call("s1", "iprange.v1.system.describe", {})
                open(start, "w").close()
                with self.assertRaisesRegex(AssertionError, "drain ceiling"):
                    service.close()
            finally:
                open(release, "w").close()
                service.close(allow_forced=True, broken_exchange=True)

    def test_a_stuck_stdout_reader_cannot_block_a_second_close(self):
        # The buffered-drain watcher holds the stdout buffer lock
        # across its blocking peek (r181 operations): a later close
        # must not wait on that lock forever. The composed teardown
        # (close raises on the incomplete drain, the harness closes
        # again with allow_forced) is the harness's own shape.
        import tempfile
        from run import JsonRpcService as Service
        with tempfile.TemporaryDirectory() as work:
            wrapper = self._holder(work)
            release = os.path.join(work, "release")
            service = Service([wrapper, "--jsonrpc"], "holder")
            try:
                service.call("s1", "iprange.v1.system.describe", {})
                with self.assertRaisesRegex(
                        AssertionError, "did not reach EOF"):
                    service.close()
                outcome = {}

                def _close_again():
                    try:
                        service.close(allow_forced=True, broken_exchange=True)
                        outcome["done"] = True
                    except Exception as exc:  # recorded, not raised
                        outcome["error"] = exc

                import threading
                worker = threading.Thread(target=_close_again, daemon=True)
                worker.start()
                worker.join(timeout=15)
                try:
                    self.assertFalse(
                        worker.is_alive(),
                        "a second close blocked on the stuck drain "
                        "watcher's buffer lock")
                finally:
                    open(release, "w").close()
            finally:
                open(release, "w").close()
                service.close(allow_forced=True, broken_exchange=True)

    def test_the_stderr_partial_line_bound_is_enforced(self):
        # The 8 KiB partial-line cap is a claimed contract (r181
        # security/performance): a newline-free stderr flood delivers
        # the drop marker and leaves the tail capped, instead of
        # accumulating without bound.
        import tempfile
        from run import JsonRpcService as Service
        flood = (
            "#!/usr/bin/env python3\n"
            "import os, sys, time\n"
            "pid = os.fork()\n"
            "if pid == 0:\n"
            "    release = {release!r}\n"
            "    start = {start!r}\n"
            "    sent = False\n"
            "    while not os.path.exists(release):\n"
            "        if not sent and os.path.exists(start):\n"
            "            sys.stderr.write('PAR')\n"
            "            sys.stderr.flush()\n"
            "            time.sleep(0.3)\n"
            "            sys.stderr.write('TIAL\\nKNOWN-LINE\\n')\n"
            "            sys.stderr.flush()\n"
            "            sys.stderr.write('y' * 9000 +\n"
            "                             'MORE\\nWIRE-MARKER\\n')\n"
            "            sys.stderr.flush()\n"
            "            sent = True\n"
            "        sys.stderr.write('y' * 65536)\n"
            "        sys.stderr.flush()\n"
            "    os._exit(0)\n"
            "os.environ['STUB_EXIT'] = '0'\n"
        )
        with tempfile.TemporaryDirectory() as work:
            stub = os.path.join(_HERE, "stub_engine.py")
            wrapper = os.path.join(work, "flood_engine")
            with open(wrapper, "w", encoding="utf-8") as stream:
                stream.write(flood.format(release=os.path.join(work, "release"),
                                          start=os.path.join(work, "start")))
                stream.write(f"os.execv({stub!r}, [{stub!r}] + sys.argv[1:])\n")
            os.chmod(wrapper, 0o755)
            release = os.path.join(work, "release")
            start = os.path.join(work, "start")
            service = Service([wrapper, "--jsonrpc"], "flood",
                              read_deadline=5, write_deadline=5)
            # A recorder survives the tail ring's eviction, so
            # exactly-once delivery is observable under the flood.
            seen = []

            class Recording(list):
                def append(self, item):
                    seen.append(item)
                    super().append(item)

            service.stderr_tail = Recording()
            # Routing pin (r189/r191): every crossing write must
            # actually run through the seam — a dead or decorative
            # call with inline handling, a routed-first/hybrid
            # drainer, and a call-site carry wipe all lose the wire
            # lines or the routing sighting and fail here. The
            # recorder keeps a marker sighting and counts only (no
            # chunk retention).
            routed_chunks = []
            routed_marker = []
            real_absorb = service._absorb_stderr_chunk

            def counting_absorb(chunk, pending):
                routed_chunks.append(len(chunk))
                if b"WIRE-MARKER" in chunk or b"KNOWN-LINE" in chunk:
                    routed_marker.append(True)
                return real_absorb(chunk, pending)

            service._absorb_stderr_chunk = counting_absorb
            try:
                service.call("s1", "iprange.v1.system.describe", {})
                open(start, "w").close()
                import time
                time.sleep(0.5)  # let the flood fill the tail
            finally:
                open(release, "w").close()
                service.close(allow_forced=True, broken_exchange=True)
            joined = "".join(service.stderr_tail)
            self.assertIn("stderr partial line dropped", joined)
            self.assertTrue(routed_chunks,
                            "the drainer must route chunks through the seam")
            self.assertTrue(routed_marker,
                            "the crossing write must run through the seam")
            joined_seen = "".join(seen)
            self.assertEqual(joined_seen.count("KNOWN-LINE\n"), 1,
                             "a wire line must be delivered exactly once "
                             "(occurrence count: glue-immune)")
            self.assertEqual(joined_seen.count("WIRE-MARKER\n"), 1,
                             "the crossing chunk's diagnostic line must land")
            self.assertEqual(joined_seen.count("PARTIAL\n"), 1,
                             "the cross-chunk carry must reassemble on the "
                             "wire path")
            self.assertLessEqual(
                len(joined), 20 * 8200,
                "the stderr tail grew without its partial-line bound")

    def test_the_stderr_seam_delivers_crossing_chunk_lines(self):
        # The seam (r183 panel): one chunk that crosses the 8 KiB cap
        # must still deliver its complete diagnostic lines before the
        # partial tail is dropped; the pending reset and the line-path
        # ring are load-bearing. Each named mutant fails one limb:
        # drop-before-split loses the marker line; a missing reset
        # glues stale bytes onto the next line; a deleted ring lets
        # the tail grow past its cap.
        from run import JsonRpcService as Service
        service = Service.__new__(Service)  # logic seam, no process
        service.stderr_tail = []
        pending = b""
        # (1) one chunk carrying a diagnostic line and then an
        # overrun of the cap in the same chunk.
        pending = service._absorb_stderr_chunk(
            b"MORE\nMARKER-LINE\n" + b"y" * 9000, pending)
        self.assertIn("MARKER-LINE\n", service.stderr_tail)
        self.assertIn(
            "[stderr partial line dropped: over 8 KiB without a "
            "newline]\n", service.stderr_tail)
        # (2) after the drop the buffer is clean: the next line is
        # exactly itself, not glued to stale bytes.
        pending = service._absorb_stderr_chunk(b"AFTER-DROP\n", pending)
        self.assertIn("AFTER-DROP\n", service.stderr_tail)
        # (3) the ring keeps the LAST 20 entries on the line path.
        for index in range(50):
            pending = service._absorb_stderr_chunk(
                ("line%d\n" % index).encode(), pending)
        self.assertEqual(len(service.stderr_tail), 20)
        self.assertIn("line49\n", service.stderr_tail)
        self.assertNotIn("line29\n", service.stderr_tail)
        # (4) a partial under the cap is KEPT and reassembled across
        # chunks (the accumulate limb; a never-keeps mutant fails).
        pending = service._absorb_stderr_chunk(b"par", b"")
        self.assertEqual(pending, b"par")
        pending = service._absorb_stderr_chunk(b"tial\n", pending)
        self.assertIn("partial\n", service.stderr_tail)
        self.assertEqual(pending, b"")
        # (5) the load-bearing SIZE: the first fragment alone is
        # 200 bytes, so any cap below 200 drops it before the
        # terminator arrives and fails here.
        pending = service._absorb_stderr_chunk(b"x" * 200, b"")
        self.assertEqual(pending, b"x" * 200)
        pending = service._absorb_stderr_chunk(b"\nEND\n", pending)
        self.assertIn("x" * 200 + "\n", service.stderr_tail)

    def test_the_drainer_uses_the_pinned_seam_at_the_call_site(self):
        # Call-node pin (r185 operations/parity): the drainer must
        # route its chunks through the seam the detectors exercise —
        # restoring an inline block at the call site is the exact
        # revert that would silently reopen the crossing-chunk loss.
        import inspect
        from run import JsonRpcService as Service
        source = inspect.getsource(Service.__init__)
        self.assertIn("pending = self._absorb_stderr_chunk(chunk, pending)",
                      source,
                      "the drainer must route its pending buffer through "
                      "the pinned seam (the assignment, not a bare call)")

    def test_a_quiet_reservation_watch_stops_at_close(self):
        # A quiet watch (no reservation ever appears) must stop its
        # thread at close and return promptly (sol turn-2): closing
        # the inotify fd from another thread does not cancel the
        # thread's blocked read, so the old close left a live thread
        # and a leaked inotify resource behind every attempt.
        import tempfile
        import crash_harness
        with tempfile.TemporaryDirectory() as work:
            watch = crash_harness.ReservationWatch(work, None)
            thread = watch._thread
            started = time.monotonic()
            watch.close()
            self.assertLess(time.monotonic() - started, 1.5,
                            "close() lingered on a quiet watch")
            self.assertIsNone(watch._thread)
            if thread is not None:
                self.assertFalse(thread.is_alive(),
                                 "the watch thread survived close")

    def test_a_stuck_stderr_reader_cannot_block_close(self):
        # A descendant retaining stderr pins the drainer's read; the
        # close must still finish under its bound (the buffered
        # wrapper close waits on the reader's lock forever otherwise).
        import tempfile
        from run import JsonRpcService as Service
        with tempfile.TemporaryDirectory() as work:
            wrapper = self._holder(work, hold_stderr_only=True)
            release = os.path.join(work, "release")
            service = Service([wrapper, "--jsonrpc"], "holder",
                              read_deadline=1, write_deadline=5)
            service.call("s1", "iprange.v1.system.describe", {})
            outcome = {}

            def _close():
                try:
                    service.close()
                    outcome["done"] = True
                except Exception as exc:  # recorded, not raised
                    outcome["error"] = exc

            import threading
            worker = threading.Thread(target=_close, daemon=True)
            worker.start()
            worker.join(timeout=15)
            try:
                self.assertFalse(
                    worker.is_alive(),
                    "close() did not finish with a stuck stderr reader")
            finally:
                open(release, "w").close()
            self.assertNotIn("error", outcome)
