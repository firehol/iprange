"""Detecting checks for measurement ratios and scenario comparison."""

import importlib.util
import json
import os
import shutil
import sys
import tempfile
import unittest
from unittest import mock

_HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, _HERE)
sys.path.insert(0, os.path.dirname(_HERE))

from measure import child_cpu_seconds, measure, parse_stat_cpu, ratio, run_once, validate_response  # noqa: E402
from perf_ceiling import summarize_rounds  # noqa: E402
_spec = importlib.util.spec_from_file_location(
    "bench_runner", os.path.join(_HERE, "run.py"))
_bench = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(_bench)
compare = _bench.compare


class MeasureTest(unittest.TestCase):
    def test_ratio_names_go_over_rust(self):
        rust = {
            "elapsed_seconds": {"median": 2.0, "min": 2.0, "max": 2.0},
            "child_cpu_seconds": {"median": 1.0, "min": 1.0, "max": 1.0},
            "child_max_rss_kib": {"median": 1000, "min": 1000, "max": 1000},
        }
        go = {
            "elapsed_seconds": {"median": 3.0, "min": 3.0, "max": 3.0},
            "child_cpu_seconds": {"median": 1.5, "min": 1.5, "max": 1.5},
            "child_max_rss_kib": {"median": 1500, "min": 1500, "max": 1500},
        }
        self.assertEqual(ratio(rust, go), {"elapsed": 1.5, "rss": 1.5, "cpu": 1.5})

    def test_ratio_refuses_a_non_positive_go_median(self):
        rust = {
            "elapsed_seconds": {"median": 2.0, "min": 2.0, "max": 2.0},
            "child_max_rss_kib": {"median": 1000, "min": 1000, "max": 1000},
        }
        go = {
            "elapsed_seconds": {"median": 3.0, "min": 3.0, "max": 3.0},
            "child_max_rss_kib": {"median": 0, "min": 0, "max": 0},
        }
        with self.assertRaises(ValueError):
            ratio(rust, go)

    def test_ratio_refuses_a_non_positive_rust_median(self):
        # A zero or negative rust elapsed or RSS median must refuse
        # rather than fabricate a division.
        rust = {
            "elapsed_seconds": {"median": 0.0, "min": 0.0, "max": 0.0},
            "child_max_rss_kib": {"median": 1000, "min": 1000, "max": 1000},
        }
        go = {
            "elapsed_seconds": {"median": 3.0, "min": 3.0, "max": 3.0},
            "child_max_rss_kib": {"median": 1500, "min": 1500, "max": 1500},
        }
        with self.assertRaises(ValueError):
            ratio(rust, go)
        rust["elapsed_seconds"]["median"] = 2.0
        rust["child_max_rss_kib"]["median"] = 0
        with self.assertRaises(ValueError):
            ratio(rust, go)

    def test_ratio_refuses_a_non_positive_go_elapsed_median(self):
        rust = {
            "elapsed_seconds": {"median": 2.0, "min": 2.0, "max": 2.0},
            "child_max_rss_kib": {"median": 1000, "min": 1000, "max": 1000},
        }
        go = {
            "elapsed_seconds": {"median": 0.0, "min": 0.0, "max": 0.0},
            "child_max_rss_kib": {"median": 1500, "min": 1500, "max": 1500},
        }
        with self.assertRaises(ValueError):
            ratio(rust, go)

    def test_ratio_refuses_a_non_positive_rust_cpu_median(self):
        # The CPU guard has two prongs; a negative rust CPU must also
        # refuse, not fabricate a negative ratio.
        rust = {
            "elapsed_seconds": {"median": 2.0, "min": 2.0, "max": 2.0},
            "child_cpu_seconds": {"median": -1.0, "min": -1.0, "max": -1.0},
            "child_max_rss_kib": {"median": 1000, "min": 1000, "max": 1000},
        }
        go = {
            "elapsed_seconds": {"median": 3.0, "min": 3.0, "max": 3.0},
            "child_cpu_seconds": {"median": 1.5, "min": 1.5, "max": 1.5},
            "child_max_rss_kib": {"median": 1500, "min": 1500, "max": 1500},
        }
        with self.assertRaises(ValueError):
            ratio(rust, go)

    def test_ratio_refuses_a_zero_rust_cpu_median(self):
        # The zero boundary of the rust prong: an all-zero CPU sample
        # must refuse exactly like a negative one, not read as a
        # pass-through zero ratio.
        rust = {
            "elapsed_seconds": {"median": 2.0, "min": 2.0, "max": 2.0},
            "child_cpu_seconds": {"median": 0.0, "min": 0.0, "max": 0.0},
            "child_max_rss_kib": {"median": 1000, "min": 1000, "max": 1000},
        }
        go = {
            "elapsed_seconds": {"median": 3.0, "min": 3.0, "max": 3.0},
            "child_cpu_seconds": {"median": 1.5, "min": 1.5, "max": 1.5},
            "child_max_rss_kib": {"median": 1500, "min": 1500, "max": 1500},
        }
        with self.assertRaises(ValueError):
            ratio(rust, go)

    def test_ratio_refuses_a_non_positive_cpu_median(self):
        rust = {
            "elapsed_seconds": {"median": 2.0, "min": 2.0, "max": 2.0},
            "child_cpu_seconds": {"median": 1.0, "min": 1.0, "max": 1.0},
            "child_max_rss_kib": {"median": 1000, "min": 1000, "max": 1000},
        }
        go = {
            "elapsed_seconds": {"median": 3.0, "min": 3.0, "max": 3.0},
            "child_cpu_seconds": {"median": 0.0, "min": 0.0, "max": 0.0},
            "child_max_rss_kib": {"median": 1500, "min": 1500, "max": 1500},
        }
        with self.assertRaises(ValueError):
            ratio(rust, go)

    def test_ratio_refuses_a_one_sided_cpu_sample(self):
        # A fabricated cpu 0.0 would read as a pass of a CPU-ratio acceptance
        # check. The binding metric is elapsed and peak RSS; CPU is
        # additional data. A missing sample must refuse, not pass.
        with_cpu = {
            "elapsed_seconds": {"median": 2.0, "min": 2.0, "max": 2.0},
            "child_cpu_seconds": {"median": 1.0, "min": 1.0, "max": 1.0},
            "child_max_rss_kib": {"median": 1000, "min": 1000, "max": 1000},
        }
        without_cpu = {
            "elapsed_seconds": {"median": 3.0, "min": 3.0, "max": 3.0},
            "child_max_rss_kib": {"median": 1500, "min": 1500, "max": 1500},
        }
        with self.assertRaises(ValueError):
            ratio(with_cpu, without_cpu)
        with self.assertRaises(ValueError):
            ratio(without_cpu, with_cpu)

    def test_mutated_expect_is_not_a_pass(self):
        scenario = {
            "calls": [{
                "method": "iprange.v1.current.publish",
                "compare": ["report.addresses"],
                "expect": {"report.addresses": "8"},
            }],
        }
        rust = [{"report": {"addresses": "7"}}]
        go = [{"report": {"addresses": "7"}}]
        mismatches = compare(scenario, rust, go)
        self.assertTrue(any("expect=" in item for item in mismatches))
        scenario["calls"][0]["expect"]["report.addresses"] = "7"
        self.assertEqual(compare(scenario, rust, go), [])


def assign(root, path, value):
    parts = path.split(".")
    cursor = root
    for index, part in enumerate(parts[:-1]):
        nxt = parts[index + 1]
        if part.isdigit():
            slot = int(part)
            while len(cursor) <= slot:
                cursor.append(None)
            if cursor[slot] is None:
                cursor[slot] = [] if nxt.isdigit() else {}
            cursor = cursor[slot]
            continue
        if part not in cursor or cursor[part] is None:
            cursor[part] = [] if nxt.isdigit() else {}
        cursor = cursor[part]
    last = parts[-1]
    if last.isdigit():
        slot = int(last)
        while len(cursor) <= slot:
            cursor.append(None)
        cursor[slot] = value
    else:
        cursor[last] = value


class MeasureRoundsTest(unittest.TestCase):
    def test_zero_and_negative_rounds_are_refused(self):
        # The guard is `rounds < 1`: zero and negative inputs must both
        # refuse with the message, so a weakened `rounds < 0` guard
        # cannot pass the zero case through to an empty-sample crash.
        for rounds in (0, -1):
            with self.subTest(rounds=rounds):
                with self.assertRaisesRegex(
                        ValueError, "rounds must be positive"):
                    measure(["/bin/true"], rounds)


