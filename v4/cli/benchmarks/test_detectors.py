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
    """An engine that dies nonzero before answering fails its own run.

    The stub reads the first request and exits 1 without answering, so
    close() sees an already-dead peer and only the runner's exit gate
    can attribute the failure. The engine that answers every call and
    then dies at teardown is caught by close() itself, which the main
    run.py self-test pins with its nonzero-exit control.
    """

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
            with open(wrapper, "w", encoding="utf-8") as stream:
                stream.write(
                    "#!/usr/bin/env python3\n"
                    "import os, sys\n"
                    "os.environ['STUB_MODE'] = 'die-on-first-request'\n"
                    f"os.execv({stub!r}, [{stub!r}] + sys.argv[1:])\n")
            _os.chmod(wrapper, 0o755)
            completed = subprocess.run(
                [sys.executable, _os.path.join(_HERE, "run.py"),
                 "--rust", wrapper, "--go", wrapper, "--scenario", path],
                capture_output=True, text=True, check=False, cwd=_HERE)
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
