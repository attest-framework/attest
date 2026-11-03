"""Judge dogfood support: a scripted OpenAI-compatible judge server, the L6 scenarios
and the code that runs them through a real engine.

``record_judge_fixtures`` records the scenarios' judge calls through the capture proxy
into ``fixtures/judge/``; ``test_judge_replay`` replays them with no model call.
"""

from __future__ import annotations

import json
import os
import secrets
import time
from dataclasses import dataclass
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any

from attest import agent, expect
from attest.plugin import AttestEngineFixture
from attest.result import AgentResult

FIXTURE_DIR = Path(__file__).resolve().parent / "fixtures" / "judge"
SCRIPTED_MODEL = "scripted-judge"
RUBRIC = "Is the response helpful and accurate?"


@dataclass(frozen=True)
class JudgeScenario:
    """One agent output judged against ``RUBRIC``, and the verdict the scripted judge gives."""

    name: str
    output: str
    scripted_score: float
    passes: bool


SCENARIOS = (
    JudgeScenario(
        name="judge-helpful-pass",
        output="Your refund of $89.99 for order ORD-1 has been processed.",
        scripted_score=0.95,
        passes=True,
    ),
    JudgeScenario(
        name="judge-unhelpful-fail",
        output="I cannot help with that.",
        scripted_score=0.1,
        passes=False,
    ),
)


class ScriptedJudge(BaseHTTPRequestHandler):
    """An OpenAI Chat Completions endpoint that scores by the output it is shown."""

    def do_POST(self) -> None:  # noqa: N802 (http.server API)
        request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        prompt = "\n".join(str(m.get("content")) for m in request["messages"])
        scenario = next((s for s in SCENARIOS if s.output in prompt), None)
        if scenario is None:
            self.send_error(400, "no scripted verdict for this prompt")
            return
        verdict = {"score": scenario.scripted_score, "explanation": f"scripted: {scenario.name}"}
        body = json.dumps(
            {
                "id": f"chatcmpl-{secrets.token_hex(8)}",
                "object": "chat.completion",
                "created": time.time_ns() // 1_000_000_000,
                "model": request["model"],
                "choices": [
                    {
                        "index": 0,
                        "finish_reason": "stop",
                        "message": {"role": "assistant", "content": json.dumps(verdict)},
                    }
                ],
                "usage": {"prompt_tokens": 120, "completion_tokens": 20, "total_tokens": 140},
            }
        ).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, format: str, *args: object) -> None:  # noqa: A002
        return


def serve_scripted_judge() -> ThreadingHTTPServer:
    """Start the scripted judge on an ephemeral loopback port (daemon thread)."""
    import threading

    server = ThreadingHTTPServer(("127.0.0.1", 0), ScriptedJudge)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server


def judge_otlp() -> dict[str, Any]:
    """A minimal OTLP/JSON export (agent root plus one model span) for a judge capture; a
    fixture needs a trace, and the judge runs in the engine, not in an instrumented agent."""
    trace_id, root = secrets.token_hex(16), secrets.token_hex(8)
    start = time.time_ns()

    def span(span_id: str, parent: str | None, name: str, attrs: dict[str, str], i: int) -> Any:
        s: dict[str, Any] = {
            "traceId": trace_id,
            "spanId": span_id,
            "name": name,
            "startTimeUnixNano": str(start + i * 1_000_000),
            "endTimeUnixNano": str(start + (i + 1) * 1_000_000),
            "attributes": [{"key": k, "value": {"stringValue": v}} for k, v in attrs.items()],
        }
        if parent:
            s["parentSpanId"] = parent
        return s

    spans = [
        span(root, None, "invoke_agent judge", {"gen_ai.operation.name": "invoke_agent"}, 0),
        span(
            secrets.token_hex(8),
            root,
            "chat judge",
            {"gen_ai.operation.name": "chat", "gen_ai.request.model": SCRIPTED_MODEL},
            1,
        ),
    ]
    return {"resourceSpans": [{"scopeSpans": [{"spans": spans}]}]}


@agent("refund-agent")
def _judged_agent(builder: Any, output: str) -> dict[str, str]:
    builder.set_metadata(cost_usd=0.0, total_tokens=0, latency_ms=1)
    return {"message": output}


def run_judge(scenario: JudgeScenario, *, cache_dir: Path) -> AgentResult:
    """Evaluate ``passes_judge`` for the scenario on a fresh engine and return the result.

    The engine reads ``ATTEST_JUDGE_*`` at startup, so the caller sets them first; the
    cache directory is private to the run so no verdict is served from a cache.
    """
    os.environ["ATTEST_CACHE_DIR"] = str(cache_dir)
    engine = AttestEngineFixture(
        engine_path=os.environ.get("ATTEST_ENGINE_PATH"), log_level="warn", strict=False
    )
    engine.start()
    try:
        return engine.inspect(expect(_judged_agent(output=scenario.output)).passes_judge(RUBRIC))
    finally:
        engine.stop()