class StatCpuParseTest(unittest.TestCase):
    # A real-shaped /proc stat record: after ")" the fields are state,
    # ppid, ... utime and stime are indices 11 and 12.
    STAT = "12345 (iprange) R 1 12345 12345 0 -1 4194560 100 0 0 0 46 9 0 0 20 0 5 0"

    def test_parse_reads_utime_and_stime(self):
        # 46 + 9 = 55 ticks.
        self.assertAlmostEqual(parse_stat_cpu(self.STAT), 55 / os.sysconf("SC_CLK_TCK"))

    def test_a_truncated_record_is_none_not_zero(self):
        truncated = "12345 (iprange) R 1 12345 12345 0"
        self.assertIsNone(parse_stat_cpu(truncated))

    def test_garbage_is_none(self):
        self.assertIsNone(parse_stat_cpu("not a stat record"))


class TimedResponseValidationTest(unittest.TestCase):
    """A timed operation must succeed: correlated, non-empty results."""

    REQUEST = {"jsonrpc": "2.0", "id": "t1", "method": "iprange.v1.current.publish", "params": {}}

    def test_a_correlated_result_passes(self):
        response = {"jsonrpc": "2.0", "id": "t1",
                    "result": {"report": {"addresses": 47}}}
        validate_response(self.REQUEST, response)  # must not raise

    def test_an_error_response_is_refused(self):
        response = {"jsonrpc": "2.0", "id": "t1", "error": {"code": -32000}}
        with self.assertRaisesRegex(AssertionError, "timed operation failed"):
            validate_response(self.REQUEST, response)

    def test_an_uncorrelated_response_is_refused(self):
        response = {"jsonrpc": "2.0", "id": "other", "result": {}}
        with self.assertRaisesRegex(AssertionError, "does not correlate"):
            validate_response(self.REQUEST, response)

    def test_a_frame_without_result_or_error_is_refused(self):
        response = {"jsonrpc": "2.0", "id": "t1"}
        with self.assertRaisesRegex(AssertionError, "neither result nor error"):
            validate_response(self.REQUEST, response)

    def test_a_null_result_is_refused(self):
        # A null result is not real work: the timed operation must have
        # done something for the sample to count (astra turn-2 finding).
        response = {"jsonrpc": "2.0", "id": "t1", "result": None}
        with self.assertRaisesRegex(AssertionError, "null result"):
            validate_response(self.REQUEST, response)

    def test_an_empty_result_is_refused(self):
        response = {"jsonrpc": "2.0", "id": "t1", "result": {}}
        with self.assertRaisesRegex(AssertionError, "empty result"):
            validate_response(self.REQUEST, response)

    def test_an_empty_report_is_refused(self):
        response = {"jsonrpc": "2.0", "id": "t1",
                    "result": {"report": {}}}
        with self.assertRaisesRegex(AssertionError, "empty report"):
            validate_response(self.REQUEST, response)

    def test_extra_trailing_frames_are_refused(self):
        # A timed operation answers exactly once: a second frame in the
        # stream (here delivered post-exit) must fail the sample. Under
        # the shared service the second frame surfaces at close() as
        # unexpected trailing bytes on stdout.
        engine = "/bin/sh"
        script = (
            "read line; "
            "printf '%s\\n' '$FRAME1'; "
            "printf '%s\\n' '$FRAME2'; "
            "exit 0")
        request = (b'{"jsonrpc": "2.0", "id": "t2", '
                   b'"method": "iprange.v1.system.describe", "params": {}}\n')
        frame = ('{"jsonrpc": "2.0", "id": "t2", "result": {"report": {"x": 1}}}')
        with mock.patch.dict(os.environ, {}):
            command = [engine, "-c",
                       script.replace("$FRAME1", frame).replace("$FRAME2", frame)]
            with self.assertRaisesRegex(AssertionError,
                                        "trailing"):
                run_once(command, request)

    def test_an_incomplete_frame_is_refused(self):
        # The other refusal arm: a frame without its terminator never
        # becomes a sample. Under the shared service the partial frame
        # is refused at decode (FrameError) once the peer exits.
        engine = "/bin/sh"
        frame = '{"jsonrpc": "2.0", "id": "t3", "result": {"report": {"x": 1}}}'
        # The short sleep lets the sampler observe the child (the
        # peak guard), so the refusal under test is what can fail.
        script = ("read line; printf '%s' '$FRAME'; sleep 0.3; exit 0"
                  ).replace("$FRAME", frame)
        request = (b'{"jsonrpc": "2.0", "id": "t3", '
                   b'"method": "iprange.v1.system.describe", "params": {}}\n')
        with self.assertRaisesRegex(AssertionError, "frame"):
            run_once([engine, "-c", script], request)

    def test_run_once_validates_a_real_exchange(self):
        import tempfile
        stub = os.path.join(_HERE, "stub_engine.py")
        with tempfile.TemporaryDirectory() as work:
            wrapper = os.path.join(work, "exit0_stub")
            with open(wrapper, "w", encoding="utf-8") as stream:
                stream.write(
                    "#!/usr/bin/env python3\n"
                    "import os, sys\n"
                    "os.environ['STUB_EXIT'] = '0'\n"
                    f"os.execv({sys.executable!r}, [{sys.executable!r}, {stub!r}] + sys.argv[1:])\n")
            os.chmod(wrapper, 0o755)
            request = (b'{"jsonrpc": "2.0", "id": "m1", '
                       b'"method": "iprange.v1.system.describe", "params": {}}\n')
            sample = run_once([wrapper, "--jsonrpc"], request)
        self.assertGreater(sample["elapsed_seconds"], 0.0)
        # The timed exchange's response travels with the sample: the
        # caller can (and must) validate the semantic result.
        self.assertIn("response", sample)
        self.assertEqual(sample["response"]["id"], "m1")


class SamplingGuardTest(unittest.TestCase):
    """The run_once sampling guards refuse absence instead of zero.

    A platform where the peak or CPU reader observes nothing must
    fail the measurement, not report a zero that would read as a
    sample downstream (security round-6 finding, closed here).
    """

    def test_a_child_whose_peak_was_never_observed_is_refused(self):
        with mock.patch("measure.child_hwm_kib", return_value=None):
            with self.assertRaisesRegex(
                    AssertionError, "peak was not observed"):
                run_once(["/bin/true"])

    def test_a_child_whose_cpu_was_never_sampled_is_refused(self):
        # A busy child guarantees at least one sampling iteration, so
        # the patched peak reader (4096) is observed and the refusal is
        # the CPU guard's, not the peak guard's firing early on a child
        # that exited before the first poll.
        busy = ["/bin/sh", "-c",
                "i=0; while [ $i -lt 200000 ]; do i=$((i+1)); done"]
        with mock.patch("measure.child_hwm_kib", return_value=4096), \
                mock.patch("measure.child_tree_cpu_seconds", return_value=None):
            with self.assertRaisesRegex(
                    AssertionError, "cpu was not sampled"):
                run_once(busy)


    def test_a_measured_zero_cpu_is_a_sample_not_a_missing_one(self):
        # An engine whose work runs in a resident worker legitimately
        # reads zero CPU: the sample is measured, not missing, and must
        # not fail the round (the r91 wave's join/refresh harnesses
        # hit exactly this against their own engines).
        busy = ["/bin/sh", "-c",
                "i=0; while [ $i -lt 200000 ]; do i=$((i+1)); done"]
        with mock.patch("measure.child_hwm_kib", return_value=4096), \
                mock.patch("measure.child_tree_cpu_seconds", return_value=0.0):
            sample = run_once(busy)
        self.assertEqual(sample["child_cpu_seconds"], 0.0)


class CeilingSummaryTest(unittest.TestCase):
    """The ceiling round summary refuses a round without a CPU sample."""

    def test_a_round_without_a_cpu_sample_is_refused(self):
        samples = [
            {"elapsed_seconds": 1.0, "child_cpu_seconds": 0.0,
             "child_max_rss_kib": 100},
            {"elapsed_seconds": 1.1, "child_cpu_seconds": 0.5,
             "child_max_rss_kib": 110},
        ]
        with self.assertRaisesRegex(AssertionError, "no cpu sample"):
            summarize_rounds(samples)

    def test_rounds_summarize_with_median_min_max(self):
        samples = [
            {"elapsed_seconds": 1.0, "child_cpu_seconds": 0.5,
             "child_max_rss_kib": 100},
            {"elapsed_seconds": 3.0, "child_cpu_seconds": 1.5,
             "child_max_rss_kib": 300},
            {"elapsed_seconds": 2.0, "child_cpu_seconds": 1.0,
             "child_max_rss_kib": 200},
        ]
        summary = summarize_rounds(samples)
        self.assertEqual(summary["rounds"], 3)
        self.assertEqual(summary["elapsed_seconds"]["median"], 2.0)
        self.assertEqual(summary["elapsed_seconds"]["min"], 1.0)
        self.assertEqual(summary["elapsed_seconds"]["max"], 3.0)
        self.assertEqual(summary["child_cpu_seconds"]["median"], 1.0)
        self.assertEqual(summary["child_max_rss_kib"]["median"], 200)


