"""trace-v2 (proto/attest/v1/trace-v2.schema.json): cross-SDK fixtures.

The schema accepts every protocol-tests/trace-v2/valid fixture and golden
converted trace and rejects every invalid fixture; the Go engine and the
TypeScript SDK run the same files. Each golden trace (the engine's
``trace convert`` output) and each valid fixture survives
``Trace.from_dict(...).to_dict()`` unchanged, so the SDK reproduces the
engine's Trace v2 JSON exactly.
"""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import jsonschema
import pytest

from attest._proto.types import Exchange, Handoff, StepError, Trace, Turn

_ROOT = Path(__file__).resolve().parents[3]
_FIXTURES = _ROOT / "protocol-tests" / "trace-v2"
_SCHEMA = json.loads((_ROOT / "proto" / "attest" / "v1" / "trace-v2.schema.json").read_text())

_VALID = sorted((_FIXTURES / "valid").glob("*.json"))
_INVALID = sorted((_FIXTURES / "invalid").glob("*.json"))
_GOLDEN = sorted((_FIXTURES / "otlp").glob("*.trace.json"))


def _load(path: Path) -> Any:
    return json.loads(path.read_text(encoding="utf-8"))


def test_trace_v2_fixtures_present() -> None:
    assert _VALID and _INVALID and _GOLDEN


@pytest.mark.parametrize("path", _VALID + _GOLDEN, ids=lambda p: p.name)
def test_trace_v2_valid_fixture_matches_schema(path: Path) -> None:
    jsonschema.validate(_load(path), _SCHEMA)


@pytest.mark.parametrize("path", _INVALID, ids=lambda p: p.name)
def test_trace_v2_invalid_fixture_is_rejected(path: Path) -> None:
    with pytest.raises(jsonschema.ValidationError):
        jsonschema.validate(_load(path), _SCHEMA)


@pytest.mark.parametrize("path", _GOLDEN, ids=lambda p: p.name)
def test_trace_from_dict_golden_round_trips_unchanged(path: Path) -> None:
    golden = _load(path)

    assert Trace.from_dict(golden).to_dict() == golden


def test_trace_from_dict_all_fields_fixture_round_trips_unchanged() -> None:
    fixture = _load(_FIXTURES / "valid" / "002-all-fields.json")
    # TraceMetadata carries no aggregate_* fields; they are engine-computed.
    for key in ("aggregate_tokens", "aggregate_cost_usd", "aggregate_latency_ms", "agent_count"):
        fixture["metadata"].pop(key)
    # parent_trace_id null and absent are the same value; to_dict omits it.
    fixture.pop("parent_trace_id")

    trace = Trace.from_dict(fixture)

    assert trace.to_dict() == fixture
    assert trace.turns == [
        Turn(
            index=0,
            step_indices=[0, 1, 2, 3],
            span_id="b000000000000001",
            input=[{"role": "user"}],
            output=[{"role": "assistant"}],
        )
    ]
    assert trace.handoffs == [Handoff(from_agent="support", to_agent="billing", step_index=3)]
    assert trace.steps[0].error == StepError(type="timeout", message="deadline exceeded")
    assert trace.steps[1].retry_of == 0
    assert isinstance(trace.steps[1].exchange, Exchange)


def test_trace_v1_to_dict_omits_v2_fields() -> None:
    trace = Trace(trace_id="trc", output={"message": "hi"})

    assert trace.to_dict() == {
        "schema_version": 1,
        "trace_id": "trc",
        "output": {"message": "hi"},
        "steps": [],
    }
