"""Ground-truth checks for the seeded feed generator."""

import unittest

from generate import generate, merged_count


class GeneratorTest(unittest.TestCase):
    def test_same_seed_is_same_feed(self):
        self.assertEqual(generate(7, 20, 4), generate(7, 20, 4))

    def test_overlap_merges(self):
        self.assertEqual(merged_count([(0, 3), (2, 5), (10, 10)]), 7)

    def test_adjacent_ranges_merge(self):
        self.assertEqual(merged_count([(0, 1), (2, 3)]), 4)

    def test_small_space_overlaps(self):
        ranges = generate(7, 20, 4, space=64)
        self.assertEqual(len(ranges), 20)
        self.assertLess(merged_count(ranges), 80)
        self.assertEqual(merged_count(ranges), merged_count(generate(7, 20, 4, space=64)))


if __name__ == "__main__":
    unittest.main()