class CancelCpuSampleTest(unittest.TestCase):
    def test_child_cpu_seconds_is_measured(self):
        sample = run_once(["/bin/sh", "-c", "i=0; while [ $i -lt 200000 ]; do i=$((i+1)); done"])
        self.assertGreater(sample["child_cpu_seconds"], 0.0)


class HarnessContractTest(unittest.TestCase):
    def test_backslash_dotdot_is_an_escape(self):
        with self.assertRaises(ValueError):
            _bench.refuse_escape("sub\\..\\..\\outside.txt", "generated path")

    def test_batch_of_errors_is_not_sixteen_describes(self):
        decoded = [{"id": f"batch-{index}", "error": {"code": -32600}} for index in range(16)]
        with self.assertRaises(AssertionError):
            _bench.batch_observation(decoded, 16)

    def test_batch_rejection_must_have_a_null_id(self):
        with self.assertRaises(AssertionError):
            _bench.batch_observation({"id": "batch-0", "error": {"code": -32600}}, 17)


class ScenarioMutantTest(unittest.TestCase):
    def test_every_scenario_rejects_a_wrong_answer(self):
        root = os.path.join(_HERE, "scenarios")
        names = sorted(name for name in os.listdir(root) if name.endswith(".json"))
        self.assertGreaterEqual(len(names), 20)
        for name in names:
            with open(os.path.join(root, name), encoding="utf-8") as stream:
                scenario = json.load(stream)
            calls = _bench.scenario_calls(scenario)
            rust = []
            go = []
            mutated = False
            for call in calls:
                result = {"bytes": "same"}
                for path in call.get("compare", []):
                    assign(result, path, "kept")
                    if not mutated:
                        call.setdefault("expect", {})[path] = "mutant"
                        mutated = True
                rust.append(result)
                go.append(json.loads(json.dumps(result)))
            self.assertTrue(mutated, name)
            mismatches = compare(scenario, rust, go)
            self.assertTrue(
                any("expect=" in item for item in mismatches),
                f"{name} accepted a mutated answer: {mismatches}")


class PerfModeDetectingTest(unittest.TestCase):
    """Astra turn-2 repairs: performance mode applies the scenario's
    assertions (expectations, cross-engine comparison, file oracles),
    reports real per-round RSS statistics, and refuses a round whose
    RSS was never sampled. Each test fails the mode the old code
    green-blessed."""

    DESCRIBE = {
        "method": "iprange.v1.system.describe",
        "params": {},
    }

    def _scenario(self, **call):
        return {"name": "perf-detect", "write": [dict(self.DESCRIBE, **call)]}

    def _stub(self, work):
        """The stub answers describe with a well-formed frame but exits
        1 by design (negative-control default); an ordinary perf round
        needs the exit-0 wrapper."""
        stub = os.path.join(_HERE, "stub_engine.py")
        wrapper = os.path.join(work, "perf_stub")
        with open(wrapper, "w", encoding="utf-8") as stream:
            stream.write(
                "#!/usr/bin/env python3\n"
                "import os, sys\n"
                "os.environ['STUB_EXIT'] = '0'\n"
                f"os.execv({stub!r}, [{stub!r}] + sys.argv[1:])\n")
        os.chmod(wrapper, 0o755)
        return wrapper

    def _run_perf(self, scenario, rounds=1, sampler=None):
        import contextlib
        import tempfile
        with tempfile.TemporaryDirectory() as work:
            stub = self._stub(work)
            context = (
                mock.patch.object(_bench, "child_hwm_kib", sampler)
                if sampler is not None
                else contextlib.nullcontext())
            with context:
                return _bench.run_perf(scenario, stub, stub, rounds, work)

    def test_a_wrong_expectation_fails_performance_mode(self):
        scenario = self._scenario(
            compare=["method"], expect={"method": "mutant"})
        with self.assertRaisesRegex(AssertionError, "expect="):
            self._run_perf(scenario)

    def test_a_cross_engine_difference_fails_performance_mode(self):
        import tempfile
        with tempfile.TemporaryDirectory() as work:
            stub = self._stub(work)
            scenario = self._scenario(compare=["product"])
            # The second engine answers a different product name: the
            # comparison the correctness mode applies must fail the
            # perf run too, not only after the timed window.
            variant = os.path.join(work, "variant_engine")
            with open(variant, "w", encoding="utf-8") as stream:
                stream.write(
                    "#!/usr/bin/env python3\n"
                    "import json, sys\n"
                    "for line in sys.stdin:\n"
                    "    if not line.strip():\n"
                    "        continue\n"
                    "    request = json.loads(line)\n"
                    "    response = {\"jsonrpc\": \"2.0\", \"id\": request[\"id\"],\n"
                    "                 \"result\": {\"product\": \"other\"}}\n"
                    "    sys.stdout.write(json.dumps(response) + \"\\n\")\n"
                    "    sys.stdout.flush()\n")
            os.chmod(variant, 0o755)
            with self.assertRaisesRegex(AssertionError, "product"):
                _bench.run_perf(scenario, stub, variant, 1, work)

    def test_a_wrong_file_oracle_fails_performance_mode(self):
        scenario = {
            "name": "perf-detect",
            "fixtures": [{"path": "out.csv", "text": "header\nalpha,1\n"}],
            "write": [dict(self.DESCRIBE,
                           expect_file_csv_rows={"path": "out.csv",
                                                 "rows": ["alpha,2"]})],
        }
        with self.assertRaisesRegex(AssertionError, "rows differ"):
            self._run_perf(scenario)

    def test_rss_statistics_report_real_per_round_spread(self):
        scenario = self._scenario(
            compare=["method"],
            expect={"method": "iprange.v1.system.describe"})
        state = {"value": 1000}

        def sampler(pid):
            state["value"] += 500
            return state["value"]

        report = self._run_perf(scenario, rounds=3, sampler=sampler)
        for label in ("rust", "go"):
            stats = report[label]["child_max_rss_kib"]
            self.assertLessEqual(stats["min"], stats["median"], label)
            self.assertLessEqual(stats["median"], stats["max"], label)
            self.assertLess(stats["min"], stats["max"], label)

    def test_a_round_without_an_rss_sample_is_refused(self):
        scenario = self._scenario(
            compare=["method"],
            expect={"method": "iprange.v1.system.describe"})
        with self.assertRaisesRegex(AssertionError, "no RSS sample"):
            self._run_perf(scenario, sampler=lambda pid: None)

    def test_a_valid_round_reports_all_fields(self):
        scenario = self._scenario(
            compare=["method"],
            expect={"method": "iprange.v1.system.describe"})
        report = self._run_perf(scenario)
        self.assertEqual(report["rounds"], 1)
        for label in ("rust", "go"):
            entry = report[label]
            self.assertEqual(entry["rounds"], 1)
            for section in ("elapsed_seconds", "child_max_rss_kib"):
                for key in ("min", "median", "max"):
                    self.assertIn(key, entry[section])
            self.assertIn("ratio", report)


class CliStepDetectingTest(unittest.TestCase):
    """Astra turn-2: the legacy CLI workload steps are compared like
    every other scenario call — a wrong exit, a wrong stdout, or a
    differing binary artifact fails the run in both modes."""

    def _wrapper(self, work, cli_stdout, cli_exit="0"):
        import sys
        stub = os.path.join(_HERE, "stub_engine.py")
        wrapper = os.path.join(work, "cli_stub")
        with open(wrapper, "w", encoding="utf-8") as stream:
            stream.write(
                "#!/usr/bin/env python3\n"
                "import os, sys\n"
                f"os.environ['STUB_CLI_STDOUT'] = {cli_stdout!r}\n"
                f"os.environ['STUB_CLI_EXIT'] = {cli_exit!r}\n"
                f"os.environ['STUB_EXIT'] = '0'\n"
                f"os.execv({sys.executable!r}, [{sys.executable!r}, {stub!r}] + sys.argv[1:])\n")
        os.chmod(wrapper, 0o755)
        return wrapper

    def _scenario(self, stdout=None, exit_code=0, compare=("exit", "stdout")):
        return {
            "name": "cli-detect",
            "cli": [{
                "cli": {"args": ["a.txt"]},
                "compare": list(compare),
                "expect": {"exit": exit_code, **({"stdout": stdout} if stdout is not None else {})},
            }],
            "fixtures": [{"path": "a.txt", "text": "10.0.0.1\n"}],
        }

    def _run(self, scenario, rounds=1):
        import tempfile
        with tempfile.TemporaryDirectory() as work:
            stub = self._wrapper(work, "10.0.0.1/32\n")
            return _bench.run_perf(scenario, stub, stub, rounds, work)

    def test_a_wrong_stdout_fails(self):
        with self.assertRaisesRegex(AssertionError, "expect="):
            self._run(self._scenario(stdout="10.0.0.2/32\n"))

    def test_a_wrong_exit_fails(self):
        with self.assertRaisesRegex(AssertionError, "expect="):
            self._run(self._scenario(exit_code=3))

    def test_a_matching_answer_passes(self):
        report = self._run(self._scenario(stdout="10.0.0.1/32\n"))
        self.assertEqual(report["rounds"], 1)


