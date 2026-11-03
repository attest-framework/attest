#!/usr/bin/env python3
"""Evaluate the policy, post one PR summary comment and emit workflow annotations.

Reads one or more report-v2 files (proto/attest/v1/report-v2.schema.json)
written by `pytest --attest-report` or the Vitest AttestReporter and drives
`attest-engine`:

  1. `policy evaluate` on each report when a policy is given; the worst exit
     wins (any code other than 0/1/2 is an evaluator error and outranks 1,
     then 1 > 2 > 0).
  2. `report summary` renders the Markdown summary. It is appended to
     $GITHUB_STEP_SUMMARY always, and on same-repo pull requests posted as
     ONE issue comment, found by a hidden marker and updated in place.
  3. `report rows` lists failures and replay misses; each becomes a workflow
     command (``::error`` / ``::warning``) that GitHub shows in the checks
     list and inline on the file and line.

Constraints:
  - Stdlib only: the action runs on vanilla Ubuntu runners with python3.
  - Report content is untrusted (it can hold agent output): it only ever
    reaches GitHub as escaped workflow-command data, as a JSON string in an
    API body, or as engine-rendered Markdown; never through a shell.
  - The token is used only for the comment API calls and is never printed.

Exit codes:
  0   - done (a comment that could not be posted is a warning, not a failure)
  1   - bad arguments, unreadable report, or an engine failure other than
        the policy verdict
"""

from __future__ import annotations

import argparse
import glob
import json
import os
import re
import shutil
import subprocess
import sys
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any

MARKER = "<!-- attest:pr-annotate -->"
DEFAULT_ENGINE = "attest-engine"

# GitHub shows 10 errors and 10 warnings per step and 50 per job.
MAX_PER_LEVEL = 10
# One warning slot stays free for the "comment not posted" warning.
MAX_ROWS = {"error": MAX_PER_LEVEL, "warning": MAX_PER_LEVEL - 1}
MAX_MESSAGE_CHARS = 1000
MAX_TITLE_CHARS = 100
# GitHub rejects comment bodies over 65536 characters.
MAX_COMMENT_CHARS = 60000
MAX_COMMENT_PAGES = 50
ENGINE_TIMEOUT_S = 120

_TOKEN_VARS = ("GITHUB_TOKEN", "INPUT_GITHUB-TOKEN")
_REPO = re.compile(r"^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$")

# Control characters other than tab and newline, line/paragraph separators,
# bidi marks/overrides/isolates, zero-width characters, word joiner and BOM.
_CONTROL = re.compile(
    r"[\x00-\x08\x0b-\x1f\x7f؜​-‏  ‪-‮"
    r"⁠⁦-⁩﻿]"
)


class ActionError(Exception):
    """A failure the step reports and exits 1 on."""


def _truncate(value: str, limit: int) -> str:
    if len(value) <= limit:
        return value
    return value[: limit - 3] + "..."


def _clean(value: object) -> str:
    """Untrusted value as text with CRLF folded to LF and control characters removed."""
    try:
        raw = str(value)
    except ValueError:  # an integer too long to print
        raw = "(out of range)"
    return _CONTROL.sub("", raw.replace("\r\n", "\n").replace("\r", "\n"))


def _one_line(value: object, limit: int) -> str:
    return _truncate(" ".join(_clean(value).split()), limit)


def _escape_data(value: str) -> str:
    """Workflow-command data: ``%``, CR and LF are percent-encoded."""
    return value.replace("%", "%25").replace("\r", "%0D").replace("\n", "%0A")


def _escape_property(value: str) -> str:
    """Workflow-command property: data escapes plus ``:`` and ``,``."""
    return _escape_data(value).replace(":", "%3A").replace(",", "%2C")


# --- engine ---------------------------------------------------------------


