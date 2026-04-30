"""Terminal-grade rendering of Attest assertion failures.

Produces a pytest/vitest-style report block that surfaces:

- Layer and assertion type
- Trace node path
- Expected vs actual evidence
- Judge audit metadata (model, rubric, sample scores, variance flag)
- Suggested next action
- Cost and latency

The renderer is colour-aware: when ``stream.isatty()`` returns True
(or ``force_color`` is set), it emits ANSI escapes; otherwise plain
text is produced so PR comments and log captures stay readable.
"""

from __future__ import annotations

from io import StringIO
from typing import TYPE_CHECKING

from attest._proto.types import (
    STATUS_HARD_FAIL,
    STATUS_PASS,
    STATUS_SOFT_FAIL,
    AssertionResult,
    JudgeMetadata,
)

if TYPE_CHECKING:
    from attest.result import AgentResult

# ANSI escape codes — kept narrow because terminal renderers vary wildly
# on which 256-colour codes survive.
_ANSI_RESET: str = "\x1b[0m"
_ANSI_RED: str = "\x1b[31m"
_ANSI_YELLOW: str = "\x1b[33m"
_ANSI_GREEN: str = "\x1b[32m"
_ANSI_DIM: str = "\x1b[2m"
_ANSI_BOLD: str = "\x1b[1m"


_LAYER_NAMES: dict[int, str] = {
    1: "schema",
    2: "constraint",
    3: "trace",
    4: "content",
    5: "embedding",
    6: "llm_judge",
    7: "trace_tree",
    8: "plugin",
}


def _layer_label(layer: int, assertion_type: str | None) -> str:
    """Render ``L{layer} {name}`` with a fallback to the assertion type."""
    if layer == 0 and assertion_type:
        return f"L? {assertion_type}"
    name = _LAYER_NAMES.get(layer, assertion_type or "uncategorized")
    return f"L{layer} {name}"


def _status_glyph(status: str, color: bool) -> str:
    """ASCII glyph + status string, colourised when ``color`` is True."""
    if status == STATUS_PASS:
        glyph = "PASS"
        c = _ANSI_GREEN
    elif status == STATUS_SOFT_FAIL:
        glyph = "SOFT"
        c = _ANSI_YELLOW
    elif status == STATUS_HARD_FAIL:
        glyph = "FAIL"
        c = _ANSI_RED
    else:
        glyph = status.upper() or "????"
        c = _ANSI_DIM
    if not color:
        return glyph
    return f"{c}{glyph}{_ANSI_RESET}"


def _bold(s: str, color: bool) -> str:
    return f"{_ANSI_BOLD}{s}{_ANSI_RESET}" if color else s


def _dim(s: str, color: bool) -> str:
    return f"{_ANSI_DIM}{s}{_ANSI_RESET}" if color else s


def _truncate(value: str, limit: int = 280) -> str:
    if len(value) <= limit:
        return value
    return value[: limit - 3] + "..."


def render_assertion_failure(
    result: AssertionResult,
    *,
    color: bool = False,
    test_file: str | None = None,
) -> str:
    """Render a single failed assertion as a multi-line block.

    Mirrors the engine markdown report structure so reviewers see the
    same fields whether they read the PR comment or the local pytest
    output.
    """
    buf = StringIO()
    layer = result.layer or 0
    label = _layer_label(layer, result.type)
    buf.write(
        f"  {_status_glyph(result.status, color)} "
        f"{_bold(result.assertion_id, color)} "
        f"{_dim('— ' + label, color)}\n"
    )
    if result.trace_node_path:
        buf.write(f"      trace path: {result.trace_node_path}\n")
    buf.write(f"      score:      {result.score:.3f}\n")
    if result.expected:
        buf.write(f"      expected:   {_truncate(result.expected)}\n")
    if result.actual:
        buf.write(f"      actual:     {_truncate(result.actual)}\n")
    if result.explanation:
        buf.write(f"      detail:     {_truncate(result.explanation)}\n")
    if result.threshold_source and result.threshold_source != "static":
        buf.write(f"      threshold:  {result.threshold_source}\n")
    if result.failure_class:
        buf.write(f"      class:      {result.failure_class}\n")
    if result.judge_metadata is not None:
        _render_judge_meta(buf, result.judge_metadata)
    if result.suggested_action:
        buf.write(f"      hint:       {_truncate(result.suggested_action)}\n")
    if result.cost or result.duration_ms:
        buf.write(f"      cost/lat:   ${result.cost:.6f} / {result.duration_ms}ms\n")
    if test_file:
        buf.write(f"      source:     {test_file}\n")
    return buf.getvalue()


def _render_judge_meta(buf: StringIO, meta: JudgeMetadata) -> None:
    if meta.model:
        rubric = meta.rubric_name or "default"
        if meta.rubric_version:
            rubric = f"{rubric} @ {meta.rubric_version}"
        buf.write(f"      judge:      {meta.model} / {rubric}\n")
    if meta.prompt_hash:
        buf.write(f"      prompt:     #{meta.prompt_hash}\n")
    if len(meta.sample_scores) > 1:
        samples = ", ".join(f"{s:.2f}" for s in meta.sample_scores)
        variance_flag = " ⚠ HIGH VARIANCE" if meta.high_variance else ""
        buf.write(
            f"      samples:    [{samples}] mean={meta.score_mean:.2f} "
            f"stddev={meta.score_stddev:.2f}{variance_flag}\n"
        )
    if meta.bias_probes:
        probes = ", ".join(f"{p.name} Δ{p.delta:+.2f}" for p in meta.bias_probes)
        buf.write(f"      bias:       {probes}\n")
    if meta.calibration is not None:
        cal = meta.calibration
        buf.write(
            f"      calibrated: {cal.label_count} labels, "
            f"agreement={cal.agreement:.2f}, κ={cal.cohen_kappa:.2f}\n"
        )


def render_summary(result: AgentResult, *, color: bool = False) -> str:
    """One-line summary of an AgentResult.

    Format: ``{N passed, N soft, N hard | cost $X | dur Yms}``.
    """
    pass_n = result.pass_count
    soft = sum(1 for r in result.assertion_results if r.status == STATUS_SOFT_FAIL)
    hard = sum(1 for r in result.assertion_results if r.status == STATUS_HARD_FAIL)
    parts = [
        f"{_status_glyph(STATUS_PASS, color)} {pass_n}",
        f"{_status_glyph(STATUS_SOFT_FAIL, color)} {soft}",
        f"{_status_glyph(STATUS_HARD_FAIL, color)} {hard}",
        f"cost ${result.total_cost:.6f}",
        f"dur {result.total_duration_ms}ms",
    ]
    return " | ".join(parts)


def render_diagnostics(
    result: AgentResult,
    *,
    color: bool = False,
    include_passing: bool = False,
    test_file: str | None = None,
) -> str:
    """Render every failing assertion (or all when ``include_passing``).

    Output is structured for direct inclusion in a pytest assertion
    message — leading newline, indented blocks, no trailing summary.
    """
    rows: list[AssertionResult] = (
        list(result.assertion_results) if include_passing else list(result.failed_assertions)
    )
    if not rows:
        return _dim("  (no failures)\n", color)
    buf = StringIO()
    buf.write("\n")
    buf.write(
        _bold(
            f"Attest diagnostic — {len(rows)} of "
            f"{len(result.assertion_results)} assertions failed:\n",
            color,
        )
    )
    for r in rows:
        buf.write(render_assertion_failure(r, color=color, test_file=test_file))
        buf.write("\n")
    buf.write(_dim(f"  Summary: {render_summary(result, color=color)}\n", color))
    return buf.getvalue()
