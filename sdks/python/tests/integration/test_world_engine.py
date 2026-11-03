"""World fixtures against the built engine: a scripted HTTP client drives the INC-1042
payments world, then effect assertions read the world's state."""

from __future__ import annotations

import json
import os
import socket
import textwrap
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any

import pytest

from attest import WorldError, expect, record, start_world
from attest._proto.types import (
    REASON_MISSING_EVIDENCE,
    STATUS_HARD_FAIL,
    STATUS_NOT_EVALUATED,
    STATUS_PASS,
    Trace,
)
from attest.capture import load_fixture
from attest.plugin import AttestEngineFixture
from tests.integration.test_capture_engine import (
    _scripted_agent,
    engine_env,  # noqa: F401  (fixture)
)
from tests.integration.test_replay_engine import (
    _V1,
    _run_agent,
    _run_pytest,
    captured,  # noqa: F401  (fixture)
    no_upstream,  # noqa: F401  (fixture)
)

_EFFECT_DSL: dict[str, Any] = json.loads(
    (
        Path(__file__).resolve().parents[4] / "protocol-tests" / "contracts" / "effect-dsl.json"
    ).read_text()
)
_WORLD = Path(__file__).parent / "data" / "payments.world.yaml"
_TRACE = (
    Path(__file__).resolve().parents[4]
    / "protocol-tests"
    / "trace-v2"
    / "otlp"
    / "inc-1042-refund.trace.json"
)
# The first refund is committed and its response held; the client gives up well before.
_CLIENT_TIMEOUT_S = 0.3
_ORDER_1042 = {"order_id": "O-1042", "amount_cents": 4999}


def _post_refund(base_url: str, key: str | None = None) -> int | None:
    """POST a refund the way the INC-1042 agent does. ``None``: the client timed out."""
    headers = {"Content-Type": "application/json"}
    if key is not None:
        headers["Idempotency-Key"] = key
    req = urllib.request.Request(
        base_url + "/refunds", data=json.dumps(_ORDER_1042).encode(), headers=headers
    )
    try:
        with urllib.request.urlopen(req, timeout=_CLIENT_TIMEOUT_S) as resp:
            resp.read()
            status: int = resp.status
            return status
    except TimeoutError:
        return None
    except urllib.error.URLError as e:
        if isinstance(e.reason, (TimeoutError, socket.timeout)):
            return None
        raise


def _double_refund(base_url: str, key: str | None = None) -> None:
    assert _post_refund(base_url, key) is None  # the held response times out
    assert _post_refund(base_url, key) in (200, 201)  # the retry is answered


def _check(
    engine: AttestEngineFixture, snapshot: dict[str, Any] | None, method: str, *args: Any, **kw: Any
) -> Any:
    chain = expect(Trace.from_dict(json.loads(_TRACE.read_text())))
    getattr(chain.world(snapshot), method)(*args, **kw)
    [row] = engine.inspect(chain).assertion_results
    return row


@pytest.mark.integration
def test_count_double_refund_without_key_fails_with_the_violation_text(
    engine: AttestEngineFixture,
    engine_env: str,  # noqa: F811
) -> None:
    with start_world(_WORLD) as world:
        _double_refund(world.base_url)
        row = _check(engine, world.state(), "count", "refunds", order_id="O-1042", equals=1)

    assert row.status == STATUS_HARD_FAIL
    assert row.explanation == "effect violated: 2 refunds for O-1042 (expected 1)"


@pytest.mark.integration
def test_count_same_idempotency_key_passes(
    engine: AttestEngineFixture,
    engine_env: str,  # noqa: F811
) -> None:
    with start_world(_WORLD) as world:
        _double_refund(world.base_url, key="refund-O-1042")
        row = _check(engine, world.state(), "count", "refunds", order_id="O-1042", equals=1)

    assert row.status == STATUS_PASS


@pytest.mark.integration
def test_count_bounds_at_least_and_at_most(
    engine: AttestEngineFixture,
    engine_env: str,  # noqa: F811
) -> None:
    with start_world(_WORLD) as world:
        _double_refund(world.base_url)
        final = world.stop()

    assert _check(engine, final, "count", "refunds", at_least=2).status == STATUS_PASS
    assert _check(engine, final, "count", "refunds", at_most=1).status == STATUS_HARD_FAIL


@pytest.mark.integration
def test_unique_passes_on_refund_id_and_fails_on_order_id(
    engine: AttestEngineFixture,
    engine_env: str,  # noqa: F811
) -> None:
    with start_world(_WORLD) as world:
        _double_refund(world.base_url)
        snapshot = world.state()

    assert _check(engine, snapshot, "unique", "refunds", by="refund_id").status == STATUS_PASS
    failed = _check(engine, snapshot, "unique", "refunds", by=["order_id"], order_id="O-1042")
    assert failed.status == STATUS_HARD_FAIL
    assert failed.explanation.startswith("effect violated:")


@pytest.mark.integration
def test_idempotent_passes_with_key_and_fails_without(
    engine: AttestEngineFixture,
    engine_env: str,  # noqa: F811
) -> None:
    with start_world(_WORLD) as keyed:
        _double_refund(keyed.base_url, key="refund-O-1042")
        keyed_state = keyed.state()
    with start_world(_WORLD) as keyless:
        _double_refund(keyless.base_url)
        keyless_state = keyless.state()

    assert _check(engine, keyed_state, "idempotent", "refunds").status == STATUS_PASS
    failed = _check(engine, keyless_state, "idempotent", "refunds", order_id="O-1042")
    assert failed.status == STATUS_HARD_FAIL
    assert failed.explanation.startswith("effect violated:")


