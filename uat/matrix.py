#!/usr/bin/env python3
"""Turn a Robot Framework output.xml into the pass matrix for the README.

The matrix is per suite, not per case: a reader wants to know which areas of
the operator work against m80, and a 60-row table of individual case names
buries that. Failures are listed by name underneath, since those are the rows
someone has to act on.

    python3 uat/matrix.py uat-results/output.xml
"""
from __future__ import annotations

import sys
import xml.etree.ElementTree as ET
from pathlib import Path


def suites_with_tests(root: ET.Element):
    """Yield the leaf suites — the ones that actually hold test cases."""
    for suite in root.iter("suite"):
        tests = suite.findall("test")
        if tests:
            yield suite, tests


def verdict(test: ET.Element) -> str:
    status = test.find("status")
    return status.get("status", "FAIL") if status is not None else "FAIL"


def failure_reason(test: ET.Element) -> str:
    status = test.find("status")
    text = (status.text or "").strip() if status is not None else ""
    # Robot puts the whole traceback in; the first line is the actionable part.
    return text.splitlines()[0] if text else "(no message)"


def main() -> int:
    if len(sys.argv) != 2:
        print(__doc__)
        return 2
    path = Path(sys.argv[1])
    root = ET.parse(path).getroot()

    rows, failures, total_pass, total = [], [], 0, 0
    for suite, tests in suites_with_tests(root):
        passed = sum(1 for t in tests if verdict(t) == "PASS")
        total_pass += passed
        total += len(tests)
        mark = "✅" if passed == len(tests) else ("⚠️" if passed else "❌")
        rows.append((mark, suite.get("name", "?"), passed, len(tests)))
        for t in tests:
            if verdict(t) != "PASS":
                failures.append((suite.get("name", "?"), t.get("name", "?"), failure_reason(t)))

    print(f"**{total_pass} of {total} cases pass.**\n")
    print("| | Suite | Passed |")
    print("|---|---|---|")
    for mark, name, passed, n in rows:
        print(f"| {mark} | {name} | {passed}/{n} |")

    if failures:
        print("\n### Failures\n")
        for suite, name, why in failures:
            print(f"- **{suite} — {name}**  \n  `{why[:200]}`")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