class CliArtifactDigestTest(unittest.TestCase):
    """Astra r91: the artifact digest oracle applies to cli steps — a
    byte-different redirected stdout across engines must fail the run
    (the pre-fix code compared None == None)."""

    def test_a_differing_cli_artifact_fails(self):
        import tempfile
        stub = os.path.join(_HERE, "stub_engine.py")

        def wrapper(work, label):
            path = os.path.join(work, f"engine-{label}")
            with open(path, "w", encoding="utf-8") as stream:
                stream.write(
                    "#!/usr/bin/env python3\n"
                    "import os, sys\n"
                    "os.environ['STUB_EXIT'] = '0'\n"
                    f"os.environ['STUB_CLI_STDOUT'] = {label!r}\n"
                    f"os.execv({sys.executable!r}, [{sys.executable!r}, {stub!r}] + sys.argv[1:])\n")
            os.chmod(path, 0o755)
            return path

        scenario = {
            "name": "cli-artifact-detect",
            "cli": [{
                "cli": {"args": ["a.txt"], "redirect_stdout": "merged.bin"},
                "compare": ["exit"],
                "expect": {"exit": 0},
                "expect_same_bytes": "merged.bin",
            }],
            "fixtures": [{"path": "a.txt", "text": "10.0.0.1\n"}],
        }
        with tempfile.TemporaryDirectory() as work:
            rust = wrapper(work, "AAA")
            go = wrapper(work, "BBB")
            with self.assertRaisesRegex(AssertionError, "bytes differ"):
                _bench.run_perf(scenario, rust, go, 1, work)


class PublishSampleCheckTest(unittest.TestCase):
    """Astra r91: the destination/semantic half of the timed-sample
    validation needs its own detector — a sample that answered with a
    wrong count, or wrote no destination, must fail."""

    @staticmethod
    def sample(addresses):
        return {"response": {"id": "perf-import",
                             "result": {"report": {"addresses": addresses}}}}

    def _work(self):
        import tempfile
        return tempfile.TemporaryDirectory()

    def test_a_wrong_count_is_refused(self):
        import tempfile
        from perf import check_sample
        with tempfile.TemporaryDirectory() as work:
            request = os.path.join(work, "request.json")
            destination = os.path.join(work, "out.iprange")
            with open(request, "w") as stream:
                stream.write("{}\n")
            with open(destination, "wb") as stream:
                stream.write(b"present")
            with self.assertRaisesRegex(AssertionError, "generator says"):
                check_sample(self.sample("12"), request, destination, 47)
            os.remove(destination)

    def test_a_missing_destination_is_refused(self):
        import tempfile
        from perf import check_sample
        with tempfile.TemporaryDirectory() as work:
            request = os.path.join(work, "request.json")
            destination = os.path.join(work, "absent.iprange")
            with open(request, "w") as stream:
                stream.write("{}\n")
            with self.assertRaisesRegex(AssertionError, "wrote no destination"):
                check_sample(self.sample("47"), request, destination, 47)

    def test_a_consistent_sample_passes(self):
        import tempfile
        from perf import check_sample
        with tempfile.TemporaryDirectory() as work:
            request = os.path.join(work, "request.json")
            destination = os.path.join(work, "out.iprange")
            with open(request, "w") as stream:
                stream.write("{}\n")
            with open(destination, "wb") as stream:
                stream.write(b"present")
            check_sample(self.sample("47"), request, destination, 47)
            self.assertFalse(os.path.exists(destination))  # consumed


class CliLargeOutputTest(unittest.TestCase):
    """r93: a cli step writing past the pipe buffer must not deadlock —
    the sampler drains while sampling, and the output arrives whole."""

    def test_a_large_cli_output_is_captured_without_deadlock(self):
        import subprocess
        import tempfile
        big = os.path.join(_HERE, "stub_engine.py")
        with tempfile.TemporaryDirectory() as work:
            wrapper = os.path.join(work, "big_cli")
            with open(wrapper, "w", encoding="utf-8") as stream:
                stream.write(
                    "#!/usr/bin/env python3\n"
                    "import os, sys\n"
                    "if sys.argv[1:] == ['--jsonrpc']:\n"
                    "    os.environ['STUB_EXIT'] = '0'\n"
                    f"    os.execv({sys.executable!r}, [{sys.executable!r}, {big!r}] + sys.argv[1:])\n"
                    "sys.stdout.write(open(sys.argv[0] + '.payload').read())\n"
                    "sys.exit(0)\n")
            with open(wrapper + ".payload", "w", encoding="utf-8") as stream:
                stream.write("x" * (4 << 20) + "\n")
            os.chmod(wrapper, 0o755)
            scenario = {
                "name": "cli-big-output",
                "cli": [{"cli": {"args": ["a.txt"]},
                         "compare": ["exit", "stdout"],
                         "expect": {"exit": 0, "stdout": "x" * (4 << 20) + "\n"}}],
                "fixtures": [{"path": "a.txt", "text": "10.0.0.1\n"}],
            }
            import time as _time
            started = _time.monotonic()
            report = _bench.run_perf(scenario, wrapper, wrapper, 1, work)
            self.assertLess(_time.monotonic() - started, 60)
            # The RSS sample is the cli child's, not an idle service's:
            # the 4 MiB python child's VmHWM must be recorded per engine.
            for label in ("rust", "go"):
                stats = report[label]["child_max_rss_kib"]
                self.assertGreater(stats["max"], 0, label)
            del subprocess


class HarnessValidatorTest(unittest.TestCase):
    """r93: the four perf qualification harnesses' per-sample validators
    are suite-covered — corrupting a comparison constant fails, and a
    consistent synthetic sample passes (the r91 signature-break class
    shipped green through exactly this gap)."""

    def _sample(self, report):
        return {"response": {"id": "x", "result": {"report": report}}}

    def test_a_wrong_overlap_is_refused_and_a_right_one_passes(self):
        import perf_join
        perf_join.EXPECTED_OVERLAP = 47
        self.assertRaisesRegex(
            AssertionError, "generator says",
            perf_join.check_join_sample, self._sample({"overlap_addresses": "12"}), None, None)
        perf_join.check_join_sample(self._sample({"overlap_addresses": "47"}), None, None)

    def test_a_wrong_retained_is_refused(self):
        import perf_history
        perf_history.EXPECTED_RETAINED = 91
        self.assertRaisesRegex(
            AssertionError, "generator says",
            perf_history.check_history_sample,
            self._sample({"windows": [{"after_addresses": "9"}]}), None, None)
        perf_history.check_history_sample(
            self._sample({"windows": [{"after_addresses": "91"}]}), None, None)

    def test_a_wrong_replace_diff_is_refused(self):
        import perf_replace
        perf_replace.EXPECTED_DIFF = {"unchanged": 10, "removed": 0, "added": 1}
        self.assertRaisesRegex(
            AssertionError, "generator says",
            perf_replace.check_replace_sample,
            self._sample({"unchanged_value_addresses": "9",
                          "removed_addresses": "0", "added_addresses": "1"}),
            None, None)
        perf_replace.check_replace_sample(
            self._sample({"unchanged_value_addresses": "10",
                          "removed_addresses": "0", "added_addresses": "1"}),
            None, None)

    def test_a_wrong_churn_diff_is_refused(self):
        import perf_churn
        perf_churn.EXPECTED_LAST_STEP = {"unchanged": 8, "removed": 1, "added": 2}
        self.assertRaisesRegex(
            AssertionError, "generator says",
            perf_churn.check_churn_sample,
            self._sample({"unchanged_value_addresses": "7",
                          "removed_addresses": "1", "added_addresses": "2"}),
            None, None)
        perf_churn.check_churn_sample(
            self._sample({"unchanged_value_addresses": "8",
                          "removed_addresses": "1", "added_addresses": "2"}),
            None, None)