@pytest.mark.integration
def test_no_snapshot_is_not_evaluated_missing_evidence(
    engine: AttestEngineFixture,
) -> None:
    row = _check(engine, None, "count", "refunds", equals=1)

    assert row.status == STATUS_NOT_EVALUATED
    assert row.reason == REASON_MISSING_EVIDENCE


@pytest.mark.integration
def test_session_env_state_and_stop(engine_env: str) -> None:  # noqa: F811
    assert "PAYMENTS_BASE_URL" not in os.environ

    with start_world(_WORLD) as world:
        assert os.environ["PAYMENTS_BASE_URL"] == world.base_url
        assert world.env == {"PAYMENTS_BASE_URL": world.base_url}
        assert world.state()["state"]["refunds"] == []
        assert _post_refund(world.base_url) is None
        good = world.env["PAYMENTS_BASE_URL"]
        world.env["PAYMENTS_BASE_URL"] = good + "/nope"
        with pytest.raises(WorldError, match="HTTP 404"):
            world.state()
        world.env["PAYMENTS_BASE_URL"] = good

    assert "PAYMENTS_BASE_URL" not in os.environ
    assert len(world.stop()["state"]["refunds"]) == 1
    with pytest.raises(WorldError, match="is stopped"):
        world.state()


@pytest.mark.integration
def test_second_world_while_one_runs_raises(engine_env: str) -> None:  # noqa: F811
    with start_world(_WORLD):
        with pytest.raises(WorldError, match="cannot overlap"):
            start_world(_WORLD)


@pytest.mark.integration
def test_malformed_world_file_raises_with_the_schema_error(
    engine_env: str,  # noqa: F811
    tmp_path: Path,  # noqa: F811
) -> None:
    bad = tmp_path / "bad.world.yaml"
    bad.write_text("world_version: 2\n")

    with pytest.raises(WorldError, match="did not start"):
        start_world(bad)

    with start_world(_WORLD):  # the failed start released the lock
        pass


@pytest.mark.integration
def test_world_composes_with_replay_in_one_block(
    captured: Path,  # noqa: F811
    no_upstream: None,  # noqa: F811
    engine_env: str,  # noqa: F811
) -> None:
    from attest import start_replay

    with start_world(_WORLD) as world:
        with start_replay("inc-9", mode="strict", fixtures_dir=captured):
            _run_agent(_V1)
            _double_refund(world.base_url)
            assert "PAYMENTS_BASE_URL" in os.environ
            assert os.environ["OPENAI_BASE_URL"] != "http://127.0.0.1:1/v1"
        snapshot = world.state()

    assert len(snapshot["state"]["refunds"]) == 2


_COMPOSE_TEST = textwrap.dedent(
    """
    import json, os, urllib.request

    def test_world_with_replay(attest_world, attest_replay):
        world = attest_world(os.environ["WORLD_FILE"])
        attest_replay("inc-9")
        body = {"model": "gpt-test", "messages": [
            {"role": "system", "content": os.environ["AGENT_SYSTEM"]},
            {"role": "user", "content": "refund O-9 for sam.customer@example.net"}]}
        req = urllib.request.Request(
            os.environ["OPENAI_BASE_URL"] + "/chat/completions",
            data=json.dumps(body).encode(),
            headers={"Content-Type": "application/json", "Authorization": "Bearer unused"})
        urllib.request.urlopen(req).read()
        refund = urllib.request.Request(
            os.environ["PAYMENTS_BASE_URL"] + "/refunds",
            data=json.dumps({"order_id": "O-1042", "amount_cents": 4999}).encode(),
            headers={"Content-Type": "application/json", "Idempotency-Key": "k"})
        try:
            urllib.request.urlopen(refund, timeout=0.3)
        except OSError:
            pass  # the first refund commits and its response is held
        assert urllib.request.urlopen(refund, timeout=5).status == 200
        assert len(world.state()["state"]["refunds"]) == 1

    def test_world_env_is_gone_after_the_test():
        assert "PAYMENTS_BASE_URL" not in os.environ
    """
)


@pytest.mark.integration
def test_pytest_attest_world_composes_with_attest_replay(
    captured: Path,  # noqa: F811
    tmp_path: Path,
    engine_env: str,  # noqa: F811
) -> None:
    run, _ = _run_pytest(
        captured,
        tmp_path,
        _V1,
        "--attest-replay=strict",
        source=_COMPOSE_TEST,
        WORLD_FILE=str(_WORLD),
    )

    assert run.returncode == 0, run.stdout + run.stderr
    assert "2 passed" in run.stdout


@pytest.mark.integration
def test_record_with_world_saves_the_world_in_the_fixture(
    engine_env: str,  # noqa: F811
    tmp_path: Path,  # noqa: F811
) -> None:
    out_dir = tmp_path / "fixtures"

    with record("inc-9", out_dir=out_dir, expect_fail=True, emit_test=False, world=_WORLD):
        _scripted_agent(refunds=2)
        _double_refund(os.environ["PAYMENTS_BASE_URL"])

    assert "PAYMENTS_BASE_URL" not in os.environ
    fixture = load_fixture(out_dir / "inc-9.attest.json")
    assert fixture.world is not None
    assert len(fixture.world["state"]["refunds"]) == 2


@pytest.mark.integration
@pytest.mark.parametrize("case", _EFFECT_DSL["cases"], ids=lambda c: c["name"])
def test_effect_dsl_fixture_results_match_the_engine(
    engine: AttestEngineFixture, case: dict[str, Any]
) -> None:
    snapshot = None if case["world"] is None else _EFFECT_DSL["worlds"][case["world"]]
    call = case["python"]

    row = _check(engine, snapshot, call["method"], *call["args"], **call["kwargs"])

    assert row.status == case["result"]["status"]
    if "explanation" in case["result"]:
        assert row.explanation == case["result"]["explanation"]