def resolve_engine(binary: str, env: dict[str, str]) -> str | None:
    """The engine executable, or None when it cannot be found.

    A custom ``engine-binary`` is used as given. The default name is looked up
    the way the SDKs do: PATH, then ``ATTEST_ENGINE_PATH``, then the
    auto-download cache ``~/.attest/bin/<version>/attest-engine``.
    """
    found = shutil.which(binary)
    if found or binary != DEFAULT_ENGINE:
        return found
    override = env.get("ATTEST_ENGINE_PATH")
    if override and os.access(override, os.X_OK):
        return override
    cached = sorted(glob.glob(os.path.expanduser("~/.attest/bin/*/attest-engine")))
    return cached[-1] if cached else None


def _run_engine(
    engine: str, args: list[str], env: dict[str, str]
) -> subprocess.CompletedProcess[bytes]:
    try:
        return subprocess.run(
            [engine, *args], capture_output=True, timeout=ENGINE_TIMEOUT_S, check=False, env=env
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise ActionError(
            f"attest-engine {' '.join(args[:2])} did not run: {_one_line(exc, 200)}"
        ) from exc


def split_reports(raw: str) -> list[str]:
    """Report paths from a newline- or space-separated input."""
    paths = raw.split()
    if not paths:
        raise ActionError("no report path given")
    return paths


def _exit_rank(code: int) -> int:
    if code not in (0, 1, 2):
        return 3
    return {0: 0, 2: 1, 1: 2}[code]


def evaluate_policies(
    engine: str | None,
    reports: list[str],
    policy: str,
    baseline: str,
    out_file: Path,
    env: dict[str, str],
) -> tuple[int, Path | None]:
    """Run `policy evaluate` on each report; return (worst exit, result file of that report).

    No policy gives (0, None). ``out_file`` receives the worst report's result
    (the first one on a tie), the same file a single-report run always wrote.
    """
    if not policy:
        return 0, None
    worst_code: int | None = None
    worst_out = b""
    for report in reports:
        if engine is None:
            code, out = 127, b""
            print(f"::error::attest-engine not found; cannot evaluate the policy on {_escape_data(report)}")
        else:
            args = ["policy", "evaluate", "--policy", policy, "--report", report]
            if baseline:
                args += ["--baseline", baseline]
            proc = _run_engine(engine, args, env)
            code, out = proc.returncode, proc.stdout
            if proc.stderr:
                sys.stderr.write(_clean(proc.stderr.decode("utf-8", "replace")))
        if len(reports) > 1:
            print(f"policy evaluate {_one_line(report, 200)}: exit {code}")
        sys.stdout.write(out.decode("utf-8", "replace"))
        if worst_code is None or _exit_rank(code) > _exit_rank(worst_code):
            worst_code, worst_out = code, out
    assert worst_code is not None
    out_file.write_bytes(worst_out)
    return worst_code, out_file if worst_out.strip() else None


# --- annotations ----------------------------------------------------------


def _valid_path(path: object) -> bool:
    if not isinstance(path, str) or not path:
        return False
    if path.startswith("/") or "\\" in path or ":" in path or _CONTROL.search(path):
        return False
    if any(c in path for c in "`%\n\r\t"):
        return False
    return all(part not in ("", ".", "..") for part in path.split("/"))


def root_relative(file: object, report: str, workspace: Path) -> str | None:
    """The repo-root-relative form of a row's ``file``, or None when it has none.

    Rows carry paths relative to the directory the tests ran from, which the
    SDK runners take to be the directory the report was written in. The path
    is therefore prefixed with the report's directory relative to the
    workspace; when that file does not exist but the unprefixed one does, the
    unprefixed one is used.
    """
    if not _valid_path(file):
        return None
    assert isinstance(file, str)
    report_dir = Path(os.path.abspath(report)).parent
    try:
        prefix = report_dir.relative_to(os.path.realpath(workspace)).as_posix()
    except ValueError:
        try:
            prefix = report_dir.relative_to(os.path.abspath(workspace)).as_posix()
        except ValueError:
            prefix = "."
    candidates = [file] if prefix == "." else [f"{prefix}/{file}", file]
    for candidate in candidates:
        if (workspace / candidate).exists():
            return candidate
    return candidates[0]


def row_annotation(row: dict[str, Any], path: str | None) -> tuple[str, str]:
    """(level, workflow command) for one `report rows` entry.

    Hard failures and replay misses are errors; soft failures and not-evaluated
    rows are warnings.
    """
    status = _one_line(row.get("status") or "failed", 30)
    reason = _one_line(row.get("reason") or "", 60)
    is_error = status == "hard_fail" or reason == "replay_miss"
    if reason == "replay_miss":
        what = "replay miss"
    elif status == "not_evaluated":
        what = f"not evaluated ({reason})" if reason else "not evaluated"
    elif status == "soft_fail":
        what = "soft failure"
    else:
        what = "failure"
    subject = _one_line(row.get("assertion_id") or row.get("test_id") or "assertion", 60)
    kind = _one_line(row.get("type") or "", 40)
    title = _truncate(f"Attest {what}: {subject}" + (f" ({kind})" if kind else ""), MAX_TITLE_CHARS)

    parts = []
    if row.get("explanation"):
        parts.append(_clean(row["explanation"]).strip())
    if row.get("test_id"):
        parts.append(f"test: {_one_line(row['test_id'], 200)}")
    message = _truncate("\n".join(parts) or what, MAX_MESSAGE_CHARS)

    props = []
    if path is not None:
        props.append(f"file={_escape_property(path)}")
        line = row.get("line")
        if isinstance(line, int) and not isinstance(line, bool) and 1 <= line <= 10**7:
            props.append(f"line={line}")
    props.append(f"title={_escape_property(title)}")
    level = "error" if is_error else "warning"
    return level, f"::{level} {','.join(props)}::{_escape_data(message)}"


def build_annotations(rows: list[tuple[dict[str, Any], str | None]]) -> list[str]:
    """Workflow commands for the rows, at most MAX_ROWS per level.

    When a level overflows, its last slot is one summary annotation counting
    the rest.
    """
    by_level: dict[str, list[str]] = {"error": [], "warning": []}
    for row, path in rows:
        level, command = row_annotation(row, path)
        by_level[level].append(command)
    out: list[str] = []
    for level, commands in by_level.items():
        cap = MAX_ROWS[level]
        if len(commands) > cap:
            hidden = len(commands) - (cap - 1)
            commands = commands[: cap - 1] + [
                f"::{level} title=Attest: {hidden} more {level}s::"
                f"{hidden} more {level}s are in the job summary and the attest-report artifact."
            ]
        out.extend(commands)
    return out


def collect_rows(
    engine: str, reports: list[str], workspace: Path, env: dict[str, str]
) -> list[tuple[dict[str, Any], str | None]]:
    """`report rows` for each report, with each row's path made root-relative."""
    collected: list[tuple[dict[str, Any], str | None]] = []
    for report in reports:
        proc = _run_engine(engine, ["report", "rows", "--report", report, "--json"], env)
        if proc.returncode != 0:
            raise ActionError(
                f"attest-engine report rows exited {proc.returncode} for "
                f"{_one_line(report, 200)}: {_one_line(proc.stderr.decode('utf-8', 'replace'), 300)}"
            )
        try:
            rows = json.loads(proc.stdout)
        except ValueError as exc:
            raise ActionError(f"attest-engine report rows printed invalid JSON: {_one_line(exc, 200)}")
        if not isinstance(rows, list) or not all(isinstance(r, dict) for r in rows):
            raise ActionError("attest-engine report rows must print a JSON array of objects")
        collected.extend((r, root_relative(r.get("file"), report, workspace)) for r in rows)
    return collected


# --- summary comment ------------------------------------------------------


def render_summary(
    engine: str,
    reports: list[str],
    policy_result: Path | None,
    run_url: str,
    env: dict[str, str],
) -> str:
    args = ["report", "summary"]
    for report in reports:
        args += ["--report", report]
    if policy_result is not None:
        args += ["--policy-result", str(policy_result)]
    proc = _run_engine(engine, args, env)
    if proc.returncode != 0:
        raise ActionError(
            f"attest-engine report summary exited {proc.returncode}"
            + (" (bad input)" if proc.returncode == 2 else "")
            + f": {_one_line(proc.stderr.decode('utf-8', 'replace'), 300)}"
        )
    summary = proc.stdout.decode("utf-8", "replace").replace("\x00", "").rstrip()
    suffix = f"\n\n[Workflow run]({run_url})" if run_url else ""
    return _truncate(summary, MAX_COMMENT_CHARS) + suffix


def _api(method: str, url: str, token: str, payload: dict[str, Any] | None = None) -> Any:
    """One GitHub REST call; returns the decoded JSON body. Raises urllib errors."""
    data = json.dumps(payload).encode("utf-8") if payload is not None else None
    req = urllib.request.Request(
        url,
        data=data,
        method=method,
        headers={
            "Authorization": f"Bearer {token}",
            "Accept": "application/vnd.github+json",
            "Content-Type": "application/json",
            "X-GitHub-Api-Version": "2022-11-28",
        },
    )
    with urllib.request.urlopen(req, timeout=30) as resp:
        body = resp.read()
    return json.loads(body) if body else None


BOT_LOGIN = "github-actions[bot]"


def _identity(api: str, token: str) -> str:
    """The login this token comments as: the user for a personal token, else the Actions bot.

    An installation token (the workflow token) cannot call GET /user (403).
    """
    try:
        user = _api("GET", f"{api}/user", token)
    except urllib.error.HTTPError:
        return BOT_LOGIN
    login = user.get("login") if isinstance(user, dict) else None
    return login if isinstance(login, str) and login else BOT_LOGIN


def _marker_comments(api: str, repo: str, pr: int, token: str) -> list[int]:
    """Ids of the PR's marker comments written by this token's identity, oldest first.

    The marker alone proves nothing: anyone who can comment can plant it, and a
    token with write access could then edit their comment. Only comments by the
    token's own login are updated.
    """
    me = _identity(api, token)
    ids: list[int] = []
    for page in range(1, MAX_COMMENT_PAGES + 1):
        batch = _api("GET", f"{api}/repos/{repo}/issues/{pr}/comments?per_page=100&page={page}", token)
        if not isinstance(batch, list):
            raise ValueError("comments response is not a list")
        ids += [
            c["id"]
            for c in batch
            if isinstance(c, dict)
            and MARKER in str(c.get("body", ""))
            and isinstance(c.get("user"), dict)
            and c["user"].get("login") == me
        ]
        if len(batch) < 100:
            break
    return ids


def post_comment(api: str, repo: str, pr: int, token: str, body: str) -> str:
    """Update the marker comment in place, or create it; return "updated" or "created".

    Marker comments by other authors are ignored (see _marker_comments). A
    candidate that has since disappeared or cannot be edited (404, 403) is
    skipped; when none remains a new comment is created.
    """
    payload = {"body": f"{MARKER}\n{body}"}
    for comment_id in _marker_comments(api, repo, pr, token):
        try:
            _api("PATCH", f"{api}/repos/{repo}/issues/comments/{comment_id}", token, payload)
            return "updated"
        except urllib.error.HTTPError as exc:
            if exc.code not in (403, 404):
                raise
    _api("POST", f"{api}/repos/{repo}/issues/{pr}/comments", token, payload)
    return "created"


def _same_repo_pull_request(env: dict[str, str]) -> int | None:
    """The PR number when this is a `pull_request` event from the same repository."""
    if env.get("GITHUB_EVENT_NAME") != "pull_request":
        return None
    path = env.get("GITHUB_EVENT_PATH")
    if not path:
        return None
    try:
        event = json.loads(Path(path).read_text(encoding="utf-8"))
        pr = event["pull_request"]
        head = pr["head"]["repo"]["full_name"]
        number = pr["number"]
    except (OSError, ValueError, KeyError, TypeError):
        return None
    if head != env.get("GITHUB_REPOSITORY") or not isinstance(number, int):
        return None
    return number


def write_step_summary(summary: str, env: dict[str, str]) -> None:
    step_summary = env.get("GITHUB_STEP_SUMMARY")
    if step_summary:
        with open(step_summary, "a", encoding="utf-8") as fh:
            fh.write(summary + "\n")
    else:
        print(summary)


def publish_comment(summary: str, env: dict[str, str]) -> None:
    """Post the PR comment where the token can; the job summary is already written."""
    number = _same_repo_pull_request(env)
    if number is None:
        print(
            "::notice::Attest summary comment not posted: not a same-repository pull request; "
            "the summary is in the job summary"
        )
        return
    repo, token = env.get("GITHUB_REPOSITORY", ""), env.get("GITHUB_TOKEN", "")
    if not _REPO.match(repo) or not token:
        print("::warning::Attest summary comment not posted: GITHUB_REPOSITORY or token missing")
        return
    api = env.get("GITHUB_API_URL", "https://api.github.com").rstrip("/")
    try:
        action = post_comment(api, repo, number, token, summary)
    except urllib.error.HTTPError as exc:
        print(
            f"::warning::Attest summary comment not posted (GitHub API returned {exc.code}); "
            "it is in the job summary. The token needs pull-requests: write."
        )
    except (urllib.error.URLError, OSError, ValueError, KeyError) as exc:
        print(f"::warning::Attest summary comment not posted: {_escape_data(_one_line(exc, 200))}")
    else:
        print(f"post_annotations: summary comment {action}")


# --- entry point ----------------------------------------------------------


def run(args: argparse.Namespace, env: dict[str, str]) -> int:
    reports = split_reports(args.report)
    for report in reports:
        if not Path(report).is_file():
            raise ActionError(f"Attest report not found at {_one_line(report, 200)}")
    workspace = Path(env.get("GITHUB_WORKSPACE") or os.getcwd())

    # The engine never needs the token: it is used only by publish_comment.
    engine_env = {k: v for k, v in env.items() if k not in _TOKEN_VARS}
    engine = resolve_engine(args.engine, engine_env)
    code, result = evaluate_policies(
        engine, reports, args.policy, args.baseline_tag, Path("policy-result.json"), engine_env
    )
    output = env.get("GITHUB_OUTPUT")
    if output:
        with open(output, "a", encoding="utf-8") as fh:
            fh.write(f"exit_code={code}\n")
    else:
        print(f"exit_code={code}")

    if engine is None:
        raise ActionError(
            f"attest-engine not found ({_one_line(args.engine, 200)}); set the engine-binary input"
        )
    run_url = ""
    if env.get("GITHUB_SERVER_URL") and env.get("GITHUB_REPOSITORY") and env.get("GITHUB_RUN_ID"):
        run_url = (
            f"{env['GITHUB_SERVER_URL']}/{env['GITHUB_REPOSITORY']}/actions/runs/{env['GITHUB_RUN_ID']}"
        )
    summary = render_summary(engine, reports, result, run_url, engine_env)
    write_step_summary(summary, env)
    for command in build_annotations(collect_rows(engine, reports, workspace, engine_env)):
        print(command)
    publish_comment(summary, env)
    return 0


def main(argv: list[str] | None = None, env: dict[str, str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Attest PR summary and annotations.")
    parser.add_argument("--report", required=True, help="report-v2 path(s), newline or space separated")
    parser.add_argument("--policy", default="", help="attest.policy.yaml; empty skips evaluation")
    parser.add_argument("--baseline-tag", default="", help="baseline tag for regression rules")
    parser.add_argument("--engine", default=DEFAULT_ENGINE, help="attest-engine binary")
    args = parser.parse_args(argv)
    try:
        return run(args, dict(os.environ) if env is None else env)
    except ActionError as exc:
        print(f"::error::{_escape_data(_one_line(exc, 500))}")
        return 1


if __name__ == "__main__":
    sys.exit(main())