class SampleBinaryDetectorTest(unittest.TestCase):
    """r95: the enforcement seam — sample_binary runs its per-sample
    check — is itself detected (the r91 signature-break class shipped
    green through exactly this gap)."""

    REQUEST = {"jsonrpc": "2.0", "id": "s", "method": "iprange.v1.system.describe",
               "params": {}}

    def _run(self, check):
        import json as _json
        import tempfile
        stub = os.path.join(_HERE, "stub_engine.py")
        with tempfile.TemporaryDirectory() as work:
            wrapper = os.path.join(work, "exit0_stub")
            with open(wrapper, "w", encoding="utf-8") as stream:
                stream.write(
                    "#!/usr/bin/env python3\n"
                    "import os, sys\n"
                    "os.environ['STUB_EXIT'] = '0'\n"
                    f"os.execv({sys.executable!r}, [{sys.executable!r}, {stub!r}] + sys.argv[1:])\n")
            os.chmod(wrapper, 0o755)
            request = os.path.join(work, "request.json")
            with open(request, "w") as stream:
                _json.dump(self.REQUEST, stream)
                stream.write("\n")
            destination = os.path.join(work, "out.iprange")
            from perf import sample_binary
            return sample_binary(wrapper, [(request, destination)], check=check)

    def test_the_check_runs_per_sample_and_can_fail_the_run(self):
        def refusing_check(sample, request, destination):
            del sample, request, destination
            raise AssertionError("generator says 47, got 12")

        with self.assertRaisesRegex(AssertionError, "generator says"):
            self._run(refusing_check)

    def test_the_check_runs_on_every_sample(self):
        # A multi-request population drives one check per sample — a
        # check invoked only on the first sample would pass this with
        # len(seen) == 1 (the r97 per-sample half).
        import json as _json
        import tempfile
        stub = os.path.join(_HERE, "stub_engine.py")
        with tempfile.TemporaryDirectory() as work:
            wrapper = os.path.join(work, "exit0_stub")
            with open(wrapper, "w", encoding="utf-8") as stream:
                stream.write(
                    "#!/usr/bin/env python3\n"
                    "import os, sys\n"
                    "os.environ['STUB_EXIT'] = '0'\n"
                    f"os.execv({sys.executable!r}, [{sys.executable!r}, {stub!r}] + sys.argv[1:])\n")
            os.chmod(wrapper, 0o755)
            requests = []
            for index in range(3):
                request = os.path.join(work, f"request-{index}.json")
                with open(request, "w") as stream:
                    payload = dict(self.REQUEST, id=f"s{index}")
                    _json.dump(payload, stream)
                    stream.write("\n")
                requests.append((request, os.path.join(work, f"out-{index}")))
            seen = []

            def counting_check(sample, request, destination):
                seen.append(sample["response"]["id"])

            from perf import sample_binary
            sample_binary(wrapper, requests, check=counting_check)
            self.assertEqual(seen, ["s0", "s1", "s2"])

    def test_a_passing_check_completes_the_sample(self):
        seen = []

        def accepting_check(sample, request, destination):
            seen.append(sample.get("response", {}).get("id"))
            self.assertIn("response", sample)

        report = self._run(accepting_check)
        self.assertEqual(len(seen), 1)
        self.assertGreater(report["elapsed_seconds"]["median"], 0)


class RpcErrorDigestCombinationTest(unittest.TestCase):
    """r97: expect_rpc_error refuses to combine with expect_same_bytes —
    an error answer produces no artifact and the pair compared
    None == None across engines (the digest hole r95 filed)."""

    def test_the_combination_is_refused(self):
        import sys as _sys
        import tempfile
        stub = os.path.join(_HERE, "stub_engine.py")
        call = {
            "method": "iprange.v1.current.publish",
            "params": {},
            "expect_rpc_error": -32010,
            "expect_same_bytes": "out.bin",
            "compare": [],
        }
        with tempfile.TemporaryDirectory() as work:
            wrapper = os.path.join(work, "err_stub")
            with open(wrapper, "w", encoding="utf-8") as stream:
                stream.write(
                    "#!/usr/bin/env python3\n"
                    "import os, sys\n"
                    "os.environ['STUB_RPC_ERROR_CODE'] = '-32010'\n"
                    f"os.execv({_sys.executable!r}, [{_sys.executable!r}, {stub!r}] + sys.argv[1:])\n")
            os.chmod(wrapper, 0o755)
            from run import JsonRpcService as Service
            service = Service([wrapper, "--jsonrpc"], "combo",
                              read_deadline=10, write_deadline=10)
            try:
                with self.assertRaisesRegex(
                        AssertionError,
                        "cannot combine with expect_same_bytes"):
                    _bench.run_calls(service, "x", {"name": "c"}, work,
                                     [call], None)
            finally:
                service.close(allow_forced=True, broken_exchange=True)


class ScenarioOrderTest(unittest.TestCase):
    """r97: scenario_calls lists cli steps in execution order (write,
    cli, read) — the index-wise compare() pairing depends on it."""

    def test_cli_steps_precede_read_steps(self):
        scenario = {
            "name": "order",
            "write": [{"method": "write-call"}],
            "cli": [{"cli": {"args": ["a"]}}],
            "read": [{"method": "read-call"}],
        }
        calls = _bench.scenario_calls(scenario)
        methods = [c.get("method", "cli") for c in calls]
        self.assertEqual(methods, ["write-call", "cli", "read-call"])


class CliStderrDrainTest(unittest.TestCase):
    """r97: a cli step writing past the stderr pipe buffer must not
    deadlock — the drain covers both pipes (the stdout twin is
    CliLargeOutputTest)."""

    def test_a_large_stderr_output_does_not_deadlock(self):
        import tempfile
        import time as _time
        with tempfile.TemporaryDirectory() as work:
            wrapper = os.path.join(work, "big_err")
            with open(wrapper, "w", encoding="utf-8") as stream:
                stream.write(
                    "#!/usr/bin/env python3\n"
                    "import sys\n"
                    "sys.stderr.write('e' * (4 << 20) + chr(10))\n"
                    "sys.stderr.flush()\n"
                    "sys.stdout.write('out\\n')\n"
                    "sys.exit(0)\n")
            os.chmod(wrapper, 0o755)
            scenario = {
                "name": "cli-big-stderr",
                "cli": [{"cli": {"args": ["a.txt"]}, "compare": ["exit", "stdout"],
                         "expect": {"exit": 0, "stdout": "out\n"}}],
                "fixtures": [{"path": "a.txt", "text": "10.0.0.1\n"}],
            }
            started = _time.monotonic()
            _bench.run_perf(scenario, wrapper, wrapper, 1, work)
            self.assertLess(_time.monotonic() - started, 60)


class CliOnlyNoServiceTest(unittest.TestCase):
    """r97: a cli-only scenario spawns no idle --jsonrpc service — the
    engine binary is invoked only as the cli child. A stub that exits
    nonzero when started with --jsonrpc (but succeeds as the cli child)
    proves no service round-trip happens."""

    def test_a_cli_only_run_never_starts_the_service(self):
        import tempfile
        stub = os.path.join(_HERE, "stub_engine.py")
        with tempfile.TemporaryDirectory() as work:
            wrapper = os.path.join(work, "rpc_rejecting")
            with open(wrapper, "w", encoding="utf-8") as stream:
                stream.write(
                    "#!/usr/bin/env python3\n"
                    "import os, sys\n"
                    "if sys.argv[1:] == ['--jsonrpc']:\n"
                    "    sys.stderr.write('service must not start\\n')\n"
                    "    sys.exit(9)\n"
                    "os.environ['STUB_CLI_STDOUT'] = '10.0.0.1' + chr(10)\n"
                    "os.environ['STUB_CLI_EXIT'] = '0'\n"
                    f"os.execv({sys.executable!r}, [{sys.executable!r}, {stub!r}] + sys.argv[1:])\n")
            os.chmod(wrapper, 0o755)
            scenario = {
                "name": "cli-only-no-service",
                "cli": [{"cli": {"args": ["a.txt"]},
                         "compare": ["exit", "stdout"],
                         "expect": {"exit": 0, "stdout": "10.0.0.1" + chr(10)}}],
                "fixtures": [{"path": "a.txt", "text": "10.0.0.1" + chr(10)}],
            }
            report = _bench.run_perf(scenario, wrapper, wrapper, 1, work)
            # The run passed, the service never started (a service start
            # would fail the run through the strict close), and the RSS
            # sample came from the cli child alone.
            for label in ("rust", "go"):
                self.assertGreater(report[label]["child_max_rss_kib"]["max"], 0)


