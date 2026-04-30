"""CLI helpers — terminal-grade renderers for Attest results.

Public surface:

- ``render_diagnostics`` — pytest/vitest-style failure report from an
  ``AgentResult``. Used by the pytest plugin and exposed for callers
  that build their own CLI front-ends.
- ``render_summary`` — short header with pass/fail counts, cost, and
  P50/P95 latency. Suitable for use as a one-line PR-summary post.

These helpers are intentionally framework-agnostic (no pytest import)
so the same code can back a future ``attest`` CLI binary.
"""

from __future__ import annotations

from attest.cli.diagnostics import render_diagnostics, render_summary

__all__ = ["render_diagnostics", "render_summary"]
