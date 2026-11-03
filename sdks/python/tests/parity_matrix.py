"""Parity matrix: compares the Python and TypeScript public surfaces.

Standard library only. The snapshots are written by each SDK's own test suite
(``sdks/parity/python.json``, ``sdks/parity/typescript.json``); this module
maps Python names to TypeScript names (snake to camel plus an alias table),
checks ``sdks/parity/exceptions.json`` and renders ``docs/guides/parity.md``.
"""

from __future__ import annotations

import json
import re
from dataclasses import dataclass
from pathlib import Path
from typing import Any

SECTIONS = (
    "adapters",
    "exports",
    "expect_chain",
    "init_flags",
    "plugins",
    "simulation",
    "test_runner",
)
SECTION_TITLES = {
    "adapters": "Adapters",
    "exports": "Top-level exports",
    "expect_chain": "ExpectChain methods",
    "init_flags": "`init` flags",
    "plugins": "Plugin runner",
    "simulation": "Simulation",
    "test_runner": "Test-runner helpers",
}
UPDATE_ENV = "ATTEST_UPDATE_PARITY"

REASONS = ("upstream_python_only", "otel_capture", "language_idiom")

# Outcome capabilities never take an exception: simulation scenario, persona and
# fault injection, the L8 plugin runner with discovery, `init`, capture, replay
# and world helpers, and trials.
OUTCOME_SECTIONS = frozenset({"simulation", "plugins", "init_flags"})
OUTCOME_NAMES = frozenset(
    {
        # capture
        "CaptureError",
        "Fixture",
        "load_fixture",
        "record",
        # replay
        "REASON_REPLAY_MISS",
        "ReplayError",
        "ReplayMissError",
        "ReplayNotEvaluatedError",
        "ReplaySession",
        "ReplaySummary",
        "attest_replay",
        "first_miss",
        "live_line",
        "resolve_mode",
        "start_replay",
        # world
        "WorldError",
        "WorldSession",
        "attest_world",
        "start_world",
        # trials
        "attest_budget",
        "attest_min_pass_rate",
    }
)

Surface = dict[str, list[str]]


def snake_to_camel(name: str) -> str:
    """``output_has_all_keywords`` to ``outputHasAllKeywords``; CamelCase and
    CONSTANT_CASE names are unchanged."""
    if "_" not in name or name.upper() == name:
        return name
    head, *rest = name.split("_")
    return head + "".join(part[:1].upper() + part[1:] for part in rest)


@dataclass(frozen=True)
class Row:
    section: str
    python: str
    typescript: str
    status: str  # match | alias | exception | gap
    reason: str = ""
    note: str = ""


@dataclass(frozen=True)
class Matrix:
    rows: list[Row]
    problems: list[str]


def load_json(path: Path) -> Any:
    return json.loads(path.read_text(encoding="utf-8"))


def serialize_surface(surface: Surface) -> str:
    ordered = {section: sorted(set(surface[section])) for section in SECTIONS}
    return json.dumps(ordered, indent=2) + "\n"


def compare_surface(committed: Surface, live: Surface) -> list[str]:
    """Drift messages between a committed snapshot and the live surface."""
    messages: list[str] = []
    for section in SECTIONS:
        have = set(committed.get(section, []))
        want = set(live.get(section, []))
        messages += [f"added {section}.{name}" for name in sorted(want - have)]
        messages += [f"removed {section}.{name}" for name in sorted(have - want)]
    return messages


def _exception_problems(
    exceptions: list[dict[str, str]], python: Surface, typescript: Surface, aliases: dict[str, str]
) -> list[str]:
    problems: list[str] = []
    seen: set[tuple[str, str]] = set()
    for exc in exceptions:
        name, section = exc.get("python", ""), exc.get("section", "")
        label = f"{section}.{name}"
        if section not in SECTIONS:
            problems.append(f"exception {name}: section must be one of {', '.join(SECTIONS)}")
            continue
        if (section, name) in seen:
            problems.append(f"duplicate exception {label}")
        seen.add((section, name))
        if exc.get("reason") not in REASONS:
            problems.append(f"exception {label}: reason must be one of {', '.join(REASONS)}")
        if exc.get("reason") == "language_idiom" and not exc.get("note"):
            problems.append(f"exception {label}: language_idiom needs a note naming the TS way")
        if name not in python.get(section, []):
            problems.append(f"stale exception {label}: no such Python name")
            continue
        if name in OUTCOME_NAMES or section in OUTCOME_SECTIONS:
            problems.append(f"exception {label}: outcome capabilities cannot be exceptions")
        counterpart = aliases.get(name, snake_to_camel(name))
        if counterpart in typescript.get(section, []):
            problems.append(f"stale exception {label}: TypeScript now exports {counterpart}")
    return problems


