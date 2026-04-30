#!/usr/bin/env python3
"""Post inline PR review comments for each failed Attest assertion.

Reads a JSON v2 report (engine/internal/report/json.go output) and posts
one comment per failed assertion via the GitHub REST API. The comment
body surfaces every diagnostic field the engine populates so reviewers
do not have to follow links to dig into the failure.

Constraints:
  - Stdlib only — the action runs in vanilla Ubuntu runners with the
    bundled python3, so we cannot pull in PyYAML / requests / pygithub.
  - Idempotent across reruns: existing comments are not replaced. Each
    run posts a single review with all current failures so repeated
    pushes accumulate distinct reviews; reviewers can dismiss old ones.

Exit codes:
  0   — everything posted (or no failures to post)
  1   — invalid args, network error, or unparseable report
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any


# Maximum chars per review comment body — GitHub's hard limit is 65536
# but reviewers stop reading long before that. Truncate to keep noise
# low; the full report is linked in the summary.
MAX_BODY_CHARS = 4000


def _truncate(value: str, limit: int = 280) -> str:
    if len(value) <= limit:
        return value
    return value[: limit - 3] + "..."


def _format_failure(result: dict[str, Any]) -> str:
    """Render a single failed assertion as a markdown comment body.

    Mirrors the engine's writeFailureBlock layout so a reviewer reading
    the inline comment sees the same shape as the PR-summary report.
    """
    layer = result.get("layer", 0) or 0
    type_name = result.get("type") or "(unknown)"
    status = result.get("status", "unknown")
    score = result.get("score", 0.0)

    lines: list[str] = []
    lines.append(f"**Attest assertion failed: `{result.get('assertion_id')}`**")
    lines.append(f"")
    lines.append(f"- **Layer:** L{layer} {type_name}")
    lines.append(f"- **Status:** `{status}` (score {score:.3f})")
    if path := result.get("trace_node_path"):
        lines.append(f"- **Trace path:** `{path}`")
    if expected := result.get("expected"):
        lines.append(f"- **Expected:** {_truncate(expected)}")
    if actual := result.get("actual"):
        lines.append(f"- **Actual:** {_truncate(actual)}")
    if explanation := result.get("explanation"):
        lines.append(f"- **Detail:** {_truncate(explanation)}")
    if cls := result.get("failure_class"):
        lines.append(f"- **Class:** `{cls}`")
    if ts := result.get("threshold_source"):
        if ts != "static":
            lines.append(f"- **Threshold source:** `{ts}`")
    if hint := result.get("suggested_action"):
        lines.append(f"- **Hint:** {_truncate(hint)}")
    body = "\n".join(lines)
    if len(body) > MAX_BODY_CHARS:
        body = body[: MAX_BODY_CHARS - 3] + "..."
    return body


def _summary_body(report: dict[str, Any], failures: list[dict[str, Any]]) -> str:
    """Top-level review body posted alongside inline comments."""
    summary = report.get("summary", {})
    total = summary.get("total", 0)
    passed = summary.get("passed", 0)
    soft = summary.get("soft_fail", 0)
    hard = summary.get("hard_fail", 0)
    cost = report.get("total_cost", 0.0)
    p50 = report.get("p50_duration_ms", 0)
    p95 = report.get("p95_duration_ms", 0)

    parts = [
        f"**Attest:** {len(failures)} failing assertion(s) of {total}.",
        f"PASS {passed} | SOFT {soft} | HARD {hard}",
        f"Cost ${cost:.6f} · P50 {p50}ms · P95 {p95}ms",
    ]
    if classes := report.get("failure_classes"):
        items = ", ".join(f"{k}: {v}" for k, v in sorted(classes.items()))
        parts.append(f"Failure classes — {items}")
    if simulated := report.get("simulated"):
        if simulated:
            parts.append("⚠ Simulated run — does not exercise the engine.")
    return "\n".join(parts)


def _api_post(url: str, token: str, payload: dict[str, Any]) -> tuple[int, str]:
    """Issue a POST against the GitHub REST API with token auth.

    Returns (status_code, response_body). Raises urllib.error.URLError on
    network failure (caller decides whether to abort).
    """
    data = json.dumps(payload).encode("utf-8")
    req = urllib.request.Request(
        url,
        data=data,
        method="POST",
        headers={
            "Authorization": f"token {token}",
            "Accept": "application/vnd.github+json",
            "Content-Type": "application/json",
            "X-GitHub-Api-Version": "2022-11-28",
        },
    )
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            return resp.status, resp.read().decode("utf-8")
    except urllib.error.HTTPError as exc:
        return exc.code, exc.read().decode("utf-8") if exc.fp else ""


def _resolve_test_file(result: dict[str, Any]) -> tuple[str | None, int | None]:
    """Best-effort extract of (path, line) for inline placement.

    The engine doesn't yet emit source-line metadata, so we look for a
    "source" hint in the explanation (e.g. "tests/test_x.py::test_y" —
    pytest's default), strip the ::test part, and post the comment on
    line 1 of the file. Reviewers see the comment threaded against the
    test file even if not the exact assert line.
    """
    explanation = result.get("explanation", "") or ""
    suggested = result.get("suggested_action", "") or ""
    actual = result.get("actual", "") or ""
    blob = " ".join((explanation, suggested, actual))
    for token in blob.split():
        if "::" in token:
            path = token.split("::", 1)[0]
            if path.endswith((".py", ".ts", ".js", ".go")):
                return path, 1
    return None, None


def main() -> int:
    parser = argparse.ArgumentParser(description="Post Attest PR annotations.")
    parser.add_argument("--report", required=True, help="Path to JSON v2 report")
    parser.add_argument("--repo", required=True, help="owner/name (e.g. attest-framework/attest)")
    parser.add_argument("--pr", required=True, type=int, help="Pull request number")
    parser.add_argument("--commit", required=True, help="Head commit SHA")
    args = parser.parse_args()

    token = os.environ.get("GITHUB_TOKEN")
    if not token:
        print("post_annotations: GITHUB_TOKEN not set", file=sys.stderr)
        return 1

    report_path = Path(args.report)
    if not report_path.is_file():
        print(f"post_annotations: report not found: {report_path}", file=sys.stderr)
        return 1
    try:
        report = json.loads(report_path.read_text())
    except json.JSONDecodeError as exc:
        print(f"post_annotations: invalid JSON: {exc}", file=sys.stderr)
        return 1

    if report.get("report_version") != 2:
        print(
            f"post_annotations: unsupported report_version "
            f"{report.get('report_version')} (need 2)",
            file=sys.stderr,
        )
        return 1

    results = report.get("results", [])
    failures = [r for r in results if r.get("status") in ("hard_fail", "soft_fail")]

    summary = _summary_body(report, failures)

    comments: list[dict[str, Any]] = []
    summary_only_failures: list[dict[str, Any]] = []
    for r in failures:
        path, line = _resolve_test_file(r)
        if path is None:
            summary_only_failures.append(r)
            continue
        comments.append(
            {
                "path": path,
                "line": line,
                "side": "RIGHT",
                "body": _format_failure(r),
            }
        )

    body_parts = [summary]
    if summary_only_failures:
        body_parts.append("")
        body_parts.append("**Failures without source-line hint:**")
        body_parts.append("")
        for r in summary_only_failures:
            body_parts.append(_format_failure(r))
            body_parts.append("")
    review_body = "\n".join(body_parts)
    if len(review_body) > MAX_BODY_CHARS:
        review_body = review_body[: MAX_BODY_CHARS - 3] + "..."

    payload = {
        "commit_id": args.commit,
        "body": review_body,
        "event": "COMMENT",
        "comments": comments,
    }
    url = f"https://api.github.com/repos/{args.repo}/pulls/{args.pr}/reviews"
    status, body = _api_post(url, token, payload)
    if status >= 300:
        print(f"post_annotations: GitHub API returned {status}: {body}", file=sys.stderr)
        return 1
    print(f"post_annotations: posted review with {len(comments)} inline comment(s)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
