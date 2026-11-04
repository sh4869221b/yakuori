#!/usr/bin/env python3
"""Check frozen test names against Go driver framing, never child transcripts."""
import argparse
from collections import Counter
import json
import pathlib
import re


def check(names, events):
    allowed = set(names)
    allowed.update(name.split("/", 1)[0] for name in names)
    runs, passes = Counter(), Counter()
    for event in events:
        if event["Action"] in ("fail", "skip"):
            raise ValueError("lane reported FAIL or SKIP")
        if event.get("OutputType") != "frame":
            continue
        for line in event["Output"].splitlines():
            if re.match(r"^\s*--- (FAIL|SKIP)", line):
                raise ValueError("lane reported FAIL or SKIP")
            run = re.match(r"^\s*=== RUN   (\S+)\s*$", line)
            passed = re.match(r"^\s*--- PASS: (\S+) \(", line)
            if run:
                runs[run[1]] += 1
            if passed:
                passes[passed[1]] += 1
    if not passes:
        raise ValueError("lane selected no PASS output")
    unexpected = (runs.keys() | passes.keys()) - allowed
    if unexpected:
        raise ValueError(f"unexpected terminals: {sorted(unexpected)}")
    for name in sorted(allowed):
        if runs[name] != 1 or passes[name] != 1:
            raise ValueError(f"{name} RUN={runs[name]} PASS={passes[name]}, want 1/1")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("names", type=pathlib.Path)
    parser.add_argument("events", type=pathlib.Path)
    args = parser.parse_args()
    try:
        names = [name for name in args.names.read_text().splitlines() if name]
        with args.events.open() as source:
            check(names, (json.loads(line) for line in source))
    except (ValueError, KeyError, TypeError, OSError) as error:
        parser.exit(1, f"{args.events}: {error}\n")


if __name__ == "__main__":
    main()