def _alias_problems(
    aliases: dict[str, str],
    python: Surface,
    typescript: Surface,
    exception_keys: set[tuple[str, str]],
) -> list[str]:
    problems: list[str] = []
    for name, target in aliases.items():
        where = [s for s in SECTIONS if name in python.get(s, [])]
        if not where:
            problems.append(f"stale alias {name}: no such Python name")
        elif any(target not in typescript.get(s, []) for s in where):
            problems.append(f"stale alias {name}: TypeScript does not export {target}")
        if any((section, name) in exception_keys for section in where):
            problems.append(f"{name} is both an alias and an exception")
    return problems


def build_matrix(python: Surface, typescript: Surface, exceptions_doc: dict[str, Any]) -> Matrix:
    aliases: dict[str, str] = exceptions_doc["aliases"]
    exceptions: list[dict[str, str]] = exceptions_doc["exceptions"]
    by_key = {(e["section"], e["python"]): e for e in exceptions if "section" in e}
    problems = _exception_problems(exceptions, python, typescript, aliases)
    problems += _alias_problems(aliases, python, typescript, set(by_key))

    rows: list[Row] = []
    for section in SECTIONS:
        have = set(typescript.get(section, []))
        for name in sorted(set(python.get(section, []))):
            target = aliases.get(name, snake_to_camel(name))
            if target in have:
                status = "alias" if name in aliases else "match"
                rows.append(Row(section, name, target, status))
            elif (section, name) in by_key:
                exc = by_key[(section, name)]
                rows.append(
                    Row(section, name, "-", "exception", exc["reason"], exc.get("note", ""))
                )
            else:
                rows.append(Row(section, name, target, "gap"))
                problems.append(
                    f"gap {section}.{name}: no TypeScript counterpart '{target}' "
                    "and no documented exception"
                )
    return Matrix(rows, problems)


def render_matrix(matrix: Matrix) -> str:
    out = [
        "# TypeScript parity",
        "",
        "Every capability the Python SDK exposes has a TypeScript counterpart, or a "
        "documented exception. This page is generated from the public surface of both "
        "SDKs by the parity test (`sdks/python/tests/test_parity.py`); do not edit it by "
        f"hand. Regenerate it with `{UPDATE_ENV}=1` after changing either SDK's surface.",
        "",
        "Python names map to TypeScript names by snake_case to camelCase, plus an explicit "
        "alias table. Capabilities that decide a test outcome (simulation scenarios, "
        "personas and fault injection, the L8 plugin runner and its discovery, `init`, "
        "capture, replay and world helpers, trials) are never exceptions. Commands other "
        "than `init` are forwarded to the engine by both SDKs, so they are identical.",
        "",
        "Exceptions use one of three reasons: `upstream_python_only` (the framework has "
        "no TypeScript release), `otel_capture` (TypeScript users capture the framework "
        "through OpenTelemetry) or `language_idiom` (the TypeScript way is named in the note).",
        "",
    ]
    exceptions = [r for r in matrix.rows if r.status == "exception"]
    out += ["## Documented exceptions", ""]
    if exceptions:
        out += ["| Python | Reason | TypeScript way |", "| --- | --- | --- |"]
        out += [f"| `{r.python}` | `{r.reason}` | {r.note or '-'} |" for r in exceptions]
    else:
        out.append("None.")
    out.append("")
    for section in SECTIONS:
        rows = [r for r in matrix.rows if r.section == section]
        out += [
            f"## {SECTION_TITLES[section]}",
            "",
            "| Python | TypeScript | Status |",
            "| --- | --- | --- |",
        ]
        for r in rows:
            ts = f"`{r.typescript}`" if r.typescript != "-" else "-"
            status = r.status if r.status != "exception" else f"exception (`{r.reason}`)"
            out.append(f"| `{r.python}` | {ts} | {status} |")
        out.append("")
    return "\n".join(out).rstrip() + "\n"


_FLAG = re.compile(r'"(--[a-z][a-z-]*)(?:=[a-z]+)?"')


def init_flags_from_source(source: str) -> list[str]:
    """Flags named in an ``init`` argument parser's source."""
    return sorted(set(_FLAG.findall(source)))
