"""OTel GenAI semantic-convention conformance suite.

Each vector in ``otel_conformance_vectors.json`` declares a span fixture
plus the Attest step it must produce. The suite covers two passes:

1. Ingest — feed the span dict through ``OTelAdapter.from_otel_span_dicts``
   and assert the produced step matches the vector's ``expected`` block.
2. Round-trip — re-export the produced trace via ``to_otel_spans`` and
   re-ingest; assert step type / agent identity / operation name survive.

When the OTel GenAI conventions repository introduces a new operation,
add a vector here. Drift in the adapter is then visible as a fixture
diff plus a failing test, not a silent classification regression.
"""

from __future__ import annotations

import json
from contextlib import contextmanager
from pathlib import Path
from typing import Any
from unittest.mock import patch

import pytest

from attest.adapters.otel import OTelAdapter, to_otel_spans

_VECTORS_PATH = Path(__file__).parent / "otel_conformance_vectors.json"


@contextmanager
def _otel_available() -> Any:
    with patch("attest.adapters.otel._require_otel"):
        yield


def _load_vectors() -> list[dict[str, Any]]:
    data = json.loads(_VECTORS_PATH.read_text(encoding="utf-8"))
    vectors: list[dict[str, Any]] = data["vectors"]
    return vectors


def _vector_id(vector: dict[str, Any]) -> str:
    return str(vector["id"])


def _build_span_dict(span_attrs: dict[str, Any], name: str) -> dict[str, Any]:
    """Wrap raw span data in the OTLP dict shape consumed by from_otel_span_dicts."""
    return {
        "name": name,
        "trace_id": "deadbeefdeadbeefdeadbeefdeadbeef",
        "span_id": "0000000000000001",
        "parent_span_id": None,
        "kind": "INTERNAL",
        "start_time_unix_nano": 0,
        "end_time_unix_nano": 1_000_000,
        "attributes": span_attrs,
    }


@pytest.mark.parametrize("vector", _load_vectors(), ids=_vector_id)
class TestOtelConformance:
    """Vector-driven assertions for ingest and round-trip behavior."""

    def test_ingest_produces_expected_step(self, vector: dict[str, Any]) -> None:
        span_data = vector["span"]
        span_dict = _build_span_dict(span_data["attributes"], span_data["name"])
        expected = vector["expected"]

        with _otel_available():
            trace = OTelAdapter.from_otel_span_dicts([span_dict])

        assert len(trace.steps) == 1, (
            f"vector {vector['id']!r}: expected exactly one step, got {len(trace.steps)}"
        )
        step = trace.steps[0]

        assert step.type == expected["step_type"], f"vector {vector['id']!r}: step_type mismatch"

        if "step_name" in expected:
            assert step.name == expected["step_name"]

        if "step_agent_id" in expected:
            assert step.agent_id == expected["step_agent_id"]
        if "step_agent_role" in expected:
            assert step.agent_role == expected["step_agent_role"]

        for key, value in expected.get("args_includes", {}).items():
            assert step.args is not None, f"vector {vector['id']!r}: args missing"
            assert step.args.get(key) == value, f"vector {vector['id']!r}: args[{key!r}] mismatch"

        for key, value in expected.get("result_includes", {}).items():
            assert step.result is not None, f"vector {vector['id']!r}: result missing"
            assert step.result.get(key) == value, (
                f"vector {vector['id']!r}: result[{key!r}] mismatch"
            )

    def test_round_trip_preserves_step_type_and_agent_identity(
        self, vector: dict[str, Any]
    ) -> None:
        span_data = vector["span"]
        span_dict = _build_span_dict(span_data["attributes"], span_data["name"])
        expected = vector["expected"]

        with _otel_available():
            ingested = OTelAdapter.from_otel_span_dicts([span_dict])
            spans = to_otel_spans(ingested)
            roundtripped = OTelAdapter.from_otel_span_dicts(spans)

        assert len(roundtripped.steps) == 1
        original = ingested.steps[0]
        rt = roundtripped.steps[0]

        assert rt.type == original.type, f"vector {vector['id']!r}: round-trip changed step type"

        if "step_agent_id" in expected:
            assert rt.agent_id == expected["step_agent_id"], (
                f"vector {vector['id']!r}: agent_id lost in round-trip"
            )
        if "step_agent_role" in expected:
            assert rt.agent_role == expected["step_agent_role"], (
                f"vector {vector['id']!r}: agent_role lost in round-trip"
            )

        # Every export must carry the canonical operation attribute,
        # so re-ingesting picks the same dispatch path on every cycle.
        attrs = spans[0]["attributes"]
        assert "gen_ai.operation.name" in attrs


def test_vectors_file_loadable() -> None:
    vectors = _load_vectors()
    assert vectors, "conformance vectors file must contain at least one entry"
    seen_ids: set[str] = set()
    for v in vectors:
        vid = _vector_id(v)
        assert vid not in seen_ids, f"duplicate vector id {vid!r}"
        seen_ids.add(vid)
