"""Ground-truth checks for the seeded feed generator."""

import unittest

import io
import ipaddress

import json
import os

from generate import (
    IPV6_BASE,
    churn,
    covered,
    day_feeds,
    diff_counts,
    generate,
    ipv6_address,
    merged_count,
    overlap_count,
    parse_ipv6,
    provider_join,
    resolve_direct,
    last_seen_days,
    last_seen_refresh,
    retained_count,
    write_ipv6,
)


class GeneratorTest(unittest.TestCase):
    def test_same_seed_is_same_feed(self):
        self.assertEqual(generate(7, 20, 4), generate(7, 20, 4))

    def test_overlap_merges(self):
        self.assertEqual(merged_count([(0, 3), (2, 5), (10, 10)]), 7)

    def test_adjacent_ranges_merge(self):
        self.assertEqual(merged_count([(0, 1), (2, 3)]), 4)

    def test_last_seen_expires_at_the_cutoff(self):
        # Day 1 stores 1-3 at value 1. Day 2 updates 3 and adds 4.
        # Day 3 cutoff 1 drops value 1 and keeps value 2. A refresh
        # that ignored the cutoff would report removed 0.
        first = last_seen_refresh([], [(1, 3)], 1, 0)
        self.assertEqual(first["added"], 3)
        self.assertEqual(first["after"], 3)
        second = last_seen_refresh(first["stored"], [(3, 4)], 2, 0)
        self.assertEqual(
            (second["added"], second["removed"], second["unchanged"], second["changed"], second["after"]),
            (1, 0, 2, 1, 4),
        )
        third = last_seen_refresh(second["stored"], [(4, 4)], 3, 1)
        self.assertEqual(
            (third["added"], third["removed"], third["unchanged"], third["changed"], third["after"]),
            (0, 2, 1, 1, 2),
        )

    def test_last_seen_seven_day_corpus_expires(self):
        days = day_feeds(11, 7, 20, 4, 64)
        steps = last_seen_days(days, range(1, 8), [max(0, day - 2) for day in range(1, 8)])
        # Cutoff is refresh_value - 2. Day 3 is the first expiry: three
        # addresses stored at 1 are absent from that day's coverage.
        self.assertEqual(
            [(step["added"], step["removed"], step["unchanged"], step["changed"], step["after"]) for step in steps],
            [
                (35, 0, 0, 0, 35),
                (21, 0, 8, 27, 56),
                (7, 3, 22, 31, 60),
                (2, 8, 11, 41, 54),
                (6, 0, 12, 42, 60),
                (0, 1, 20, 39, 59),
                (1, 2, 8, 49, 58),
            ],
        )

    def test_churn_names_each_day(self):
        days = [[(0, 4)], [(2, 6)], [(2, 2)]]
        steps = churn(days)
        self.assertEqual(steps[1], {"unchanged": 3, "removed": 2, "added": 2})
        self.assertEqual(steps[2], {"unchanged": 1, "removed": 4, "added": 0})

    def test_seven_day_corpus_partitions_each_day(self):
        days = day_feeds(11, 7, 20, 4, 64)
        steps = churn(days)
        self.assertEqual(steps, [
            {"unchanged": 0, "removed": 0, "added": 35},
            {"unchanged": 27, "removed": 8, "added": 21},
            {"unchanged": 26, "removed": 22, "added": 12},
            {"unchanged": 27, "removed": 11, "added": 16},
            {"unchanged": 31, "removed": 12, "added": 17},
            {"unchanged": 28, "removed": 20, "added": 11},
            {"unchanged": 31, "removed": 8, "added": 19},
        ])
        previous = 0
        for day, step in zip(days, steps):
            self.assertEqual(step["unchanged"] + step["removed"], previous)
            self.assertEqual(step["unchanged"] + step["added"], merged_count(day))
            previous = merged_count(day)

    def test_seven_day_scenario_names_the_generator(self):
        steps = churn(day_feeds(11, 7, 20, 4, 64))
        path = os.path.join(os.path.dirname(__file__), "scenarios", "s2-seven-day-refresh.json")
        with open(path, encoding="utf-8") as stream:
            scenario = json.load(stream)
        refreshes = [call for call in scenario["calls"] if call["method"] == "iprange.v1.retention.first_seen.refresh"]
        self.assertEqual(len(refreshes), 7)
        for call, step in zip(refreshes, steps):
            expect = call["expect"]
            self.assertEqual(int(expect["report.added_addresses"]), step["added"])
            self.assertEqual(int(expect["report.removed_addresses"]), step["removed"])
            self.assertEqual(int(expect["report.unchanged_value_addresses"]), step["unchanged"])

    def test_seven_day_last_seen_scenario_names_the_generator(self):
        steps = last_seen_days(day_feeds(11, 7, 20, 4, 64), range(1, 8), [max(0, day - 2) for day in range(1, 8)])
        path = os.path.join(os.path.dirname(__file__), "scenarios", "s2-seven-day-last-seen.json")
        with open(path, encoding="utf-8") as stream:
            scenario = json.load(stream)
        refreshes = [call for call in scenario["calls"] if call["method"] == "iprange.v1.retention.last_seen.refresh"]
        self.assertEqual(len(refreshes), 7)
        self.assertEqual(int(refreshes[2]["params"]["cutoff"]), 1)
        self.assertEqual(int(refreshes[2]["expect"]["report.removed_addresses"]), 3)
        for call, step in zip(refreshes, steps):
            expect = call["expect"]
            self.assertEqual(int(expect["report.added_addresses"]), step["added"])
            self.assertEqual(int(expect["report.removed_addresses"]), step["removed"])
            self.assertEqual(int(expect["report.unchanged_value_addresses"]), step["unchanged"])
            self.assertEqual(int(expect["report.changed_value_addresses"]), step["changed"])
            self.assertEqual(int(expect["report.after_addresses"]), step["after"])

    def test_retention_keeps_values_above_cutoff(self):
        ranges = [(0, 9, 10), (8, 11, 10), (20, 20, 5)]
        self.assertEqual(retained_count(ranges, 9), 12)
        self.assertEqual(retained_count(ranges, 10), 0)

    def test_overlap_is_the_unchanged_count(self):
        self.assertEqual(overlap_count([(0, 5)], [(4, 9)]), 2)

    def test_diff_counts_match_the_known_replace(self):
        before = [(0, 3), (10, 10)]
        after = [(2, 6), (20, 20)]
        self.assertEqual(diff_counts(before, after), {"unchanged": 2, "removed": 3, "added": 4})

    def test_small_space_overlaps(self):
        ranges = generate(7, 20, 4, space=64)
        self.assertEqual(len(ranges), 20)
        self.assertLess(merged_count(ranges), 80)
        self.assertEqual(merged_count(ranges), merged_count(generate(7, 20, 4, space=64)))

    def test_later_provider_row_wins(self):
        resolved = resolve_direct([(0, 9, 1), (5, 12, 2)])
        self.assertEqual(resolved, [(0, 4, 1), (5, 12, 2)])

    def test_provider_join_matches_the_probed_country(self):
        feeds = [
            ("alpha", [(12, 16)]),
            ("beta", [(18, 21)]),
            ("gamma", [(35, 36)]),
            ("delta", [(100, 100)]),
        ]
        country = [(10, 19, 1), (30, 39, 2)]
        report = provider_join(feeds, country)
        self.assertEqual(report["selected"], 12)
        self.assertEqual(report["mapped"], 9)
        self.assertEqual(report["unmapped"], 3)
        self.assertEqual(sorted(report["cells"], key=lambda cell: (cell[0], cell[1] is None, cell[1] or 0)), [
            ("alpha", 1, 5),
            ("beta", 1, 2),
            ("beta", None, 2),
            ("delta", None, 1),
            ("gamma", 2, 2),
        ])

    def test_provider_scenario_names_both_sets(self):
        feeds = [
            ("alpha", [(12, 16)]),
            ("beta", [(18, 21)]),
            ("gamma", [(35, 36)]),
            ("delta", [(100, 100)]),
        ]
        path = os.path.join(os.path.dirname(__file__), "scenarios", "s5-provider-joins.json")
        with open(path, encoding="utf-8") as stream:
            scenario = json.load(stream)
        joins = [call for call in scenario["calls"] if call["method"] == "iprange.v1.join.direct"]
        self.assertEqual(len(joins), 2)
        providers = {
            "country": [(10, 19, 1), (30, 39, 2)],
            "asn": [(15, 24, 100), (50, 54, 200)],
        }
        for call, (name, rows) in zip(joins, providers.items()):
            report = provider_join(feeds, rows)
            self.assertEqual(call["expect"]["report.selected_addresses"], str(report["selected"]))
            self.assertEqual(call["expect"]["report.mapped_addresses"], str(report["mapped"]))
            self.assertEqual(call["expect"]["report.unmapped_addresses"], str(report["unmapped"]))
            self.assertEqual(int(call["expect"]["report.result_cell_count"]), len(report["cells"]))
            for feed, value, count in report["cells"]:
                rendered = "null" if value is None else str(value)
                self.assertIn(f"{feed},{rendered},{count}", call["expect_file_contains"]["text"])
            self.assertIn(name, call["params"]["direct"]["path"])

    def test_overlapping_feeds_are_one_selected_union(self):
        feeds = [("zeta", [(0, 9)]), ("alpha", [(5, 14)])]
        report = provider_join(feeds, [(0, 9, 1), (5, 12, 2)])
        self.assertEqual(report["selected"], 15)
        self.assertEqual(report["mapped"], 13)
        self.assertEqual(report["unmapped"], 2)
        self.assertEqual(len(report["cells"]), 4)

    def test_seeded_ipv6_keeps_the_integer_corpus(self):
        ranges = generate(7, 20, 4, space=64)
        self.assertEqual(merged_count(ranges), 47)
        self.assertEqual(len(covered(ranges)), 5)
        stream = io.StringIO()
        write_ipv6(ranges, stream)
        text = stream.getvalue()
        self.assertIn("2001:db8::", text)
        self.assertNotIn(".", text)
        self.assertEqual(parse_ipv6(text), ranges)
        self.assertEqual(merged_count(parse_ipv6(text)), 47)
        self.assertEqual(ipv6_address(1), "2001:db8::1")
        self.assertEqual(int(ipaddress.IPv6Address(ipv6_address(0))), IPV6_BASE)


if __name__ == "__main__":
    unittest.main()
