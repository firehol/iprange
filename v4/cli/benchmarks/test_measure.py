"""The measurement reports the child, not the runner."""

import os
import subprocess
import sys
import tempfile
import textwrap
import unittest

from measure import measure, run_once


class MeasureTest(unittest.TestCase):
    def test_child_peak_is_not_the_runner(self):
        script = textwrap.dedent(
            """
            import sys
            blob = bytearray(8 * 1024 * 1024)
            blob[0] = 1
            sys.stdout.buffer.write(b"ok\\n")
            """
        )
        with tempfile.TemporaryDirectory() as work:
            path = os.path.join(work, "child.py")
            with open(path, "w", encoding="utf-8") as stream:
                stream.write(script)
            sample = run_once([sys.executable, path])
        self.assertGreater(sample["child_max_rss_kib"], 1024)
        self.assertLess(sample["elapsed_seconds"], 5)

    def test_failed_child_is_not_a_sample(self):
        with self.assertRaises(AssertionError):
            run_once([sys.executable, "-c", "raise SystemExit(3)"])

    def test_median_uses_rounds(self):
        report = measure([sys.executable, "-c", "print('ok')"], 3)
        self.assertEqual(report["rounds"], 3)
        self.assertLessEqual(report["elapsed_seconds"]["min"], report["elapsed_seconds"]["median"])
        self.assertGreaterEqual(report["elapsed_seconds"]["max"], report["elapsed_seconds"]["median"])


if __name__ == "__main__":
    unittest.main()
