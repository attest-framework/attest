"""Capture emitter: drafted contract specs → expect() DSL calls → a pytest module."""

from __future__ import annotations

import ast
import shutil
import sys
import types
from pathlib import Path

import pytest

from attest import expect
from attest.capture import (
    CaptureError,
    Contract,
    _flush_opentelemetry,
    dsl_call,
    emit_pytest,
    load_fixture,
    record,
    render_pytest,
)
from attest.capture import test_path_for as path_for_test
from attest.cli.admin import _capture_writes_fixture

_FIXTURES = Path(__file__).resolve().parents[3] / "protocol-tests" / "fixture-v1" / "valid"
_INCIDENT = _FIXTURES / "002-otlp-file-trace-only.attest.json"


def _contract(type_: str, spec: dict[str, object]) -> Contract:
    return Contract(id="c", kind="k", description="d", type=type_, spec=spec)


@pytest.mark.parametrize(
    ("type_", "spec", "call"),
    [
        (
            "trace",
            {"check": "trajectory", "mode": "superset", "tools": ["a", "b"], "soft": False},
            '.trajectory(["a", "b"], mode="superset")',
        ),
        ("trace", {"check": "trajectory", "mode": "strict", "tools": ["a"]}, '.trajectory(["a"])'),
        (
            "trace",
            {"check": "tool_order", "before": "a", "after": "b", "soft": True},
            '.tool_called_before("a", "b", soft=True)',
        ),
        (
            "trace",
            {"check": "tool_cardinality", "tool": "t", "per": "order_id", "max": 1, "soft": False},
            '.tool_cardinality("t", per="order_id", at_most=1)',
        ),
        (
            "trace",
            {"check": "tool_cardinality", "tool": "t", "min": 1, "max": 2},
            '.tool_cardinality("t", at_least=1, at_most=2)',
        ),
        (
            "schema",
            {"target": "output.structured", "schema": {"type": "object", "required": ["a"]}},
            '.output_matches_schema({"type": "object", "required": ["a"]})',
        ),
        (
            "constraint",
            {"field": "metadata.cost_usd", "operator": "lte", "value": 0.0185, "soft": False},
            ".cost_under(0.0185)",
        ),
    ],
)
def test_dsl_call_drafted_spec_renders_matching_dsl_call(
    type_: str, spec: dict[str, object], call: str
) -> None:
    assert dsl_call(_contract(type_, spec)) == call


@pytest.mark.parametrize(
    ("type_", "spec"),
    [
        ("trace", {"check": "trajectory", "mode": "partial_order", "order": [["a", "b"]]}),
        ("constraint", {"field": "metadata.latency_ms", "operator": "lte", "value": 1}),
        ("llm_judge", {}),
        ("trace", {"check": "tool_order", "before": "a"}),
    ],
)
def test_dsl_call_unsupported_or_malformed_spec_raises_value_error(
    type_: str, spec: dict[str, object]
) -> None:
    with pytest.raises(ValueError, match="contract c"):
        dsl_call(_contract(type_, spec))


def test_dsl_call_rendered_call_emits_the_same_spec() -> None:
    fixture = load_fixture(_INCIDENT)
    chain = expect(fixture.trace)

    for contract in fixture.contracts:
        chain = eval("chain" + dsl_call(contract), {"chain": chain})  # noqa: S307

    assert [a.spec for a in chain.assertions] == [c.spec for c in fixture.contracts]
    assert [a.type for a in chain.assertions] == [c.type for c in fixture.contracts]


def test_render_pytest_incident_asserts_expected_statuses(tmp_path: Path) -> None:
    fixture = load_fixture(_INCIDENT)
    fixture_path = tmp_path / "fixtures" / "inc-1042.attest.json"

    source = render_pytest(fixture, fixture_path, tmp_path / "test_inc_1042.py")

    ast.parse(source)
    assert 'load_fixture(Path(__file__).parent / "fixtures/inc-1042.attest.json")' in source
    assert '.tool_cardinality("issue_refund", per="order_id", at_most=1)' in source
    assert '("cardinality-issue_refund-per-order_id", "hard_fail"),' in source
    assert "def test_inc_1042_reproduces_incident(attest: AttestEngineFixture)" in source
    assert "attest.inspect(contracts())" in source


