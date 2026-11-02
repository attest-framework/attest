"""attest.record() and `attest capture` against the engine built by `make engine`.

A scripted agent calls a fake OpenAI-compatible upstream through the capture proxy and
exports the INC-1042 shape (one lookup, two refunds of the same order) to the capture's
OTLP receiver. Ids and timestamps change on every run, as in a real run.
"""

from __future__ import annotations

import json
import os
import secrets
import subprocess
import sys
import threading
import time
import urllib.request
from collections.abc import Iterator
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import pytest

from attest import CaptureError, load_fixture, record
from tests.integration.conftest import _engine_binary_path

_PLANTED_KEY = "sk-test-pyplanted0123456789abcdef"
_PLANTED_EMAIL = "sam.customer@example.net"
_INCIDENT_OTLP = (
    Path(__file__).resolve().parents[4]
    / "protocol-tests"
    / "trace-v2"
    / "otlp"
    / "inc-1042-refund.otlp.json"
)


class _FakeModel(BaseHTTPRequestHandler):
    def do_POST(self) -> None:  # noqa: N802 (http.server API)
        self.rfile.read(int(self.headers["Content-Length"]))
        if self.headers.get("Authorization") != f"Bearer {_PLANTED_KEY}":
            self.send_error(401)
            return
        body = json.dumps(
            {
                "id": f"chatcmpl-{secrets.token_hex(8)}",
                "object": "chat.completion",
                "created": time.time_ns(),
                "model": "gpt-test",
                "choices": [
                    {
                        "index": 0,
                        "finish_reason": "stop",
                        "message": {"role": "assistant", "content": "ok"},
                    }
                ],
            }
        ).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, format: str, *args: object) -> None:  # noqa: A002
        return


