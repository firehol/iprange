"""Detecting checks for measurement ratios and scenario comparison."""

import importlib.util
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
