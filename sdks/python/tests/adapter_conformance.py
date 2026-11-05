"""Behavioral conformance for adapters: run the shared corpus through the Python adapters.

The corpus (``sdks/parity/adapters/<adapter>/<case>.json``, written by
``generate.py`` from the official SDK types) holds a provider response or framework
run record and the golden canonical trace. A case passes when the adapter's trace
equals the golden exactly, except ``trace_id`` (random per trace, replaced by
``trc_normalized``). An adapter's parity row is ``match`` only if it has corpus
cases and all of them pass.
"""

from __future__ import annotations

import importlib
import json
from collections.abc import Callable
from pathlib import Path
from typing import Any
from unittest.mock import patch
from uuid import UUID

from attest._proto.types import Trace
from attest.adapters._base import BaseAdapter
from attest.adapters.anthropic import AnthropicAdapter
from attest.adapters.crewai import CrewAIAdapter
from attest.adapters.gemini import GeminiAdapter
from attest.adapters.langchain import LangChainCallbackHandler
from attest.adapters.manual import ManualAdapter
from attest.adapters.ollama import OllamaAdapter
from attest.adapters.openai import OpenAIAdapter
from attest.adapters.otel import OTelAdapter

REPO = Path(__file__).resolve().parents[3]
CORPUS = REPO / "sdks" / "parity" / "adapters"
OTLP_GOLDEN = REPO / "protocol-tests" / "trace-v2" / "otlp"
NORMALIZED_TRACE_ID = "trc_normalized"

# Parity-matrix adapter names each corpus directory vouches for.
MATRIX_NAMES: dict[str, tuple[str, ...]] = {
    "anthropic": ("AnthropicAdapter",),
    "crewai": ("CrewAIAdapter",),
    "gemini": ("GeminiAdapter",),
    "langchain": ("LangChainAdapter", "LangChainCallbackHandler"),
    "manual": ("ManualAdapter",),
    "ollama": ("OllamaAdapter",),
    "openai": ("OpenAIAdapter",),
    # OTLP -> trace conversion is the engine's; its goldens are protocol-tests/trace-v2/otlp.
    "otel": ("OTelAdapter",),
}
_PROVIDERS: dict[str, type[BaseAdapter]] = {
    "anthropic": AnthropicAdapter,
    "gemini": GeminiAdapter,
    "ollama": OllamaAdapter,
    "openai": OpenAIAdapter,
}

Case = dict[str, Any]


def load_cases() -> list[Case]:
    """Every corpus case, ordered by adapter then case name."""
    return [json.loads(p.read_text()) for p in sorted(CORPUS.glob("*/*.json"))]


def case_id(case: Case) -> str:
    return f"{case['adapter']}/{case['case']}"


def otlp_cases() -> list[Path]:
    """The engine's OTLP goldens, the OTel adapter's corpus."""
    return sorted(OTLP_GOLDEN.glob("*.otlp.json"))


def canonical(trace: Trace) -> dict[str, Any]:
    """The comparable form of a trace: ``to_dict()`` with the random id replaced."""
    data: dict[str, Any] = json.loads(json.dumps(trace.to_dict()))
    data["trace_id"] = NORMALIZED_TRACE_ID
    return data


def diff(expected: Any, actual: Any, path: str = "trace") -> list[str]:
    """Differences between two JSON values, one line per differing path."""
    if isinstance(expected, dict) and isinstance(actual, dict):
        out: list[str] = []
        for key in sorted(set(expected) | set(actual)):
            if key not in actual:
                out.append(f"{path}.{key}: missing, expected {expected[key]!r}")
            elif key not in expected:
                out.append(f"{path}.{key}: unexpected {actual[key]!r}")
            else:
                out += diff(expected[key], actual[key], f"{path}.{key}")
        return out
    if isinstance(expected, list) and isinstance(actual, list):
        out = []
        for i in range(max(len(expected), len(actual))):
            if i >= len(actual):
                out.append(f"{path}[{i}]: missing, expected {expected[i]!r}")
            elif i >= len(expected):
                out.append(f"{path}[{i}]: unexpected {actual[i]!r}")
            else:
                out += diff(expected[i], actual[i], f"{path}[{i}]")
        return out
    if expected != actual or type(expected) is not type(actual):
        return [f"{path}: expected {expected!r}, got {actual!r}"]
    return []


def _sdk_object(case: Case, payload: Any) -> Any:
    module, _, name = case["sdk"]["type"].rpartition(".")
    return getattr(importlib.import_module(module), name).model_validate(payload)


