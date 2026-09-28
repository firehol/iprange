"""The detecting power of the two harness detectors.

`run_all.py` must not accept any exit as a field difference.
`cancel_inflight.py` must not accept a producer that ignores cancel.
Both are asserted here so the claims are committed tests, not prose.
"""

import importlib.util
import os
import sys
import unittest

from cancel_inflight import cancelled_result
from run_all import s0_detect_verdict

_HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.dirname(_HERE))
_spec = importlib.util.spec_from_file_location(
    "detectors_runner", os.path.join(_HERE, "run.py"))
_bench = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(_bench)


class ExitGateTest(unittest.TestCase):
    """An engine that answers every call and then dies nonzero fails.

    The stub answers a describe correctly and exits 1, so the scenario
    run must fail on the exit check, not on any protocol error.
    """

    def test_a_dying_engine_fails_its_own_run(self):
        import os as _os
        import subprocess
        stub = _os.path.join(_HERE, "stub_engine.py")
        scenario = _os.path.join(_HERE, "scenarios", "n4-batch-limit.json")
        # STUB_MODE=die-after-answer makes the stub exit nonzero right
        # after answering, so close() sees an already-dead peer and only
        # the runner's exit gate can catch it. Deleting that gate must
        # make this test pass, which is why it asserts the gate's own
        # message.
        env = dict(_os.environ, STUB_MODE="die-after-answer", STUB_EXIT="1")
        completed = subprocess.run(
            [sys.executable, _bench.__file__ if hasattr(_bench, "__file__") else "",
             "--rust", stub, "--go", stub, "--scenario", scenario],
            capture_output=True, text=True, check=False, env=env, cwd=_HERE)
        self.assertNotEqual(completed.returncode, 0)
        self.assertIn("after answering its calls", completed.stderr)


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


class CancelDetectorTest(unittest.TestCase):
    def test_a_result_for_the_cancelled_request_fails(self):
        # A producer whose cancel is dead code finishes the import and
        # answers with a result. That must not pass.
        self.assertEqual(
            cancelled_result({"id": "p", "result": {"report": {"addresses": "1"}}}),
            "cancelled publish answered with a result")

    def test_a_suppressed_request_is_a_pass(self):
        self.assertEqual(cancelled_result(None), "")

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
