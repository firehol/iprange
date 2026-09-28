"""Detecting checks for measurement ratios and scenario comparison."""

import importlib.util
import json
import os
import sys
import unittest

from measure import child_cpu_seconds, measure, parse_stat_cpu, ratio, run_once

_HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.dirname(_HERE))
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
    def test_zero_rounds_is_refused(self):
        with self.assertRaises(ValueError):
            measure(["/bin/true"], 0)


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
