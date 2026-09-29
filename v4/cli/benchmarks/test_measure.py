"""Detecting checks for measurement ratios and scenario comparison."""

import importlib.util
import json
import os
import sys
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
