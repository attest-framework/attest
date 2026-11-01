"""Unit tests for the pure functions in post_annotations.py.

The script is stdlib-only so the tests run in the same Ubuntu runner
without `pip install`: ``python3 -m unittest discover -s
.github/actions/pr-annotate`` (CI: ci-github-config.yml). fixtures/report-v2.json
follows proto/attest/v1/report-v2.schema.json.
"""

from __future__ import annotations

import importlib.util
import json
import unittest
from pathlib import Path


def _load_module():
    """Import the action script as a module without altering sys.path."""
    here = Path(__file__).parent
    spec = importlib.util.spec_from_file_location(
        "post_annotations", here / "post_annotations.py"
    )
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


_pa = _load_module()


_REPORT = json.loads((Path(__file__).parent / "fixtures" / "report-v2.json").read_text())

# A patch whose head side shows lines 1-5 and 20-22 of tests/test_refund.py.
_PATCH = """@@ -1,3 +1,5 @@
 import pytest
+
+
 def test_refund_issued(attest):
     pass
@@ -18,2 +20,3 @@ def helper():
 x = 1
-y = 2
+y = 3
+z = 4
\\ No newline at end of file"""


class TestSourceLocation(unittest.TestCase):
    def test_report_row_location_is_used(self) -> None:
        self.assertEqual(
            _pa._source_location({"file": "tests/test_refund.py", "line": 4}),
            ("tests/test_refund.py", 4),
        )

    def test_missing_location_returns_none_never_line_one(self) -> None:
        row = {"explanation": "ok at tests/test_x.py::TestSuite::test_case before fail"}
        self.assertIsNone(_pa._source_location(row))

    def test_malformed_locations_are_rejected(self) -> None:
        for row in (
            {"file": "/abs/test.py", "line": 3},
            {"file": "C:/x/test.py", "line": 3},
            {"file": "tests\\\\test.py", "line": 3},
            {"file": "../outside/test.py", "line": 3},
            {"file": "tests//test.py", "line": 3},
            {"file": "tests/test.py", "line": 0},
            {"file": "tests/test.py", "line": True},
            {"file": "tests/test.py", "line": "4"},
            {"file": "", "line": 4},
            {"file": "tests/a\nb.py", "line": 4},
            {"file": "tests/`x`.py", "line": 4},
            {"file": "tests/%0A.py", "line": 4},
            {"file": "tests/\x1b[31m.py", "line": 4},
            {"line": 4},
        ):
            with self.subTest(row=row):
                self.assertIsNone(_pa._source_location(row))


class TestDiffLines(unittest.TestCase):
    def test_head_lines_of_every_hunk(self) -> None:
        self.assertEqual(_pa._diff_lines(_PATCH), {1, 2, 3, 4, 5, 20, 21, 22})

    def test_empty_patch_has_no_lines(self) -> None:
        self.assertEqual(_pa._diff_lines(""), set())

    def test_unparseable_hunk_header_raises(self) -> None:
        with self.assertRaises(ValueError):
            _pa._diff_lines("@@ garbage @@")


class TestBuildReview(unittest.TestCase):
    def _failures(self) -> list[dict]:
        return [r for r in _REPORT["results"] if r["status"] in ("hard_fail", "soft_fail")]

    def test_failure_on_a_diff_line_is_an_inline_comment_on_the_test_line(self) -> None:
        commentable = {"tests/test_refund.py": _pa._diff_lines(_PATCH)}

        payload, annotations = _pa.build_review(_REPORT, self._failures(), commentable, "abc123")

        self.assertEqual(payload["commit_id"], "abc123")
        self.assertEqual(
            [(c["path"], c["line"], c["side"]) for c in payload["comments"]],
            [("tests/test_refund.py", 4, "RIGHT")],
        )
        self.assertIn('contains \\"store credit\\"', json.dumps(payload["comments"][0]["body"]))
        self.assertEqual(len(annotations), 1)
        self.assertTrue(annotations[0].startswith("::error file=tests/test_refund.py,line=4,"))

    def test_failure_outside_the_diff_goes_to_the_body_with_its_location(self) -> None:
        payload, annotations = _pa.build_review(_REPORT, self._failures(), {}, "abc123")

        self.assertEqual(payload["comments"], [])
        self.assertIn("`tests/test_refund.py:4` (not in this diff)", payload["body"])
        self.assertTrue(annotations[0].startswith("::error file=tests/test_refund.py,line=4,"))

    def test_failure_without_location_goes_to_the_body_without_annotation(self) -> None:
        row = {k: v for k, v in self._failures()[0].items() if k not in ("test_id", "file", "line")}

        payload, annotations = _pa.build_review(_REPORT, [row], {"tests/test_refund.py": {1}}, "c")

        self.assertEqual(payload["comments"], [])
        self.assertEqual(annotations, [])
        self.assertIn("without a source location", payload["body"])
        self.assertIn(row["assertion_id"], payload["body"])

    def test_soft_failure_is_a_warning_annotation(self) -> None:
        row = {**self._failures()[0], "status": "soft_fail"}

        _, annotations = _pa.build_review(_REPORT, [row], {}, "c")

        self.assertTrue(annotations[0].startswith("::warning "))


