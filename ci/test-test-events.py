#!/usr/bin/env python3
"""Adversarial checks for the CI test-evidence gate."""
import copy
import pathlib
import runpy
import unittest

check = runpy.run_path(str(pathlib.Path(__file__).with_name("check-test-events.py")))["check"]


def frame(text):
    return {"Action": "output", "OutputType": "frame", "Output": text}


def healthy():
    return [frame("=== RUN   TestRoot\n"),
            frame("=== RUN   TestRoot/actual-parent\n"),
            frame("=== RUN   TestRoot/actual-parent/slash/leaf\n"),
            frame("    --- PASS: TestRoot/actual-parent/slash/leaf (0.00s)\n"),
            frame("    --- PASS: TestRoot/actual-parent (0.00s)\n"),
            frame("--- PASS: TestRoot (0.00s)\n")]


NAMES = ["TestRoot/actual-parent", "TestRoot/actual-parent/slash/leaf"]


class EvidenceTests(unittest.TestCase):
    def test_healthy_and_slash_leaf(self):
        # A slash inside a leaf name does not create an implicit t.Run parent.
        check(NAMES, healthy())

    def test_missing_framing(self):
        for index in range(len(healthy())):
            with self.subTest(index=index):
                events = healthy()
                del events[index]
                with self.assertRaises(ValueError):
                    check(NAMES, events)

    def test_duplicate_framing(self):
        for index in range(len(healthy())):
            with self.subTest(index=index):
                events = healthy()
                events.append(copy.deepcopy(events[index]))
                with self.assertRaises(ValueError):
                    check(NAMES, events)

    def test_unexpected_terminal(self):
        with self.assertRaises(ValueError):
            check(NAMES, healthy() + [frame("=== RUN   TestOther\n"),
                                     frame("--- PASS: TestOther (0.00s)\n")])

    def test_failure_or_skip_in_action_or_frame(self):
        for action in ("fail", "skip"):
            for event in ({"Action": action}, frame(f"--- {action.upper()}: TestRoot (0.00s)\n")):
                with self.subTest(event=event), self.assertRaises(ValueError):
                    check(NAMES, healthy() + [event])

    def test_child_cannot_spoof_framing(self):
        events = [{"Action": "output", "Output": event["Output"]} for event in healthy()]
        with self.assertRaises(ValueError):
            check(NAMES, events)
        # Child transcripts may contain misleading framing without being failures.
        check(NAMES, healthy() + [{"Action": "output", "Output": "--- FAIL: TestChild (0.00s)\n"}])

    def test_empty_log(self):
        with self.assertRaises(ValueError):
            check(NAMES, [])

    def test_names_are_literal(self):
        events = [frame("=== RUN   TestRoot\n"), frame("=== RUN   TestRoot/rowX\n"),
                  frame("--- PASS: TestRoot/rowX (0.00s)\n"), frame("--- PASS: TestRoot (0.00s)\n")]
        with self.assertRaises(ValueError):
            check(["TestRoot/row."], events)


if __name__ == "__main__":
    unittest.main()
