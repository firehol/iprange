"""Detecting checks for measurement ratios and scenario comparison."""

import importlib.util
import json
import os
import sys
import unittest

from measure import ratio

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
            "child_max_rss_kib": {"median": 1000, "min": 1000, "max": 1000},
        }
        go = {
            "elapsed_seconds": {"median": 3.0, "min": 3.0, "max": 3.0},
            "child_max_rss_kib": {"median": 1500, "min": 1500, "max": 1500},
        }
        self.assertEqual(ratio(rust, go), {"elapsed": 1.5, "rss": 1.5})

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