class TestUntrustedMarkdown(unittest.TestCase):
    def test_report_fields_render_only_inside_code_spans(self) -> None:
        payload = "a\n@org/team &#64;user [x](http://e) <img src=x> http://evil.example org/repo#1"
        out = _pa._format_failure(
            {
                "assertion_id": "a`\n# Attest passed",
                "status": "hard_fail",
                "score": "high",
                "type": "x\n\n## fake",
                "expected": payload,
            }
        )

        self.assertNotIn("\n#", out)
        self.assertIn("**Attest assertion failed: `a' # Attest passed`**", out)
        expected_line = next(l for l in out.splitlines() if l.startswith("- **Expected:**"))
        self.assertEqual(
            expected_line,
            "- **Expected:** `a @org/team &#64;user [x](http://e) <img src=x> "
            "http://evil.example org/repo#1`",
        )
        # Outside code spans only the fixed labels remain.
        outside = "".join(out.split("`")[0::2])
        for token in ("@", "http", "<", "[", "&#", "#1"):
            self.assertNotIn(token, outside)

    def test_non_numeric_summary_fields_render_as_zero(self) -> None:
        out = _pa._summary_body({"summary": {"total": "@x"}, "total_cost": float("nan")}, [])

        self.assertIn("of 0.", out)
        self.assertIn("$0.000000", out)
        self.assertIn("L0 ", _pa._format_failure({"layer": float("inf")}))


class TestMainArguments(unittest.TestCase):
    def test_bad_repo_or_commit_exits_1_before_any_request(self) -> None:
        import contextlib
        import io
        import sys

        for argv in (
            ["--repo", "o/r/../x", "--commit", "a" * 40],
            ["--repo", "o/r", "--commit", "HEAD"],
        ):
            with self.subTest(argv=argv):
                sys.argv = ["post_annotations.py", "--report", "r.json", "--pr", "1", *argv]
                with contextlib.redirect_stderr(io.StringIO()) as err:
                    self.assertEqual(_pa.main(), 1)
                self.assertIn("--repo must be owner/name", err.getvalue())


class TestWorkflowAnnotation(unittest.TestCase):
    def test_untrusted_text_cannot_start_a_new_workflow_command(self) -> None:
        row = {
            "status": "hard_fail",
            "layer": 4,
            "type": "content",
            "expected": "x\n::add-mask::secret",
            "actual": "50%\r\n::stop-commands::t",
        }

        out = _pa._workflow_annotation("tests/a,b:c.py", 7, row)

        self.assertEqual(out.count("\n"), 0)
        self.assertEqual(out.count("\r"), 0)
        self.assertIn("file=tests/a%2Cb%3Ac.py,line=7,", out)
        self.assertIn("x%0A::add-mask::secret", out)
        self.assertIn("50%25%0D%0A::stop-commands::t", out)

    def test_control_characters_are_stripped(self) -> None:
        row = {"status": "hard_fail", "expected": "a\x1b[31mred\u2028b"}

        out = _pa._workflow_annotation("tests/a.py", 1, row)

        self.assertNotIn("\x1b", out)
        self.assertNotIn("\u2028", out)


class TestFormatFailure(unittest.TestCase):
    def test_includes_every_diagnostic_field(self) -> None:
        out = _pa._format_failure(
            {
                "assertion_id": "a1",
                "status": "hard_fail",
                "score": 0.0,
                "layer": 1,
                "type": "schema",
                "trace_node_path": "output.message",
                "expected": "matches schema X",
                "actual": "missing required field",
                "explanation": "schema violation",
                "failure_class": "broken_code",
                "threshold_source": "static",
                "suggested_action": "regenerate fixture",
            }
        )
        self.assertIn("a1", out)
        self.assertIn("L1 `schema`", out)
        self.assertIn("output.message", out)
        self.assertIn("matches schema X", out)
        self.assertIn("missing required field", out)
        self.assertIn("broken_code", out)
        self.assertIn("regenerate fixture", out)
        # threshold_source=static should NOT show — only non-static.
        self.assertNotIn("Threshold source", out)


class TestSummaryBody(unittest.TestCase):
    def test_passes_summary_through(self) -> None:
        report = {
            "summary": {"total": 5, "passed": 3, "soft_fail": 1, "hard_fail": 1},
            "total_cost": 0.0123,
            "p50_duration_ms": 50,
            "p95_duration_ms": 200,
            "failure_classes": {"broken_code": 1, "stochastic_variance": 1},
        }
        out = _pa._summary_body(report, [{}, {}])
        self.assertIn("2 failing", out)
        self.assertIn("PASS 3 | SOFT 1 | HARD 1", out)
        self.assertIn("$0.012300", out)
        self.assertIn("P50 50ms", out)
        self.assertIn("broken_code", out)

    def test_simulated_banner(self) -> None:
        report = {
            "summary": {"total": 1, "passed": 1, "soft_fail": 0, "hard_fail": 0},
            "total_cost": 0.0,
            "simulated": True,
        }
        out = _pa._summary_body(report, [])
        self.assertIn("Simulated run", out)


if __name__ == "__main__":
    unittest.main()
