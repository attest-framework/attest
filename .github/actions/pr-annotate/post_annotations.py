#!/usr/bin/env python3
"""Post PR review comments and workflow annotations for failed Attest assertions.

Reads a report-v2 file (proto/attest/v1/report-v2.schema.json) written by
`pytest --attest-report` or the Vitest AttestReporter. Each failed row
carries the file and line of the test that requested it: the failure is
posted as an inline review comment on that line when the line is part of
the pull request diff, and listed in the review body otherwise. Located
failures are also emitted as workflow annotations (``::error`` for hard
failures, ``::warning`` for soft ones), which GitHub shows at the file and
line. Rows without a location go into the review
body; the script never guesses a line. The comment body surfaces every
diagnostic field the engine populates.

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
import math
import os
import re
import sys
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any


# Maximum chars per review body — GitHub's hard limit is 65536 but reviewers
# stop reading long before that. The body holds whole failure blocks up to
# this size and then counts the rest; one block is bounded well below it.
MAX_BODY_CHARS = 4000


def _truncate(value: str, limit: int = 280) -> str:
    if len(value) <= limit:
        return value
    return value[: limit - 3] + "..."


# Control, line/paragraph separator, bidi override/isolate and zero-width space.
_CONTROL = re.compile(r"[\x00-\x08\x0b-\x1f\x7f\u200b\u2028\u2029\u202a-\u202e\u2066-\u2069]")


def _text(value: object, limit: int) -> str:
    """Untrusted value as one line of plain text: no control or bidi characters."""
    try:
        raw = str(value)
    except ValueError:  # an integer too long to print (sys.int_info.str_digits_check_threshold)
        raw = "(out of range)"
    return _truncate(" ".join(_CONTROL.sub(" ", raw).split()), limit)


def _md(value: object, limit: int = 280) -> str:
    """Render an untrusted report field as an inline code span.

    The report comes from the pull request's own test run. Inside a code span
    GitHub renders no markdown, HTML, entities, mentions, autolinks or issue
    references. Whitespace runs collapse to one space (the span cannot end a
    line) and backticks become quotes (the span cannot be closed early).
    """
    text = _text(value, limit).replace("`", "'")
    return f"`{text}`" if text else "`(empty)`"


def _bounded(value: object, low: float, high: float, fmt: str) -> str:
    """A report number in [low, high] formatted with ``fmt``; anything else as inert text."""
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        return _md(value, 20)
    if isinstance(value, float) and not math.isfinite(value):
        return _md(value, 20)
    if not low <= value <= high:
        return "`(out of range)`"
    return format(value, fmt)


def _layer_text(value: object) -> str:
    return _bounded(value or 0, 0, 8, "d" if isinstance(value, int) else "g")


def _count_text(value: object) -> str:
    return _bounded(value, 0, 10**9, "d" if isinstance(value, int) else "g")


def _format_failure(result: dict[str, Any]) -> str:
    """Render a single failed assertion as a markdown comment body.

    Mirrors the engine's writeFailureBlock layout so a reviewer reading
    the inline comment sees the same shape as the PR-summary report.
    """
    type_name = result.get("type") or "(unknown)"
    status = result.get("status", "unknown")
    score_text = _bounded(result.get("score", 0.0), -1e6, 1e6, ".3f")

    lines: list[str] = []
    lines.append(f"**Attest assertion failed: {_md(result.get('assertion_id'), 80)}**")
    lines.append("")
    lines.append(f"- **Layer:** L{_layer_text(result.get('layer'))} {_md(type_name, 40)}")
    lines.append(f"- **Status:** {_md(status, 20)} (score {score_text})")
    if path := result.get("trace_node_path"):
        lines.append(f"- **Trace path:** {_md(path)}")
    if expected := result.get("expected"):
        lines.append(f"- **Expected:** {_md(expected)}")
    if actual := result.get("actual"):
        lines.append(f"- **Actual:** {_md(actual)}")
    if explanation := result.get("explanation"):
        lines.append(f"- **Detail:** {_md(explanation)}")
    if cls := result.get("failure_class"):
        lines.append(f"- **Class:** {_md(cls, 40)}")
    if ts := result.get("threshold_source"):
        if ts != "static":
            lines.append(f"- **Threshold source:** {_md(ts, 40)}")
    if hint := result.get("suggested_action"):
        lines.append(f"- **Hint:** {_md(hint)}")
    # Every field is bounded, so a block stays under MAX_BODY_CHARS (about 2 KB max).
    return "\n".join(lines)


_MAX_CLASSES = 10


def _summary_body(report: dict[str, Any], failures: list[dict[str, Any]]) -> str:
    """Top-level review body posted alongside inline comments."""
    summary = report.get("summary", {})
    total = _count_text(summary.get("total", 0))
    passed = _count_text(summary.get("passed", 0))
    soft = _count_text(summary.get("soft_fail", 0))
    hard = _count_text(summary.get("hard_fail", 0))
    cost = _bounded(report.get("total_cost", 0.0), 0, 1e9, ".6f")
    p50 = _count_text(report.get("p50_duration_ms", 0))
    p95 = _count_text(report.get("p95_duration_ms", 0))

    parts = [
        f"**Attest:** {len(failures)} failing assertion(s) of {total}.",
        f"PASS {passed} | SOFT {soft} | HARD {hard}",
        f"Cost ${cost} · P50 {p50}ms · P95 {p95}ms",
    ]
    if classes := report.get("failure_classes"):
        shown = sorted(classes.items(), key=lambda kv: str(kv[0]))[:_MAX_CLASSES]
        items = ", ".join(f"{_md(k, 40)}: {_count_text(v)}" for k, v in shown)
        if len(classes) > _MAX_CLASSES:
            items += f", and {len(classes) - _MAX_CLASSES} more"
        parts.append(f"Failure classes — {items}")
    if simulated := report.get("simulated"):
        if simulated:
            parts.append("⚠ Simulated run — does not exercise the engine.")
    return "\n".join(parts)


def _api_request(
    method: str, url: str, token: str, payload: dict[str, Any] | None = None
) -> tuple[int, str]:
    """Issue a request against the GitHub REST API with token auth.

    Returns (status_code, response_body). Raises urllib.error.URLError on
    network failure (caller decides whether to abort).
    """
    data = json.dumps(payload).encode("utf-8") if payload is not None else None
    req = urllib.request.Request(
        url,
        data=data,
        method=method,
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


def _source_location(result: dict[str, Any]) -> tuple[str, int] | None:
    """Return the (file, line) of the test that requested this assertion.

    report-v2 rows written by the SDK test runners carry ``file`` (relative to
    the runner's root, forward slashes) and ``line`` (1-based line of the test
    definition). Rows without them, or with an absolute, parent-relative or
    malformed location, return None: the failure goes into the review summary
    instead of being pinned to a guessed line.
    """
    path = result.get("file")
    line = result.get("line")
    if not isinstance(path, str) or not path or isinstance(line, bool) or not isinstance(line, int):
        return None
    if line < 1 or path.startswith("/") or "\\" in path or ":" in path:
        return None
    if any(part in ("", ".", "..") for part in path.split("/")):
        return None
    if _CONTROL.search(path) or any(c in path for c in "`%\n\r\t"):
        return None
    return path, line


def _diff_lines(patch: str) -> set[int]:
    """Right-side (head) line numbers a review comment can attach to in one file's patch."""
    lines: set[int] = set()
    right = 0
    # GitHub patches separate lines with "\n" only; str.splitlines() would also
    # split on form feeds and Unicode separators inside a line's content.
    for raw in patch.split("\n"):
        row = raw.removesuffix("\r")
        if row.startswith("@@"):
            match = _HUNK.match(row)
            if match is None:
                raise ValueError(f"unparseable hunk header: {row!r}")
            right = int(match.group(1))
        elif row.startswith("+") or row.startswith(" "):
            lines.add(right)
            right += 1
    return lines


