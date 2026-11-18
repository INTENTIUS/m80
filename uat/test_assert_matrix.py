"""Run: python3 -m unittest uat/test_assert_matrix.py"""
import importlib.util
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).parent
SCRIPT = HERE / "assert-matrix.py"


def run(results: dict[str, str], expectations: str):
    tests = "".join(
        f'<test name="{n}"><status status="{s}"/></test>' for n, s in results.items()
    )
    with tempfile.TemporaryDirectory() as d:
        x, e = Path(d, "output.xml"), Path(d, "expected.txt")
        x.write_text(f"<robot><suite>{tests}</suite></robot>")
        e.write_text(expectations)
        p = subprocess.run(
            [sys.executable, str(SCRIPT), str(x), str(e)], capture_output=True, text=True
        )
    return p.returncode, p.stdout


EXP = "# c\nA strict\nflaky: B flaky\n"


class Matrix(unittest.TestCase):
    def test_flaky_may_fail(self):
        self.assertEqual(run({"A strict": "FAIL", "B flaky": "FAIL", "C": "PASS"}, EXP)[0], 0)

    def test_flaky_may_pass(self):
        code, out = run({"A strict": "FAIL", "B flaky": "PASS", "C": "PASS"}, EXP)
        self.assertEqual(code, 0)
        self.assertIn("flaky (not fatal): PASS", out)

    def test_strict_pass_still_fatal(self):
        self.assertEqual(run({"A strict": "PASS", "B flaky": "FAIL"}, EXP)[0], 1)

    def test_new_failure_still_fatal(self):
        self.assertEqual(run({"A strict": "FAIL", "B flaky": "FAIL", "C": "FAIL"}, EXP)[0], 1)

    def test_flaky_missing_is_fatal(self):
        self.assertEqual(run({"A strict": "FAIL"}, EXP)[0], 1)


if __name__ == "__main__":
    unittest.main()
