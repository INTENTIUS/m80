#!/usr/bin/env python3
"""Gate a UAT run against the recorded pass matrix, in both directions.

    python3 uat/assert-matrix.py uat-results/output.xml uat/expected-failures.txt

A failure not in the expected list is a regression and fails the gate. An
expected failure that now PASSES also fails the gate — good news, but the
recorded matrix (docs/kubemicrovm.md and the expectations file) must move
with reality, and a gate that lets the record rot in the flattering
direction is how "50 of 63" stops being true without anyone deciding it.

The expectations file is one full Robot test name per line, `#` comments and
blank lines ignored. Skipped tests count as neither passed nor failed.
"""
from __future__ import annotations

import sys
import xml.etree.ElementTree as ET
from pathlib import Path


def main() -> int:
    if len(sys.argv) != 3:
        print(__doc__)
        return 2
    output = ET.parse(sys.argv[1]).getroot()
    expected = {
        line.strip()
        for line in Path(sys.argv[2]).read_text().splitlines()
        if line.strip() and not line.strip().startswith("#")
    }

    passed, failed = set(), set()
    for test in output.iter("test"):
        status = test.find("status")
        verdict = status.get("status", "FAIL") if status is not None else "FAIL"
        name = test.get("name", "(unnamed)")
        (passed if verdict == "PASS" else failed if verdict == "FAIL" else set()).add(name)

    regressions = sorted(failed - expected)
    surprises = sorted(expected & passed)
    missing = sorted(expected - failed - passed)

    print(f"{len(passed)} passed, {len(failed)} failed ({len(expected)} expected)")
    ok = True
    if regressions:
        ok = False
        print("\nNEW failures — regressions:")
        for name in regressions:
            print(f"  ✗ {name}")
    if surprises:
        ok = False
        print("\nExpected failures that now PASS — update uat/expected-failures.txt")
        print("and the pass matrix in docs/kubemicrovm.md, then re-run:")
        for name in surprises:
            print(f"  ✓ {name}")
    if missing:
        ok = False
        print("\nExpected failures that did not run at all — the suite moved underneath us:")
        for name in missing:
            print(f"  ? {name}")
    if ok:
        print("matrix holds: every failure expected, every expectation still failing")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