_REPO = re.compile(r"^(?!\.\.?/)[A-Za-z0-9_.-]+/(?!\.\.?$)[A-Za-z0-9_.-]+$")
_SHA = re.compile(r"^[0-9a-f]{40}$")

_HUNK = re.compile(r"^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@")

# GitHub returns at most 3000 files for a pull request, 100 per page.
_MAX_FILE_PAGES = 30


def _pull_request_lines(repo: str, pr: int, token: str) -> dict[str, set[int]]:
    """Map each file in the pull request to the head lines its diff shows."""
    commentable: dict[str, set[int]] = {}
    for page in range(1, _MAX_FILE_PAGES + 1):
        url = f"https://api.github.com/repos/{repo}/pulls/{pr}/files?per_page=100&page={page}"
        status, body = _api_request("GET", url, token)
        if status >= 300:
            raise RuntimeError(f"listing pull request files returned {status}: {body}")
        files = json.loads(body)
        for f in files:
            commentable[f["filename"]] = _diff_lines(f.get("patch") or "")
        if len(files) < 100:
            break
    return commentable


def _escape_data(value: str) -> str:
    escaped = value.replace("%", "%25").replace("\r", "%0D").replace("\n", "%0A")
    return _CONTROL.sub("", escaped)


def _escape_property(value: str) -> str:
    return _escape_data(value).replace(":", "%3A").replace(",", "%2C")


def _workflow_annotation(path: str, line: int, result: dict[str, Any]) -> str:
    """An ``::error`` (hard failure) or ``::warning`` (soft failure) workflow command.

    GitHub shows it at path:line in the run summary and the diff.
    """
    layer = _layer_text(result.get("layer")).strip("`")
    title = f"Attest L{layer} {_text(result.get('type') or 'assertion', 40)} failed"
    message = "; ".join(
        f"{label}: {_text(result[key], 300)}"
        for label, key in (("expected", "expected"), ("actual", "actual"), ("detail", "explanation"))
        if result.get(key)
    )
    level = "error" if result.get("status") == "hard_fail" else "warning"
    return (
        f"::{level} file={_escape_property(path)},line={line},"
        f"title={_escape_property(title)}::{_escape_data(_truncate(message, 1000))}"
    )