@pytest.fixture
def engine_env(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> Iterator[str]:
    """Point model calls at a fake upstream and resolve the built engine."""
    engine = _engine_binary_path()
    if not os.path.exists(engine):
        pytest.skip("attest-engine binary not found; build with 'make engine'")
    server = ThreadingHTTPServer(("127.0.0.1", 0), _FakeModel)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    monkeypatch.setenv("OPENAI_BASE_URL", f"http://127.0.0.1:{server.server_address[1]}/v1")
    monkeypatch.setenv("OPENAI_API_KEY", _PLANTED_KEY)
    monkeypatch.setenv("ATTEST_ENGINE_PATH", engine)
    monkeypatch.setenv("ATTEST_ENGINE_NO_DOWNLOAD", "1")
    monkeypatch.setenv("ATTEST_CACHE_DIR", str(tmp_path / "cache"))
    yield engine
    server.shutdown()


def _post(url: str, body: dict[str, object], headers: dict[str, str]) -> None:
    req = urllib.request.Request(
        url, data=json.dumps(body).encode(), headers={"Content-Type": "application/json", **headers}
    )
    with urllib.request.urlopen(req) as resp:
        resp.read()


def _scripted_agent(refunds: int) -> None:
    """One model call and an INC-1042-shaped trace, configured from the environment."""
    _post(
        os.environ["OPENAI_BASE_URL"] + "/chat/completions",
        {
            "model": "gpt-test",
            "messages": [{"role": "user", "content": f"refund O-9 for {_PLANTED_EMAIL}"}],
        },
        {"Authorization": f"Bearer {os.environ['OPENAI_API_KEY']}"},
    )
    _post(os.environ["OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"], _incident_otlp(refunds), {})


def _incident_otlp(refunds: int) -> dict[str, object]:
    """An OTLP/JSON export of the INC-1042 shape with fresh ids and timestamps: one
    lookup and ``refunds`` refunds of order O-9."""
    trace_id, root = secrets.token_hex(16), secrets.token_hex(8)
    start = time.time_ns()

    def span(
        span_id: str, parent: str | None, name: str, i: int, attrs: dict[str, str], length: int = 1
    ) -> dict[str, object]:
        s: dict[str, object] = {
            "traceId": trace_id,
            "spanId": span_id,
            "name": name,
            "startTimeUnixNano": str(start + i * 1_000_000),
            "endTimeUnixNano": str(start + (i + length) * 1_000_000),
            "attributes": [{"key": k, "value": {"stringValue": v}} for k, v in attrs.items()],
        }
        if parent:
            s["parentSpanId"] = parent
        return s

    def tool(name: str, args: str, i: int) -> dict[str, object]:
        return span(
            secrets.token_hex(8),
            root,
            f"execute_tool {name}",
            i,
            {
                "gen_ai.operation.name": "execute_tool",
                "gen_ai.tool.name": name,
                "gen_ai.tool.call.id": f"call_{secrets.token_hex(6)}",
                "gen_ai.tool.call.arguments": args,
                "gen_ai.tool.call.result": '{"ok":true}',
            },
        )

    spans = [
        span(
            root,
            None,
            "invoke_agent refund-desk",
            0,
            {"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": "refund-desk"},
            length=20,
        ),
        span(
            secrets.token_hex(8),
            root,
            "chat gpt-test",
            1,
            {"gen_ai.operation.name": "chat", "gen_ai.request.model": "gpt-test"},
        ),
        tool("lookup_order", '{"order_id":"O-9"}', 2),
    ]
    spans += [
        tool("issue_refund", f'{{"order_id":"O-9","to":"{_PLANTED_EMAIL}"}}', 4 + 2 * i)
        for i in range(refunds)
    ]
    return {"resourceSpans": [{"scopeSpans": [{"spans": spans}]}]}


@pytest.mark.integration
def test_record_incident_twice_byte_identical_redacted_and_test_reproduces(
    engine_env: str, tmp_path: Path
) -> None:
    upstream = os.environ["OPENAI_BASE_URL"]
    paths = []
    for run in ("a", "b"):
        out_dir = tmp_path / run / "tests" / "attest" / "fixtures"
        with record("inc-9", out_dir=out_dir, expect_fail=True) as cap:
            assert os.environ["OPENAI_BASE_URL"] == cap.env["OPENAI_BASE_URL"] != upstream
            _scripted_agent(refunds=2)
        assert os.environ["OPENAI_BASE_URL"] == upstream
        assert cap.result.verified is True
        assert cap.result.test_file == tmp_path / run / "tests" / "attest" / "test_inc_9.py"
        paths.append(out_dir / "inc-9.attest.json")

    first, second = (p.read_bytes() for p in paths)
    assert first == second
    assert _PLANTED_KEY.encode() not in first
    assert _PLANTED_EMAIL.encode() not in first
    fixture = load_fixture(paths[0])
    assert fixture.source["kind"] == "in_process"
    assert fixture.expected_failures == ["cardinality-issue_refund-per-order_id"]
    assert [e["api"] for e in fixture.exchanges] == ["chat_completions"]

    run = subprocess.run(
        [
            sys.executable,
            "-m",
            "pytest",
            "-q",
            "-p",
            "no:cacheprovider",
            str(tmp_path / "a" / "tests"),
        ],
        capture_output=True,
        text=True,
        check=False,
        cwd=tmp_path / "a",
    )
    assert run.returncode == 0, run.stdout + run.stderr
    assert "1 passed" in run.stdout


@pytest.mark.integration
def test_record_incident_without_expect_fail_drafts_observed_max_and_verifies(
    engine_env: str, tmp_path: Path
) -> None:
    out_dir = tmp_path / "fixtures"

    with record("inc-9", out_dir=out_dir) as cap:
        _scripted_agent(refunds=2)

    fixture = load_fixture(out_dir / "inc-9.attest.json")
    [refund] = [c for c in fixture.contracts if c.id == "cardinality-issue_refund-per-order_id"]
    assert cap.result.verified is True
    assert refund.spec["max"] == 2


@pytest.mark.integration
def test_record_expect_fail_without_incident_raises_and_drafts_no_test(
    engine_env: str, tmp_path: Path
) -> None:
    out_dir = tmp_path / "fixtures"

    with pytest.raises(CaptureError, match="is not verified; no test drafted"):
        with record("inc-9", out_dir=out_dir, expect_fail=True):
            _scripted_agent(refunds=1)

    assert load_fixture(out_dir / "inc-9.attest.json").verified is False
    assert not (tmp_path / "test_inc_9.py").exists()


@pytest.mark.integration
def test_record_block_raises_abandons_capture_and_restores_env(
    engine_env: str, tmp_path: Path
) -> None:
    upstream = os.environ["OPENAI_BASE_URL"]

    with pytest.raises(RuntimeError, match="agent crashed"):
        with record("broken", out_dir=tmp_path):
            raise RuntimeError("agent crashed")

    assert os.environ["OPENAI_BASE_URL"] == upstream
    assert not (tmp_path / "broken.attest.json").exists()


@pytest.mark.integration
def test_attest_capture_cli_from_otlp_drafts_test_that_reproduces(
    engine_env: str, tmp_path: Path
) -> None:
    def capture() -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            [
                sys.executable,
                "-m",
                "attest",
                "capture",
                "--name",
                "inc-1042",
                "--expect-fail",
                "--from",
                str(_INCIDENT_OTLP),
            ],
            capture_output=True,
            text=True,
            check=False,
            cwd=tmp_path,
        )

    first = capture()
    second = capture()

    assert first.returncode == 0, first.stderr
    assert "test: drafted tests/attest/test_inc_1042.py" in first.stderr
    assert "left unchanged" in second.stderr
    run = subprocess.run(
        [sys.executable, "-m", "pytest", "-q", "-p", "no:cacheprovider", "tests"],
        capture_output=True,
        text=True,
        check=False,
        cwd=tmp_path,
    )
    assert run.returncode == 0, run.stdout + run.stderr


@pytest.mark.integration
def test_attest_capture_cli_unverified_drafts_no_test(engine_env: str, tmp_path: Path) -> None:
    export = tmp_path / "healthy.otlp.json"
    export.write_text(json.dumps(_incident_otlp(refunds=1)))

    proc = subprocess.run(
        [sys.executable, "-m", "attest", "capture", "--name", "healthy", "--expect-fail"]
        + ["--from", str(export)],
        capture_output=True,
        text=True,
        check=False,
        cwd=tmp_path,
    )

    assert proc.returncode == 1
    assert "test: none drafted for unverified" in proc.stderr
    assert not (tmp_path / "tests" / "attest" / "test_healthy.py").exists()
