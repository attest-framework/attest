"""Every adapter corpus case produces its golden trace; a planted divergence is caught."""

from __future__ import annotations

import copy
import importlib.util
import json
from importlib import metadata
from pathlib import Path
from typing import Any
from unittest.mock import patch

import pytest

from attest.adapters.openai import OpenAIAdapter
from tests.adapter_conformance import (
    CORPUS,
    MATRIX_NAMES,
    canonical,
    case_diffs,
    case_id,
    diff,
    load_cases,
    otlp_cases,
    otlp_diffs,
    produce,
)

CASES = load_cases()
ENGINE = Path(__file__).resolve().parents[3] / "bin" / "attest-engine"


def _framework_missing(case: dict[str, object]) -> str | None:
    package = {"crewai": "crewai"}.get(str(case["adapter"]))
    if package and importlib.util.find_spec(package) is None:
        return f"{package} is not installed (installed by the framework CI job, extra 'crewai')"
    return None


@pytest.mark.parametrize("case", CASES, ids=case_id)
def test_adapter_trace_matches_golden(case: dict[str, object]) -> None:
    reason = _framework_missing(case)
    if reason:
        pytest.skip(reason)

    assert case_diffs(case) == []


@pytest.mark.parametrize("path", otlp_cases(), ids=lambda p: p.name)
def test_otel_trace_matches_engine_golden(path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    if not ENGINE.exists():
        pytest.skip("attest-engine binary not found; build with 'make engine'")
    monkeypatch.setenv("ATTEST_ENGINE_PATH", str(ENGINE))

    assert otlp_diffs(path) == []


def test_checker_reports_planted_golden_divergence() -> None:
    case = copy.deepcopy(next(c for c in CASES if case_id(c) == "openai/chat_tool_call"))
    case["golden"]["steps"][1]["args"]["order_id"] = "ORD-2"

    assert case_diffs(case) == [
        "trace.steps[1].args.order_id: expected 'ORD-2', got 'ORD-1'",
    ]


def test_checker_reports_planted_adapter_divergence() -> None:
    case = next(c for c in CASES if case_id(c) == "openai/chat_tool_call")

    with patch.object(OpenAIAdapter, "_extract_tool_calls", lambda self, response: []):
        problems = case_diffs(case)

    assert problems == ["trace.steps[1]: missing, expected " + repr(case["golden"]["steps"][1])]


def test_checker_distinguishes_value_types() -> None:
    assert diff({"tokens": 1}, {"tokens": True}) == ["trace.tokens: expected 1, got True"]


def test_canonical_replaces_only_the_trace_id() -> None:
    case = next(c for c in CASES if case_id(c) == "openai/chat_text")

    trace = produce(case)

    assert trace.trace_id != "trc_normalized"
    assert canonical(trace) == case["golden"]


def test_every_corpus_directory_maps_to_a_matrix_adapter() -> None:
    directories = {p.name for p in CORPUS.iterdir() if p.is_dir() and not p.name.startswith(".")}

    assert directories <= set(MATRIX_NAMES)


def test_corpus_pins_match_installed_sdks() -> None:
    pins = json.loads((CORPUS / "versions.json").read_text())["packages"]
    installed = {}
    for package in pins:
        try:
            installed[package] = metadata.version(package)
        except metadata.PackageNotFoundError:
            continue

    assert {k: pins[k] for k in installed} == installed


@pytest.mark.parametrize("case", CASES, ids=case_id)
def test_case_sdk_package_is_a_pinned_distribution(case: dict[str, Any]) -> None:
    pins = json.loads((CORPUS / "versions.json").read_text())["packages"]

    assert case["sdk"]["package"] in {*pins, "attest-ai"}