def test_render_pytest_verified_fixture_enforces_contracts(tmp_path: Path) -> None:
    fixture = load_fixture(_INCIDENT)
    verified = type(fixture)(**{**fixture.__dict__, "expect_fail": False, "expected_failures": []})

    source = render_pytest(verified, tmp_path / "f.attest.json", tmp_path / "test_x.py")

    assert "def test_inc_1042_contracts_hold(attest: AttestEngineFixture) -> None:" in source
    assert "attest.evaluate(contracts())" in source
    assert "EXPECTED" not in source


def test_emit_pytest_existing_test_left_unchanged(tmp_path: Path) -> None:
    fixture_path = tmp_path / "tests" / "attest" / "fixtures" / "inc-1042.attest.json"
    fixture_path.parent.mkdir(parents=True)
    shutil.copy(_INCIDENT, fixture_path)

    first = emit_pytest(fixture_path)
    assert first == tmp_path / "tests" / "attest" / "test_inc_1042.py"
    first.write_text("# edited\n")
    second = emit_pytest(fixture_path)

    assert second is None
    assert first.read_text() == "# edited\n"


@pytest.mark.parametrize(
    ("name", "test_name"),
    [("inc-1042", "test_inc_1042.py"), ("1st.run", "test_fixture_1st_run.py")],
)
def test_test_path_for_names_test_after_fixture(name: str, test_name: str) -> None:
    path = Path("tests/attest/fixtures") / f"{name}.attest.json"

    assert path_for_test(path) == Path("tests/attest") / test_name


@pytest.mark.parametrize(
    ("args", "writes"),
    [
        (["--name", "x", "--", "make", "--help"], True),
        (["--name", "x", "--from", "a.json"], True),
        (["--check-redaction", "f.json"], False),
        (["--check-redaction=f.json"], False),
        (["--name", "x", "--serve"], False),
        (["--help"], False),
    ],
)
def test_capture_writes_fixture_by_mode(args: list[str], writes: bool) -> None:
    assert _capture_writes_fixture(args) is writes


class _Provider:
    def __init__(self, flushed: bool) -> None:
        self.flushed = flushed
        self.calls = 0

    def force_flush(self) -> bool:
        self.calls += 1
        return self.flushed


def _otel_trace_module(provider: object) -> types.ModuleType:
    module = types.ModuleType("opentelemetry.trace")
    module.get_tracer_provider = lambda: provider  # type: ignore[attr-defined]
    return module


def test_flush_opentelemetry_loaded_provider_is_flushed(monkeypatch: pytest.MonkeyPatch) -> None:
    provider = _Provider(flushed=True)
    monkeypatch.setitem(sys.modules, "opentelemetry.trace", _otel_trace_module(provider))

    _flush_opentelemetry()

    assert provider.calls == 1


def test_flush_opentelemetry_flush_fails_raises_capture_error(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setitem(sys.modules, "opentelemetry.trace", _otel_trace_module(_Provider(False)))

    with pytest.raises(CaptureError, match="failed to flush"):
        _flush_opentelemetry()


def test_flush_opentelemetry_not_loaded_is_a_no_op(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delitem(sys.modules, "opentelemetry.trace", raising=False)

    _flush_opentelemetry()


def test_record_engine_fails_to_start_raises_capture_error(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    engine = tmp_path / "attest-engine"
    engine.write_text("#!/bin/sh\necho 'capture: listener refused' >&2\nexit 1\n")
    engine.chmod(0o755)
    monkeypatch.setattr("attest.engine_manager.resolve_engine", lambda path: str(path))

    with pytest.raises(CaptureError, match="did not start: capture: listener refused"):
        with record("x", engine_path=str(engine)):
            pass