def main() -> int:
    parser = argparse.ArgumentParser(description="Post Attest PR annotations.")
    parser.add_argument("--report", required=True, help="Path to report-v2 JSON file")
    parser.add_argument("--repo", required=True, help="owner/name (e.g. attest-framework/attest)")
    parser.add_argument("--pr", required=True, type=int, help="Pull request number")
    parser.add_argument("--commit", required=True, help="Head commit SHA")
    args = parser.parse_args()

    if not _REPO.match(args.repo) or not _SHA.match(args.commit):
        print("post_annotations: --repo must be owner/name and --commit a 40-hex SHA", file=sys.stderr)
        return 1

    token = os.environ.get("GITHUB_TOKEN")
    if not token:
        print("post_annotations: GITHUB_TOKEN not set", file=sys.stderr)
        return 1

    report_path = Path(args.report)
    if not report_path.is_file():
        print(f"post_annotations: report not found: {report_path}", file=sys.stderr)
        return 1
    try:
        report = json.loads(report_path.read_text(encoding="utf-8"))
    # ValueError covers JSONDecodeError, UnicodeDecodeError and an integer
    # literal longer than sys.get_int_max_str_digits() (4300 by default).
    except (OSError, ValueError) as exc:
        print(f"post_annotations: cannot read report: {exc}", file=sys.stderr)
        return 1
    if not isinstance(report, dict) or not isinstance(report.get("results", []), list):
        print("post_annotations: report must be an object with a results list", file=sys.stderr)
        return 1
    for key in ("summary", "failure_classes"):
        if not isinstance(report.get(key, {}), dict):
            print(f"post_annotations: report {key} must be an object", file=sys.stderr)
            return 1

    if report.get("report_version") != 2:
        print(
            "post_annotations: unsupported report_version "
            f"{_escape_data(_text(report.get('report_version'), 40))} (need 2)",
            file=sys.stderr,
        )
        return 1

    results = report.get("results", [])
    failures = [
        r for r in results if isinstance(r, dict) and r.get("status") in ("hard_fail", "soft_fail")
    ]
    if not failures:
        print("post_annotations: no failing assertions; nothing to post")
        return 0

    try:
        commentable = _pull_request_lines(args.repo, args.pr, token)
    except (RuntimeError, ValueError, KeyError, TypeError) as exc:
        print(f"post_annotations: {exc}", file=sys.stderr)
        return 1

    payload, annotations = build_review(report, failures, commentable, args.commit)
    for annotation in annotations:
        print(annotation)

    url = f"https://api.github.com/repos/{args.repo}/pulls/{args.pr}/reviews"
    status, body = _api_request("POST", url, token, payload)
    if status >= 300:
        print(f"post_annotations: GitHub API returned {status}: {body}", file=sys.stderr)
        return 1
    print(f"post_annotations: posted review with {len(payload['comments'])} inline comment(s)")
    return 0


def build_review(
    report: dict[str, Any],
    failures: list[dict[str, Any]],
    commentable: dict[str, set[int]],
    commit: str,
) -> tuple[dict[str, Any], list[str]]:
    """Return the review payload and the workflow annotations for ``failures``.

    A failure whose test location is a line of the pull request diff becomes an
    inline comment there. Every other failure is listed in the review body, with
    its ``file:line`` when the report has one. Every located failure also gets a
    workflow annotation, which GitHub shows at that file and line whether or not
    the line is in the diff.
    """
    comments: list[dict[str, Any]] = []
    annotations: list[str] = []
    elsewhere: list[str] = []
    for r in failures:
        location = _source_location(r)
        if location is None:
            elsewhere.append(_format_failure(r))
            continue
        path, line = location
        annotations.append(_workflow_annotation(path, line, r))
        if line in commentable.get(path, set()):
            comments.append({"path": path, "line": line, "side": "RIGHT", "body": _format_failure(r)})
        else:
            elsewhere.append(f"{_md(f'{path}:{line}', 220)} (not in this diff)\n\n{_format_failure(r)}")

    payload = {
        "commit_id": commit,
        "body": _review_body(_summary_body(report, failures), elsewhere),
        "event": "COMMENT",
        "comments": comments,
    }
    return payload, annotations


def _review_body(summary: str, blocks: list[str]) -> str:
    """The summary plus whole failure blocks while they fit in MAX_BODY_CHARS.

    Blocks are never cut (a cut could leave a code span open); the ones that do
    not fit are counted in a closing line that points at the full results.
    """
    body = summary
    if not blocks:
        return body
    body += "\n\n**Failures outside this diff or without a source location:**\n"
    for shown, block in enumerate(blocks):
        rest = len(blocks) - shown
        more = _more_line(rest - 1) if rest > 1 else ""
        if len(body) + 2 + len(block) + len(more) > MAX_BODY_CHARS:
            return body + _more_line(rest)
        body += "\n" + block + "\n"
    return body


def _more_line(count: int) -> str:
    return (
        f"\n… and {count} more failure(s); see the workflow annotations and the "
        "attest-report artifact.\n"
    )


if __name__ == "__main__":
    sys.exit(main())