def _decode(value: Any) -> Any:
    from langchain_core.messages import messages_from_dict
    from langchain_core.outputs import ChatGeneration, LLMResult
    from langchain_core.tools import ToolException

    if isinstance(value, dict):
        if "$message" in value:
            return messages_from_dict([value["$message"]])[0]
        if "$llm_result" in value:
            raw = value["$llm_result"]
            return LLMResult(
                generations=[
                    [
                        ChatGeneration(message=_decode({"$message": g["message"]}), text=g["text"])
                        for g in row
                    ]
                    for row in raw["generations"]
                ],
                llm_output=raw["llm_output"],
            )
        if "$error" in value:
            return ToolException(value["$error"]["message"])
        return {k: _decode(v) for k, v in value.items()}
    if isinstance(value, list):
        return [_decode(v) for v in value]
    return value


def _replay_langchain(case: Case) -> Trace:
    """Replay recorded callbacks on the handler with its clock fixed at each event's ``t_ms``."""
    clock = {"ms": 0}
    handler = LangChainCallbackHandler(**case["construct"])
    run_ids: dict[str, UUID] = {}

    def run_uuid(name: str | None) -> UUID | None:
        if name is None:
            return None
        return run_ids.setdefault(name, UUID(int=len(run_ids) + 1, version=4))

    with patch.object(BaseAdapter, "_now_ms", lambda self: clock["ms"]):
        for event in case["payload"]["events"]:
            clock["ms"] = event["t_ms"]
            args = _decode(event["args"])
            run_id = run_uuid(event["run_id"])
            parent = run_uuid(event["parent_run_id"])
            method = getattr(handler, event["method"])
            if event["method"] == "on_tool_start":
                method(
                    args.get("serialized", {}),
                    args["input_str"],
                    run_id=run_id,
                    parent_run_id=parent,
                    inputs=args["inputs"],
                )
            elif event["method"] == "on_chat_model_start":
                method(
                    {},
                    args["messages"],
                    run_id=run_id,
                    parent_run_id=parent,
                    invocation_params=args["invocation_params"],
                )
            elif event["method"] == "on_chain_start":
                method({}, args["inputs"], run_id=run_id, parent_run_id=parent)
            else:
                (value,) = args.values()
                method(value, run_id=run_id, parent_run_id=parent)
    return handler.build_trace()


def _run_crewai(case: Case) -> Trace:
    from crewai import Agent, Crew, Task
    from crewai.crews.crew_output import CrewOutput

    payload = case["payload"]
    output = CrewOutput.model_validate(payload["crew_output"])
    crew = Crew.model_construct(
        agents=[Agent.model_construct(**a) for a in payload["crew"]["agents"]],
        tasks=[Task.model_construct(**t) for t in payload["crew"]["tasks"]],
    )
    return CrewAIAdapter(**case["construct"]).trace_from_crew_output(output, crew)


def _run_manual(case: Case) -> Trace:
    ops: list[dict[str, Any]] = case["payload"]["ops"]

    def build(builder: Any) -> None:
        for op in ops:
            getattr(builder, op["op"])(**op["args"])

    return ManualAdapter(**case["construct"]).capture(build)


def produce(case: Case) -> Trace:
    """Feed one corpus case through the Python adapter."""
    kind = case["call"]["kind"]
    if kind == "trace_from_response":
        call = case["call"]
        adapter = _PROVIDERS[case["adapter"]](**case["construct"])
        return adapter.trace_from_response(  # type: ignore[attr-defined, no-any-return]
            _sdk_object(case, case["payload"]),
            input_messages=call["input_messages"],
            started_at_ms=call["started_at_ms"],
            ended_at_ms=call["ended_at_ms"],
        )
    runners: dict[str, Callable[[Case], Trace]] = {
        "callbacks": _replay_langchain,
        "crew_output": _run_crewai,
        "builder_ops": _run_manual,
    }
    return runners[kind](case)


def case_diffs(case: Case) -> list[str]:
    """Differences between the adapter's trace and the case's golden; empty on a pass."""
    return diff(case["golden"], canonical(produce(case)))


def otlp_diffs(path: Path) -> list[str]:
    """Differences between ``OTelAdapter.from_otlp`` and the engine's golden for ``path``."""
    golden = json.loads(path.with_name(path.name.replace(".otlp.", ".trace.")).read_text())
    return diff(golden, OTelAdapter.from_otlp(path).to_dict())


def matrix_adapters_with_cases() -> set[str]:
    """Parity-matrix adapter names that have at least one corpus case."""
    corpus_dirs = {c["adapter"] for c in load_cases()}
    if otlp_cases():
        corpus_dirs.add("otel")
    return {name for d in corpus_dirs for name in MATRIX_NAMES.get(d, ())}
