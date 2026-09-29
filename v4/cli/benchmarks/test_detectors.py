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
        self.assertIn("lost its state", cancelled_result(response))

    def test_a_result_for_the_cancelled_request_fails(self):
        # A producer whose cancel is dead code finishes the import and
        # answers with a result. That must not pass.
        self.assertEqual(
            cancelled_result({"id": "p", "result": {"report": {"addresses": "1"}}}),
            "cancelled publish answered with a result")

    def test_no_answer_is_refused(self):
        # The spec answers every request exactly once: silence is a
        # dropped request, indistinguishable from an ignored cancel.
        self.assertIn("never answered", cancelled_result(None))

    def test_the_factual_cancelled_outcome_is_a_pass(self):
        response = {"id": "p", "error": {
            "code": -32010,
            "data": {"code": "cancelled", "outcome": "not_started"}}}
        self.assertEqual(cancelled_result(response), "")

    def test_a_wrong_error_code_fails(self):
        self.assertIn("cancelled request answered",
                      cancelled_result({"id": "p", "error": {"code": -32000}}))

    def test_a_lost_cancelled_code_fails(self):
        response = {"id": "p", "error": {
            "code": -32010, "data": {"code": "io", "outcome": "not_started"}}}
        self.assertIn("lost its code", cancelled_result(response))