class HarnessMainWiringTest(unittest.TestCase):
    """r99: the four perf harness mains wire their per-sample validators
    into sample_binary (a signature change at the call site — exactly
    the r91 break — ships green without this)."""

    def test_every_harness_main_passes_its_validator(self):
        import inspect
        import perf_churn
        import perf_history
        import perf_join
        import perf_replace
        pairs = [
            (perf_join, "check_join_sample"),
            (perf_history, "check_history_sample"),
            (perf_replace, "check_replace_sample"),
            (perf_churn, "check_churn_sample"),
        ]
        for module, checker in pairs:
            source = inspect.getsource(module.main)
            self.assertIn(
                f"check={checker}", source,
                f"{module.__name__}.main does not wire {checker} into "
                f"sample_binary")
            self.assertIn("sample_binary(", source, module.__name__)
            # The validator itself is exercised by HarnessValidatorTest;
            # this pins the wiring (the call-site contract).




class CliResidueDrainBoundTest(unittest.TestCase):
    """r101: the post-exit residue drain is deadline-bounded — a source
    pin in the HarnessMainWiringTest pattern (the r101 panel verified
    the bound TRUE at source; this pins it against regression): the
    residue loop must consult the same deadline the outer loop does,
    so a leaked pipe-writer cannot pin it."""

    def test_the_residue_loop_consults_the_deadline(self):
        import inspect
        source = inspect.getsource(_bench.run_cli)
        self.assertIn("while time.monotonic() <= deadline", source,
                      "run_cli's residue drain must be deadline-bounded")


class EngineOrderAlternationTest(unittest.TestCase):
    """r107: both timed legs alternate the engine order per round —
    a run-order-correlated drift cannot bias every timed pair the same
    direction (pinned at source, HarnessMainWiringTest pattern)."""

    def test_both_legs_alternate_by_round_index(self):
        import inspect
        source = inspect.getsource(_bench.run_perf)
        self.assertIn("round_index % 2", source,
                      "run_perf must alternate engine order by round")
        self.assertEqual(source.count("round_index % 2"), 2,
                         "both the write leg and the read leg must alternate")


class RssSpawnRaceRetryTest(unittest.TestCase):
    """r137: the RSS-sample spawn race (performance-r137 F1). A child
    that completes before the sampler's first read leaves the round's
    peak mechanically unrecoverable. A round whose sample is missing
    from a child the sampler never observed is re-measured exactly
    once, the failed attempt discarded; a miss from an observed child
    is a real defect and refuses at once; two consecutive misses
    refuse as before (the guard's detector power is unchanged)."""

    def _scenario(self):
        return {"name": "retry-detect",
                "write": [{"method": "iprange.v1.system.describe",
                           "compare": []}]}

    def _fake_engine(self, fill):
        calls = []

        def fake(binary, name, scenario, work, calls_arg, peer=None,
                 engine_work=None, peak=None, prepare=False, deferred=None):
            calls.append(name)
            if peak is not None:
                fill(peak)
            return []

        fake.calls = calls
        return fake

    def test_a_spawn_race_round_is_remeasured_once(self):
        state = {"n": 0}

        def fill(peak):
            state["n"] += 1
            if state["n"] > 2:  # only the retry's legs yield samples
                peak["observed"] = True
                peak["kib"] = 1234

        fake = self._fake_engine(fill)
        with tempfile.TemporaryDirectory() as work, \
                mock.patch.object(_bench, "run_engine", fake):
            report = _bench.run_perf(self._scenario(), "rust", "go", 1, work)
        self.assertEqual(len(fake.calls), 4,
                         "exactly one retry: two legs, then two more")
        for label in ("rust", "go"):
            stats = report[label]["child_max_rss_kib"]
            self.assertEqual(stats["median"], 1234, label)
            self.assertEqual(stats["min"], 1234,
                             "the failed attempt's data must be discarded")

    def test_two_consecutive_misses_refuse_after_one_retry(self):
        fake = self._fake_engine(lambda peak: None)
        with tempfile.TemporaryDirectory() as work, \
                mock.patch.object(_bench, "run_engine", fake):
            with self.assertRaisesRegex(AssertionError, "no RSS sample"):
                _bench.run_perf(self._scenario(), "rust", "go", 1, work)
        self.assertEqual(len(fake.calls), 4,
                         "the retry is bounded to exactly one")

    def test_an_observed_child_miss_refuses_without_retry(self):
        def fill(peak):
            # The sampler saw the child but folded no sample: a real
            # defect, not the spawn race — no retry may mask it.
            peak["observed"] = True

        fake = self._fake_engine(fill)
        with tempfile.TemporaryDirectory() as work, \
                mock.patch.object(_bench, "run_engine", fake):
            with self.assertRaisesRegex(AssertionError, "no RSS sample"):
                _bench.run_perf(self._scenario(), "rust", "go", 1, work)
        self.assertEqual(len(fake.calls), 2, "a real miss must not retry")


class WorkDirNormalizationTest(unittest.TestCase):
    """r137: relative --work-dir (operations-r137 F3). $WORK paths are
    harness-cwd-relative while the engine runs with cwd=work_dir; the
    entry point normalizes the work dir once so the two views cannot
    split."""

    def test_relative_work_dir_is_normalized(self):
        captured = {}

        def fake_run_perf(scenario, rust, go, rounds, work):
            captured["work"] = work
            return {"scenario": scenario["name"], "rounds": rounds}

        with tempfile.TemporaryDirectory() as cwd:
            os.makedirs(os.path.join(cwd, "relwork"))
            previous = os.getcwd()
            os.chdir(cwd)
            try:
                argv = ["run.py", "--rust", "/bin/true", "--go", "/bin/true",
                        "--scenario", "s.json", "--mode", "perf",
                        "--rounds", "1", "--work-dir", "relwork"]
                import contextlib
                import io
                with mock.patch.object(sys, "argv", argv), \
                        mock.patch.object(_bench, "run_perf", fake_run_perf), \
                        mock.patch.object(_bench, "load_scenario",
                                          lambda path: {"name": "s"}), \
                        contextlib.redirect_stdout(io.StringIO()):
                    rc = _bench.main()
            finally:
                os.chdir(previous)
        self.assertEqual(rc, 0)
        self.assertTrue(os.path.isabs(captured["work"]))
        self.assertEqual(captured["work"], os.path.join(cwd, "relwork"))


class RetryWorkDirStateTest(unittest.TestCase):
    """r139 panel: the retry must restart from clean work dirs
    (operations/parity/portability F1). A stateful engine leaves
    artifacts behind (a created database, a published name); a retry
    that sees them fails with a product-looking error."""

    def test_the_retry_restarts_from_clean_work_dirs(self):
        state = {"n": 0}

        def fake(binary, name, scenario, work, calls, peer=None,
                 engine_work=None, peak=None, prepare=False, deferred=None):
            state["n"] += 1
            marker = os.path.join(engine_work, "artifact")
            if os.path.exists(marker):
                raise AssertionError(
                    f"{name}: engine_work not reset between attempts")
            with open(marker, "w", encoding="utf-8") as stream:
                stream.write("leftover engine state")
            if state["n"] > 2:  # only the retry's legs yield samples
                peak["observed"] = True
                peak["kib"] = 1234
            return []

        scenario = {"name": "retry-detect",
                    "write": [{"method": "iprange.v1.system.describe",
                               "compare": []}]}
        with tempfile.TemporaryDirectory() as work, \
                mock.patch.object(_bench, "run_engine", fake):
            report = _bench.run_perf(scenario, "rust", "go", 1, work)
        self.assertEqual(state["n"], 4, "exactly one retry")
        self.assertEqual(report["rust"]["child_max_rss_kib"]["median"], 1234)


