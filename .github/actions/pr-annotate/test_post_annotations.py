"""Unit tests for the pure functions in post_annotations.py.

The script is stdlib-only so the tests run in the same Ubuntu runner
without `pip install` — invoke them via ``python3 -m unittest`` from
the action directory.
"""

from __future__ import annotations

import importlib.util
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


class TestResolveTestFile(unittest.TestCase):
    def test_pytest_id_in_explanation_yields_path(self) -> None:
        result = {
            "explanation": "ok at tests/test_x.py::TestSuite::test_case before fail",
        }
        path, line = _pa._resolve_test_file(result)
        self.assertEqual(path, "tests/test_x.py")
        self.assertEqual(line, 1)

    def test_no_source_hint_returns_none(self) -> None:
        result = {"explanation": "constraint not satisfied"}
        path, line = _pa._resolve_test_file(result)
        self.assertIsNone(path)
        self.assertIsNone(line)

    def test_extension_must_match_supported_set(self) -> None:
        # Random tokens with `::` but unsupported extension are ignored.
        result = {"explanation": "ns::Class::method failed"}
        path, line = _pa._resolve_test_file(result)
        self.assertIsNone(path)
        self.assertIsNone(line)


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
        self.assertIn("L1 schema", out)
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