class RetryEvidenceTest(unittest.TestCase):
    """r139 panel (tester F1): the discarded attempt must leak no
    elapsed sample, and the retry must be named on stderr — the two
    claims the earlier detectors left unasserted."""

    def _scenario(self):
        return {"name": "retry-detect",
                "write": [{"method": "iprange.v1.system.describe",
                           "compare": []}]}

    def _fake_engine(self, fill):
        def fake(binary, name, scenario, work, calls, peer=None,
                 engine_work=None, peak=None, prepare=False, deferred=None):
            if peak is not None:
                fill(peak)
            return []
        return fake

    def test_the_discarded_attempt_leaks_no_elapsed(self):
        import contextlib
        import io
        # Scripted clock: attempt 1's legs span 100 s each; the
        # retry's legs span 1 s each. Only the retry's spans may
        # reach the report (elapsed is SOW-0030's binding quantity).
        script = [0.0, 100.0, 100.0, 200.0, 300.0, 301.0, 301.0, 302.0]
        state = {"i": 0, "n": 0}

        def clock():
            index = state["i"]
            state["i"] += 1
            return script[index] if index < len(script) else 400.0 + index

        def fill(peak):
            state["n"] += 1
            if state["n"] > 2:
                peak["observed"] = True
                peak["kib"] = 1234

        with tempfile.TemporaryDirectory() as work, \
                mock.patch.object(_bench, "run_engine",
                                  self._fake_engine(fill)), \
                mock.patch("time.perf_counter", clock), \
                contextlib.redirect_stderr(io.StringIO()):
            report = _bench.run_perf(self._scenario(), "rust", "go", 1, work)
        for label in ("rust", "go"):
            span = report[label]["elapsed_seconds"]
            self.assertEqual(span["median"], 1.0,
                             "the failed attempt's elapsed must be discarded")
            self.assertEqual(span["max"], 1.0, label)

    def test_the_retry_is_named_on_stderr(self):
        import contextlib
        import io
        state = {"n": 0}

        def fill(peak):
            state["n"] += 1
            if state["n"] > 2:
                peak["observed"] = True
                peak["kib"] = 1234

        captured = io.StringIO()
        with tempfile.TemporaryDirectory() as work, \
                mock.patch.object(_bench, "run_engine",
                                  self._fake_engine(fill)), \
                contextlib.redirect_stderr(captured):
            _bench.run_perf(self._scenario(), "rust", "go", 1, work)
        self.assertIn("re-measured once", captured.getvalue(),
                      "the retry must be disclosed on stderr")

    def test_an_observed_miss_prints_no_retry_note(self):
        import contextlib
        import io

        def fill(peak):
            peak["observed"] = True

        captured = io.StringIO()
        with tempfile.TemporaryDirectory() as work, \
                mock.patch.object(_bench, "run_engine",
                                  self._fake_engine(fill)), \
                contextlib.redirect_stderr(captured):
            with self.assertRaisesRegex(AssertionError, "no RSS sample"):
                _bench.run_perf(self._scenario(), "rust", "go", 1, work)
        self.assertNotIn("re-measured once", captured.getvalue(),
                         "a real defect must not be disclosed as a retry")


class WorkDirCorrectnessModeTest(unittest.TestCase):
    """r139 panel (tester F2): the work-dir normalization is the
    entry-point's, not the perf branch's — correctness mode must
    receive the same absolute work dir."""

    def test_correctness_mode_receives_the_normalized_work_dir(self):
        import contextlib
        import io
        captured = {}

        def fake_run_engine(binary, name, scenario, work, calls, peer=None,
                            engine_work=None, peak=None, prepare=True,
                            deferred=None):
            captured["work"] = work
            return []

        with tempfile.TemporaryDirectory() as cwd:
            os.makedirs(os.path.join(cwd, "relwork"))
            previous = os.getcwd()
            os.chdir(cwd)
            try:
                argv = ["run.py", "--rust", "/bin/true", "--go", "/bin/true",
                        "--scenario", "s.json", "--work-dir", "relwork"]
                with mock.patch.object(sys, "argv", argv), \
                        mock.patch.object(_bench, "run_engine",
                                          fake_run_engine), \
                        mock.patch.object(_bench, "load_scenario",
                                          lambda path: {"name": "s"}), \
                        contextlib.redirect_stdout(io.StringIO()):
                    rc = _bench.main()
            finally:
                os.chdir(previous)
        self.assertEqual(rc, 0)
        self.assertTrue(os.path.isabs(captured["work"]))
        self.assertEqual(captured["work"], os.path.join(cwd, "relwork"))


class SamplerJoinTest(unittest.TestCase):
    """r141 panel (five lanes): the sampler join must be pinned — a
    late-folding sampler is the exact TOCTOU signature the join
    closes, and the join-deleted mutant must die here."""

    def _stub(self, work):
        stub = os.path.join(_HERE, "stub_engine.py")
        wrapper = os.path.join(work, "join_stub")
        with open(wrapper, "w", encoding="utf-8") as stream:
            stream.write(
                "#!/usr/bin/env python3\n"
                "import os, sys\n"
                "os.environ['STUB_EXIT'] = '0'\n"
                f"os.execv({stub!r}, [{stub!r}] + sys.argv[1:])\n")
        os.chmod(wrapper, 0o755)
        return wrapper

    def test_a_late_sampler_fold_causes_no_spurious_retry(self):
        import contextlib
        import io
        import time

        def slow_fold(pid):
            # The fold lands well after the stub run completes:
            # without the join the round sees no sample and
            # misclassifies the miss as a spawn race.
            time.sleep(0.75)
            return 4096

        scenario = {
            "name": "join-detect",
            "write": [{"method": "iprange.v1.system.describe",
                       "params": {},
                       "compare": ["method"],
                       "expect": {"method": "iprange.v1.system.describe"}}],
        }
        captured = io.StringIO()
        with tempfile.TemporaryDirectory() as work:
            stub = self._stub(work)
            with mock.patch.object(_bench, "child_hwm_kib", slow_fold), \
                    contextlib.redirect_stderr(captured):
                report = _bench.run_perf(scenario, stub, stub, 1, work)
        self.assertNotIn("re-measured once", captured.getvalue(),
                         "a late fold must not be misclassified as a spawn race")
        self.assertEqual(report["rust"]["child_max_rss_kib"]["median"], 4096)
        self.assertEqual(report["go"]["child_max_rss_kib"]["median"], 4096)

    def test_both_joins_are_unconditional_no_arg_calls(self):
        import ast
        import inspect
        import measure as measure_module
        # AST pins with a NAMED residual floor (r151 panel; the
        # SOW decoy-floor convention): the demonstrated forgery
        # lanes die — receiver aliases, unbound calls, dead twins,
        # getattr/dunder/setattr indirection, wrapper bindings,
        # typed rebindings, argument forms of every spelling. The
        # declared floor is deliberate multi-part deception
        # (e.g. operator.methodcaller) which no source pin can
        # universally exclude; the behavioral late-fold detector is
        # the semantic backstop (its 0.75 s margin is disclosed).
        for module, fname in ((_bench, "run_engine"),
                              (measure_module, "run_once")):
            source = inspect.getsource(getattr(module, fname))
            tree = ast.parse(source)
            joins = []
            for node in ast.walk(tree):
                if not isinstance(node, ast.Attribute) or node.attr != "join":
                    continue
                receiver = node.value
                if (isinstance(receiver, ast.Attribute)
                        and receiver.attr == "path"):
                    continue  # os.path.join is unrelated
                if isinstance(receiver, ast.Constant):
                    continue  # str.join is unrelated
                joins.append(node)
            self.assertEqual(
                len(joins), 1,
                "exactly one join reference outside os.path/str "
                "(receiver aliases, unbound calls, and dead twins "
                "all add references)")
            call = [node for node in ast.walk(tree)
                    if isinstance(node, ast.Call) and node.func is joins[0]]
            self.assertEqual(len(call), 1,
                             f"{fname}: the join reference must be the call")
            self.assertEqual(call[0].args, [],
                             f"{fname}: the join must take no argument")
            self.assertEqual(call[0].keywords, [],
                             f"{fname}: the join must take no keyword")
            for kind in ("getattr", "setattr", "delattr"):
                for node in ast.walk(tree):
                    if (isinstance(node, ast.Call)
                            and isinstance(node.func, ast.Name)
                            and node.func.id == kind and node.args
                            and isinstance(node.args[0], ast.Name)
                            and node.args[0].id == "sampler"):
                        self.fail(f"{fname}: {kind}(sampler, ...) could "
                                  f"hide a timed join")
            for node in ast.walk(tree):
                if (isinstance(node, ast.Attribute)
                        and isinstance(node.value, ast.Name)
                        and node.value.id == "sampler"
                        and node.attr.startswith("__")):
                    self.fail(f"{fname}: sampler.__dunder__ indirection "
                              f"could hide a timed join")
            for node in ast.walk(tree):
                if isinstance(node, ast.AnnAssign):
                    targets = [node.target]
                elif isinstance(node, ast.Assign):
                    targets = list(node.targets)
                else:
                    continue
                if not any(isinstance(t, ast.Name) and t.id == "sampler"
                           for t in targets):
                    continue
                value = node.value
                is_thread = (isinstance(value, ast.Call)
                             and isinstance(value.func, ast.Attribute)
                             and value.func.attr == "Thread"
                             and isinstance(value.func.value, ast.Name)
                             and value.func.value.id == "threading")
                is_none = (isinstance(value, ast.Constant)
                           and value.value is None)
                self.assertTrue(
                    is_thread or is_none,
                    f"{fname}: sampler may only bind None or a literal "
                    f"threading.Thread (a wrapper could hide a timed join)")

    def test_run_once_joins_before_reading_its_samples(self):
        import contextlib
        import io
        import time

        def slow_fold(pid):
            time.sleep(0.75)
            return 4096

        def slow_cpu(pid):
            time.sleep(0.75)
            return 0.5

        with tempfile.TemporaryDirectory() as work:
            stub = os.path.join(work, "join_stub")
            inner = os.path.join(_HERE, "stub_engine.py")
            with open(stub, "w", encoding="utf-8") as stream:
                stream.write(
                    "#!/usr/bin/env python3\n"
                    "import os, sys\n"
                    "os.environ['STUB_EXIT'] = '0'\n"
                    f"os.execv({inner!r}, [{inner!r}] + sys.argv[1:])\n")
            os.chmod(stub, 0o755)
            request = json.dumps({"jsonrpc": "2.0", "id": 1,
                                  "method": "iprange.v1.system.describe",
                                  "params": {}}).encode()
            with mock.patch("measure.child_hwm_kib", slow_fold), \
                    mock.patch("measure.child_tree_cpu_seconds", slow_cpu), \
                    contextlib.redirect_stderr(io.StringIO()):
                result = run_once([stub], stdin_bytes=request, cwd=work)
        self.assertEqual(result["child_max_rss_kib"], 4096,
                         "a late fold must be joined before the samples "
                         "are read")


class CorrectnessWorkDirStateTest(unittest.TestCase):
    """portability-r141 F1 via the r145 refusal contract: a reused
    --work-dir is refused with the honest message (never a
    misattributed engine error, never a delete) — the same-failure
    search over the perf path's retry fix."""

    def test_a_reused_work_dir_is_refused_not_cleaned(self):
        import contextlib
        import io
        calls = []

        def fake_run_engine(binary, name, scenario, work, calls_arg, peer=None,
                            engine_work=None, peak=None, prepare=True,
                            deferred=None):
            target = engine_work or os.path.join(work, name)
            os.makedirs(target, exist_ok=True)
            with open(os.path.join(target, "artifact"), "w",
                      encoding="utf-8") as stream:
                stream.write("engine artifact")
            calls.append(name)
            return []

        with tempfile.TemporaryDirectory() as work:
            argv = ["run.py", "--rust", "/bin/true", "--go", "/bin/true",
                    "--scenario", "s.json", "--work-dir", work]
            with mock.patch.object(sys, "argv", argv), \
                    mock.patch.object(_bench, "run_engine", fake_run_engine), \
                    mock.patch.object(_bench, "load_scenario",
                                      lambda path: {"name": "s"}), \
                    contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(_bench.main(), 0)
            # A second run over the same work dir is refused with the
            # honest message — never the misattributed engine error,
            # and never a delete (r145 panel's refusal contract).
            with mock.patch.object(sys, "argv", argv), \
                    mock.patch.object(_bench, "run_engine", fake_run_engine), \
                    mock.patch.object(_bench, "load_scenario",
                                      lambda path: {"name": "s"}), \
                    contextlib.redirect_stdout(io.StringIO()):
                with self.assertRaisesRegex(AssertionError,
                                            "refusing to touch"):
                    _bench.main()
            self.assertTrue(os.path.exists(
                os.path.join(work, "rust", "artifact")),
                "the second run must leave the first run's state intact")
        self.assertEqual(len(calls), 2, "one run, two engine legs")


class WorkDirForeignContentTest(unittest.TestCase):
    """r143/r145 panels: the runner must never delete foreign
    content. `rust` and `go` collide with real directory names
    (--work-dir v4 would target source trees); deletion authority is
    in-memory provenance only, so every pre-existing path is refused
    loudly and left untouched."""

    def test_foreign_content_is_refused_not_deleted(self):
        import contextlib
        import io
        with tempfile.TemporaryDirectory() as work:
            victim = os.path.join(work, "rust")
            os.makedirs(victim)
            precious = os.path.join(victim, "userdata.txt")
            with open(precious, "w", encoding="utf-8") as stream:
                stream.write("user data that must survive")

            def fake_run_engine(*args, **kwargs):
                raise AssertionError("the run must not reach the engine")

            argv = ["run.py", "--rust", "/bin/true", "--go", "/bin/true",
                    "--scenario", "s.json", "--work-dir", work]
            with mock.patch.object(sys, "argv", argv), \
                    mock.patch.object(_bench, "run_engine", fake_run_engine), \
                    mock.patch.object(_bench, "load_scenario",
                                      lambda path: {"name": "s"}), \
                    contextlib.redirect_stdout(io.StringIO()):
                with self.assertRaisesRegex(AssertionError,
                                            "refusing to touch"):
                    _bench.main()
            self.assertTrue(os.path.exists(precious),
                            "foreign content must survive a refused run")
            self.assertEqual(os.listdir(victim), ["userdata.txt"],
                             "a refusal must leave the directory untouched")

    def test_prepare_engine_dirs_wipes_only_process_owned_state(self):
        with tempfile.TemporaryDirectory() as work:
            target = os.path.join(work, "perf-rust-0")
            _bench.prepare_engine_dirs(target)
            with open(os.path.join(target, "artifact"), "w",
                      encoding="utf-8") as stream:
                stream.write("engine state")
            # The retry's restart: this process created the directory,
            # so it may be wiped and remade.
            _bench.prepare_engine_dirs(target, may_wipe={target})
            self.assertEqual(os.listdir(target), [])

    def test_any_preexisting_path_is_refused_even_when_empty(self):
        with tempfile.TemporaryDirectory() as work:
            target = os.path.join(work, "rust")
            os.makedirs(target)
            with self.assertRaisesRegex(AssertionError, "refusing to touch"):
                _bench.prepare_engine_dirs(target)
            self.assertTrue(os.path.isdir(target))

    def test_a_symlinked_engine_path_is_refused(self):
        # Latent platform step (Windows symlink privilege); no
        # Windows leg runs this suite (recorded decision 2026-09-17).
        with tempfile.TemporaryDirectory() as work:
            real = os.path.join(work, "real")
            os.makedirs(real)
            precious = os.path.join(real, "userdata.txt")
            with open(precious, "w", encoding="utf-8") as stream:
                stream.write("must survive")
            link = os.path.join(work, "rust")
            os.symlink(real, link)
            with self.assertRaisesRegex(AssertionError, "refusing to touch"):
                _bench.prepare_engine_dirs(link, may_wipe={link})
            self.assertTrue(os.path.exists(precious))

    def test_no_on_disk_state_grants_deletion_authority(self):
        with tempfile.TemporaryDirectory() as work:
            target = os.path.join(work, "rust")
            os.makedirs(target)
            precious = os.path.join(target, "userdata.txt")
            with open(precious, "w", encoding="utf-8") as stream:
                stream.write("content must survive any on-disk claim")
            # Whatever files a user places alongside (old marker
            # names included), a path this process did not create is
            # refused — deletion authority is in-memory only.
            for name in (target + ".owned", target + ".iprange-bench-owned"):
                with open(name, "w", encoding="utf-8") as stream:
                    stream.write("iprange-bench-owned-v1 1 2\n")
                with self.assertRaisesRegex(AssertionError,
                                            "refusing to touch"):
                    _bench.prepare_engine_dirs(target)
                os.remove(name)
            self.assertTrue(os.path.exists(precious))

    def test_a_refusal_has_no_side_effects(self):
        with tempfile.TemporaryDirectory() as work:
            good = os.path.join(work, "rust")
            bad = os.path.join(work, "go")
            os.makedirs(bad)
            with self.assertRaisesRegex(AssertionError, "refusing to touch"):
                _bench.prepare_engine_dirs(good, bad)
            self.assertFalse(os.path.exists(good),
                             "the verified path must not be created "
                             "when a sibling is refused")

    def test_a_failed_wipe_raises_instead_of_swallowing(self):
        real_rmtree = shutil.rmtree

        def flaky_rmtree(path, ignore_errors=False, onerror=None,
                         onexc=None, **kwargs):
            # Emulates every demonstrated swallow idiom: the
            # ignore_errors spelling, a quiet onerror/onexc handler,
            # and outer except handlers of selective classes (a
            # plain OSError defeats handlers that re-raise only
            # PermissionError). The declared residual is a deliberate
            # handler catching exactly this class — named floor.
            if ignore_errors:
                return real_rmtree(path, **kwargs)
            if onerror is not None or onexc is not None:
                return real_rmtree(path, **kwargs)
            raise OSError("simulated wipe denial")

        with tempfile.TemporaryDirectory() as work:
            target = os.path.join(work, "perf-rust-0")
            _bench.prepare_engine_dirs(target)
            with mock.patch.object(_bench.shutil, "rmtree", flaky_rmtree):
                with self.assertRaisesRegex(OSError,
                                            "simulated wipe denial"):
                    _bench.prepare_engine_dirs(target, may_wipe={target})
