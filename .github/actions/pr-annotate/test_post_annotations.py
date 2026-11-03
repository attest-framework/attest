"""Tests for post_annotations.py against a stub engine and a fake GitHub API.

The script is stdlib-only so the tests run on the same Ubuntu runner without
`pip install`: ``python3 -m unittest discover -s .github/actions/pr-annotate``
(CI: ci-github-config.yml). The stub engine (testdata/stub_engine.py) stands in
for `attest-engine report summary|rows` and `policy evaluate`.
"""

from __future__ import annotations

import contextlib
import importlib.util
import io
import json
import os
import shutil
import subprocess
import tempfile
import threading
import unittest
import unittest.mock
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path


def _load_module():
    here = Path(__file__).parent
    spec = importlib.util.spec_from_file_location("post_annotations", here / "post_annotations.py")
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


_pa = _load_module()
_STUB = str(Path(__file__).parent / "testdata" / "stub_engine.py")
_TOKEN = "ghs_secret_token_value"
_SIDE_FILES = {"policy_exit": ".policy-exit", "rows_json": ".rows.json", "rows_exit": ".rows-exit"}


class _FakeGitHub:
    """In-memory issue-comments API on a loopback port."""

    def __init__(self) -> None:
        self.comments: list[dict] = []
        self.calls: list[tuple[str, str]] = []
        self.force_status: int | None = None
        self.locked_ids: set[int] = set()  # PATCH on these returns 403
        self.user_login: str | None = None  # GET /user: None is 403 (installation token)
        self.tokens: set[str] = set()
        self.paged = False
        self.user_status = 403  # GET /user failure status while user_login is None
        owner = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, format: str, *args: object) -> None:  # noqa: A002
                pass

            def _reply(self, status: int, body: object) -> None:
                data = json.dumps(body).encode()
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            def _handle(self) -> None:
                owner.calls.append((self.command, self.path))
                owner.tokens.add(self.headers.get("Authorization", ""))
                length = int(self.headers.get("Content-Length") or 0)
                payload = json.loads(self.rfile.read(length)) if length else None
                if owner.force_status:
                    return self._reply(owner.force_status, {"message": "forced"})
                if self.command == "GET" and self.path == "/user":
                    if owner.user_login is None:
                        return self._reply(
                            owner.user_status, {"message": "Resource not accessible by integration"}
                        )
                    return self._reply(200, {"login": owner.user_login})
                if self.command == "GET":
                    page = int(self.path.rsplit("&page=", 1)[1]) if "&page=" in self.path else 1
                    if owner.paged:
                        return self._reply(200, owner.comments[(page - 1) * 100 : page * 100])
                    return self._reply(200, owner.comments)
                if self.command == "POST":
                    assert payload is not None
                    comment = {
                        "id": 100 + len(owner.comments),
                        "body": payload["body"],
                        "user": {
                            "login": owner.user_login or _pa.BOT_LOGIN,
                            "type": "User" if owner.user_login else "Bot",
                        },
                    }
                    owner.comments.append(comment)
                    return self._reply(201, comment)
                comment_id = int(self.path.rsplit("/", 1)[1])
                if comment_id in owner.locked_ids:
                    return self._reply(403, {"message": "not yours"})
                assert payload is not None
                for comment in owner.comments:
                    if comment["id"] == comment_id:
                        comment["body"] = payload["body"]
                        return self._reply(200, comment)
                return self._reply(404, {})

            do_GET = do_POST = do_PATCH = _handle

        self.server = HTTPServer(("127.0.0.1", 0), Handler)
        self.url = f"http://127.0.0.1:{self.server.server_port}"
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def close(self) -> None:
        self.server.shutdown()
        self.server.server_close()


class ActionCase(unittest.TestCase):
    """A workspace with a stub engine, fake GitHub API and workflow environment."""

    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.root = Path(os.path.realpath(self._tmp.name))
        self.log = self.root / "stub.log"
        self.github = _FakeGitHub()
        self.addCleanup(self.github.close)
        cwd = os.getcwd()
        os.chdir(self.root)
        self.addCleanup(os.chdir, cwd)
        self.env = {
            "GITHUB_WORKSPACE": str(self.root),
            "GITHUB_OUTPUT": str(self.root / "output"),
            "GITHUB_STEP_SUMMARY": str(self.root / "step-summary.md"),
            "GITHUB_REPOSITORY": "acme/agent",
            "GITHUB_SERVER_URL": "https://github.com",
            "GITHUB_RUN_ID": "77",
            "GITHUB_API_URL": self.github.url,
            "GITHUB_TOKEN": _TOKEN,
            "STUB_LOG": str(self.log),
            "RUNNER_TEMP": str(self.root / "runner-temp"),
            "PATH": os.environ["PATH"],
        }
        (self.root / "runner-temp").mkdir()
        self.use_pull_request()

    def use_pull_request(self, head_repo: str = "acme/agent", number: int = 7) -> None:
        event = self.root / "event.json"
        event.write_text(
            json.dumps({"pull_request": {"number": number, "head": {"repo": {"full_name": head_repo}}}})
        )
        self.env.update(GITHUB_EVENT_NAME="pull_request", GITHUB_EVENT_PATH=str(event))

    def report(self, name: str = "attest-report.json", **side: object) -> str:
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("{}")
        for suffix, content in side.items():
            Path(f"{path}{_SIDE_FILES[suffix]}").write_text(
                content if isinstance(content, str) else json.dumps(content)
            )
        return str(path)

    def run_action(
        self,
        *reports: str,
        policy: str = "",
        extra: dict[str, str] | None = None,
        author: str = "",
    ):
        argv = ["--report", "\n".join(reports), "--engine", _STUB]
        if author:
            argv += ["--comment-author", author]
        if policy:
            argv += ["--policy", policy]
        out = io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(out):
            code = _pa.main(argv, {**self.env, **(extra or {})})
        return code, out.getvalue()

    def exit_code_output(self) -> str:
        line = [x for x in (self.root / "output").read_text().splitlines() if x.startswith("exit_code=")]
        return line[-1].split("=", 1)[1]


class TestEscaping(unittest.TestCase):
    def test_data_escapes_percent_and_newlines(self) -> None:
        self.assertEqual(_pa._escape_data("50%\r\nnext\n"), "50%25%0D%0Anext%0A")

    def test_property_also_escapes_colon_and_comma(self) -> None:
        self.assertEqual(_pa._escape_property("a:b,c%"), "a%3Ab%2Cc%25")

    def test_annotation_cannot_start_a_second_command(self) -> None:
        row = {
            "status": "hard_fail",
            "type": "x",
            "assertion_id": "a,file=evil",
            "explanation": "boom\n::error::injected\r\n::set-output name=x::y",
            "file": "tests/t.py",
            "line": 3,
        }
        _, command = _pa.row_annotation(row, "tests/t.py")
        self.assertEqual(len(command.splitlines()), 1)
        self.assertNotIn("\n", command)
        self.assertIn("boom%0A::error::injected%0A", command)
        self.assertIn("title=Attest failure%3A a%2Cfile=evil (x)", command)

    def test_control_and_bidi_characters_are_stripped(self) -> None:
        row = {"status": "hard_fail", "explanation": "a\x1b[31mred‮txt\x00z"}
        _, command = _pa.row_annotation(row, None)
        self.assertTrue(command.endswith("::a[31mredtxtz"), command)

    def test_message_is_capped(self) -> None:
        _, command = _pa.row_annotation({"status": "hard_fail", "explanation": "x" * 5000}, None)
        self.assertLessEqual(len(command.split("::", 2)[2]), _pa.MAX_MESSAGE_CHARS)


class TestLevels(unittest.TestCase):
    def level(self, **row: object) -> str:
        return _pa.row_annotation(row, "t.py")[0]

    def test_hard_fail_and_replay_miss_are_errors(self) -> None:
        self.assertEqual(self.level(status="hard_fail"), "error")
        self.assertEqual(self.level(status="not_evaluated", reason="replay_miss"), "error")

    def test_soft_fail_and_not_evaluated_are_warnings(self) -> None:
        self.assertEqual(self.level(status="soft_fail"), "warning")
        self.assertEqual(self.level(status="not_evaluated", reason="simulation"), "warning")

    def test_file_and_line_are_properties_only_when_valid(self) -> None:
        _, with_line = _pa.row_annotation({"status": "hard_fail", "line": 9}, "tests/t.py")
        self.assertTrue(with_line.startswith("::error file=tests/t.py,line=9,title="))
        _, without = _pa.row_annotation({"status": "hard_fail", "line": 9}, None)
        self.assertTrue(without.startswith("::error title="))


class TestCaps(unittest.TestCase):
    def rows(self, n_errors: int, n_warnings: int) -> list:
        return [({"status": "hard_fail", "assertion_id": f"e{i}"}, "t.py") for i in range(n_errors)] + [
            ({"status": "soft_fail", "assertion_id": f"w{i}"}, "t.py") for i in range(n_warnings)
        ]

    def test_under_the_cap_every_row_is_emitted(self) -> None:
        out = _pa.build_annotations(self.rows(10, 9))
        self.assertEqual(len(out), 19)
        self.assertFalse(any("more" in c for c in out))

    def test_overflow_ends_each_level_with_one_summary_annotation(self) -> None:
        out = _pa.build_annotations(self.rows(25, 12))
        errors = [c for c in out if c.startswith("::error")]
        warnings = [c for c in out if c.startswith("::warning")]
        self.assertEqual((len(errors), len(warnings)), (10, 9))
        self.assertIn("16 more errors", errors[-1])
        self.assertIn("4 more warnings", warnings[-1])
        self.assertIn("e8", errors[-2])


class TestRootRelative(ActionCase):
    def test_report_in_subdirectory_prefixes_row_paths(self) -> None:
        (self.root / "py" / "tests").mkdir(parents=True)
        (self.root / "py" / "tests" / "test_a.py").write_text("")
        report = self.report("py/attest-report.json")
        self.assertEqual(_pa.root_relative("tests/test_a.py", report, self.root), "py/tests/test_a.py")

    def test_unprefixed_path_used_when_only_it_exists(self) -> None:
        (self.root / "tests").mkdir()
        (self.root / "tests" / "test_a.py").write_text("")
        report = self.report("py/attest-report.json")
        self.assertEqual(_pa.root_relative("tests/test_a.py", report, self.root), "tests/test_a.py")

    def test_report_at_root_leaves_path_unchanged(self) -> None:
        report = self.report()
        self.assertEqual(_pa.root_relative("tests/test_a.py", report, self.root), "tests/test_a.py")

    def test_unsafe_or_missing_paths_have_no_location(self) -> None:
        report = self.report()
        for bad in (None, "", "/etc/passwd", "../x.py", "a/../b.py", "a:b.py", "a\\b.py", 5):
            self.assertIsNone(_pa.root_relative(bad, report, self.root), bad)


class TestWorstExit(ActionCase):
    def exit_for(self, *codes: int | str, policy: bool = True) -> str:
        reports = [self.report(f"r{i}/attest-report.json", policy_exit=str(c)) for i, c in enumerate(codes)]
        code, _ = self.run_action(*reports, policy="attest.policy.yaml" if policy else "")
        self.assertEqual(code, 0)
        return self.exit_code_output()

    def test_blocking_beats_warning_beats_pass(self) -> None:
        self.assertEqual(self.exit_for(0, 2), "2")
        self.assertEqual(self.exit_for(2, 0, 1), "1")
        self.assertEqual(self.exit_for(0, 0), "0")

    def test_evaluator_error_outranks_a_block(self) -> None:
        self.assertEqual(self.exit_for(1, 127), "127")

    def test_no_policy_is_exit_zero_and_never_calls_policy_evaluate(self) -> None:
        self.assertEqual(self.exit_for(1, policy=False), "0")
        self.assertNotIn("policy", self.log.read_text())

    def test_signal_killed_evaluator_is_not_overwritten_by_a_later_pass(self) -> None:
        self.assertEqual(self.exit_for("kill", 0), "-9")

    def test_policy_result_of_the_worst_report_goes_to_the_summary(self) -> None:
        a = self.report("a/attest-report.json", policy_exit="0")
        b = self.report("b/attest-report.json", policy_exit="1")
        self.run_action(a, b, policy="p.yaml")
        self.assertIn(f"policy: {{\"report\": \"{b}\"}}", (self.root / "step-summary.md").read_text())

    def test_policy_result_goes_to_runner_temp_not_the_workspace(self) -> None:
        _, out = self.run_action(self.report(), policy="p.yaml")
        self.assertFalse((self.root / "policy-result.json").exists())
        self.assertEqual(len(list((self.root / "runner-temp").glob("attest-policy-*/policy-result.json"))), 1)
        self.assertNotIn('{"report"', out)  # the policy JSON is not echoed to the log


class TestSummaryComment(ActionCase):
    def test_first_run_creates_one_comment_with_marker_summary_and_run_link(self) -> None:
        code, out = self.run_action(self.report())

        self.assertEqual(code, 0, out)
        (comment,) = self.github.comments
        self.assertTrue(comment["body"].startswith(_pa.MARKER + "\n### attest: 1 passed"))
        self.assertIn("[Workflow run](https://github.com/acme/agent/actions/runs/77)", comment["body"])
        self.assertIn(("POST", "/repos/acme/agent/issues/7/comments"), self.github.calls)

    def test_second_run_updates_the_same_comment_in_place(self) -> None:
        report = self.report()
        self.run_action(report)
        first_id = self.github.comments[0]["id"]

        code, out = self.run_action(report, self.report("b/attest-report.json"))

        self.assertEqual(code, 0, out)
        self.assertEqual(len(self.github.comments), 1)
        self.assertIn("2 failed", self.github.comments[0]["body"])
        self.assertIn(("PATCH", f"/repos/acme/agent/issues/comments/{first_id}"), self.github.calls)
        self.assertEqual(sum(1 for m, _ in self.github.calls if m == "POST"), 1)

    def test_unrelated_comments_are_left_alone(self) -> None:
        self.github.comments.append({"id": 1, "body": "looks good", "user": {"login": "a"}})
        self.run_action(self.report())
        self.assertEqual(self.github.comments[0]["body"], "looks good")
        self.assertEqual(len(self.github.comments), 2)

    def test_planted_marker_comment_is_never_edited_even_when_editable(self) -> None:
        planted = {"id": 1, "body": f"{_pa.MARKER} planted", "user": {"login": "mallory"}}
        self.github.comments.append(planted)

        self.run_action(self.report())

        self.assertEqual(planted["body"], f"{_pa.MARKER} planted")
        self.assertEqual(len(self.github.comments), 2)
        self.assertFalse(any(m == "PATCH" for m, _ in self.github.calls))
        self.assertIn("### attest:", self.github.comments[1]["body"])

    def test_own_comment_is_found_among_planted_ones(self) -> None:
        self.github.comments.append(
            {"id": 1, "body": f"{_pa.MARKER} planted", "user": {"login": "mallory"}}
        )
        report = self.report()
        self.run_action(report)
        self.run_action(report)

        self.assertEqual(len(self.github.comments), 2)
        self.assertEqual(sum(1 for m, _ in self.github.calls if m == "POST"), 1)

    def test_personal_token_updates_only_comments_by_its_own_login(self) -> None:
        self.github.user_login = "octocat"
        report = self.report()
        self.run_action(report)
        self.run_action(report)

        self.assertEqual(len(self.github.comments), 1)
        self.assertEqual(self.github.comments[0]["user"]["login"], "octocat")

    def test_comment_list_is_read_past_the_first_page(self) -> None:
        mine = {"id": 900, "body": f"{_pa.MARKER}\nold", "user": {"login": _pa.BOT_LOGIN, "type": "Bot"}}
        self.github.comments.extend(
            {"id": i, "body": "x", "user": {"login": "a"}} for i in range(100)
        )
        self.github.comments.append(mine)
        self.github.paged = True

        self.run_action(self.report())

        self.assertIn("### attest:", mine["body"])
        self.assertEqual(len(self.github.comments), 101)

    def test_step_summary_is_written_alongside_the_comment(self) -> None:
        self.run_action(self.report())
        self.assertIn("### attest: 1 passed", (self.root / "step-summary.md").read_text())

    def test_fork_pull_request_gets_step_summary_and_no_api_calls(self) -> None:
        self.use_pull_request(head_repo="mallory/agent")

        code, out = self.run_action(self.report())

        self.assertEqual(code, 0, out)
        self.assertEqual(self.github.calls, [])
        self.assertIn("::notice::Attest summary comment not posted", out)
        self.assertIn("### attest:", (self.root / "step-summary.md").read_text())

    def test_push_event_posts_no_comment(self) -> None:
        code, _ = self.run_action(self.report(), extra={"GITHUB_EVENT_NAME": "push"})
        self.assertEqual(code, 0)
        self.assertEqual(self.github.calls, [])

    def test_token_that_cannot_write_warns_and_does_not_fail(self) -> None:
        self.github.force_status = 403

        code, out = self.run_action(self.report())

        self.assertEqual(code, 0, out)
        self.assertIn("::warning::Attest summary comment not posted (GitHub API returned 403)", out)
        self.assertIn("### attest:", (self.root / "step-summary.md").read_text())

    def test_unreachable_api_warns_and_does_not_fail(self) -> None:
        code, out = self.run_action(self.report(), extra={"GITHUB_API_URL": "http://127.0.0.1:1"})
        self.assertEqual(code, 0, out)
        self.assertIn("::warning::Attest summary comment not posted", out)

    def test_engine_never_sees_the_token(self) -> None:
        self.run_action(self.report(), policy="p.yaml", extra={"INPUT_GITHUB-TOKEN": _TOKEN})
        self.assertTrue(self.log.exists())
        self.assertFalse(Path(f"{self.log}.token-seen").exists())

    def test_token_is_never_printed(self) -> None:
        self.github.force_status = 403
        _, out = self.run_action(self.report())
        self.assertNotIn(_TOKEN, out)
        self.assertEqual(self.github.tokens, {f"Bearer {_TOKEN}"})

    def test_oversized_summary_is_cut_at_a_line_and_the_run_link_follows(self) -> None:
        big = self.root / "big-engine.py"
        big.write_text("#!/usr/bin/env python3\nfor i in range(20000):\n    print(f'line {i} ' + 'x' * 20)\n")
        big.chmod(0o755)
        body = _pa.render_summary(
            str(big), [self.report()], None, "https://github.com/a/b/actions/runs/1", self.env
        )
        self.assertLessEqual(len(body), _pa.MAX_COMMENT_CHARS)
        self.assertTrue(body.endswith("[Workflow run](https://github.com/a/b/actions/runs/1)"))
        cut = body.split("\n\n_Summary truncated")[0]
        self.assertRegex(cut.splitlines()[-1], r"^line \d+ x{20}$")


class TestCutSummary(unittest.TestCase):
    def test_short_text_is_unchanged(self) -> None:
        self.assertEqual(_pa.cut_summary("a\nb", 100), "a\nb")

    def test_cut_never_ends_inside_a_fence(self) -> None:
        text = "head\n\n```\n" + "code\n" * 50 + "```\ntail\n"
        out = _pa.cut_summary(text, 120)
        self.assertLessEqual(len(out), 120)
        self.assertEqual(out.count("```"), 0)
        self.assertTrue(out.startswith("head"))

    def test_closed_fence_before_the_cut_is_kept(self) -> None:
        text = "```\nx\n```\n" + "line\n" * 100
        out = _pa.cut_summary(text, 150)
        self.assertEqual(out.count("```"), 2)


class TestNeutralizing(ActionCase):
    def test_runner_command_prefix_is_broken_up_in_every_text_path(self) -> None:
        rows = [{"status": "hard_fail", "assertion_id": "##[stop-commands]tok", "explanation": "x ##[error]y"}]
        report = self.report(rows_json=rows, policy_exit="1")
        stderr_stub = self.root / "stderr-engine.py"
        stderr_stub.write_text(
            "#!/usr/bin/env python3\nimport sys\nsys.stderr.write('detail ##[stop-commands]z\\n')\nsys.exit(1)\n"
        )
        stderr_stub.chmod(0o755)
        _, out = self.run_action(report, policy="p.yaml")
        self.assertNotIn("##[", out)
        out2 = io.StringIO()
        with contextlib.redirect_stdout(out2), contextlib.redirect_stderr(out2):
            _pa.main(["--report", report, "--policy", "p.yaml", "--engine", str(stderr_stub)], self.env)
        self.assertNotIn("##[", out2.getvalue())
        self.assertIn("policy evaluate: detail # #[stop-commands]z", out2.getvalue())

    def test_control_pattern_is_ascii_source_and_covers_c1(self) -> None:
        self.assertTrue(_pa._CONTROL.pattern.isascii())
        for char in ("\x85", "\x9f", "\u202e", "\u200b", "\ufeff", "\x1b"):
            self.assertIsNotNone(_pa._CONTROL.search(char), repr(char))


class TestIdentity(ActionCase):
    def test_non_permission_user_error_skips_the_comment_with_a_warning(self) -> None:
        self.github.user_status = 500

        code, out = self.run_action(self.report())

        self.assertEqual(code, 0, out)
        self.assertIn("::warning::Attest summary comment not posted (GitHub API returned 500)", out)
        self.assertFalse(any(m == "POST" for m, _ in self.github.calls))
        self.assertIn("### attest:", (self.root / "step-summary.md").read_text())

    def test_bot_login_comment_of_type_user_is_not_ours(self) -> None:
        planted = {"id": 1, "body": f"{_pa.MARKER} x", "user": {"login": _pa.BOT_LOGIN, "type": "User"}}
        self.github.comments.append(planted)

        self.run_action(self.report())

        self.assertEqual(planted["body"], f"{_pa.MARKER} x")
        self.assertEqual(len(self.github.comments), 2)

    def test_dropped_connection_is_a_warning_not_a_failure(self) -> None:
        import http.client
        import unittest.mock

        with unittest.mock.patch.object(
            _pa, "_api", side_effect=http.client.RemoteDisconnected("closed")
        ):
            code, out = self.run_action(self.report())

        self.assertEqual(code, 0, out)
        self.assertIn("::warning::Attest summary comment not posted", out)


class TestCommentAuthor(ActionCase):
    def test_app_token_updates_only_the_named_apps_comment_without_asking_who_it_is(self) -> None:
        ours = {"id": 1, "body": f"{_pa.MARKER}\nold", "user": {"login": "my-app[bot]", "type": "Bot"}}
        actions_bot = {
            "id": 2,
            "body": f"{_pa.MARKER} other",
            "user": {"login": _pa.BOT_LOGIN, "type": "Bot"},
        }
        self.github.comments += [actions_bot, ours]

        code, out = self.run_action(self.report(), author="my-app[bot]")

        self.assertEqual(code, 0, out)
        self.assertIn("### attest:", ours["body"])
        self.assertEqual(actions_bot["body"], f"{_pa.MARKER} other")
        self.assertEqual(len(self.github.comments), 2)
        self.assertNotIn(("GET", "/user"), self.github.calls)

    def test_named_bot_author_still_requires_type_bot(self) -> None:
        planted = {"id": 1, "body": f"{_pa.MARKER} x", "user": {"login": "my-app[bot]", "type": "User"}}
        self.github.comments.append(planted)

        self.run_action(self.report(), author="my-app[bot]")

        self.assertEqual(planted["body"], f"{_pa.MARKER} x")
        self.assertEqual(len(self.github.comments), 2)


class TestMissingVersionFile(ActionCase):
    def test_explicit_engine_binary_does_not_need_the_version_file(self) -> None:
        with unittest.mock.patch.object(
            _pa, "action_version", side_effect=_pa.ActionError("no VERSION")
        ) as version:
            code, out = self.run_action(self.report())

        self.assertEqual(code, 0, out)
        version.assert_not_called()

    def test_engine_on_path_does_not_need_the_version_file(self) -> None:
        exe = self.root / "bin" / "attest-engine"
        exe.parent.mkdir()
        exe.write_text("#!/bin/sh\n")
        exe.chmod(0o755)
        with unittest.mock.patch.object(
            _pa, "action_version", side_effect=_pa.ActionError("no VERSION")
        ) as version:
            found = _pa.resolve_engine("attest-engine", "", {"PATH": str(exe.parent)})

        self.assertEqual(found, str(exe))
        version.assert_not_called()

    def test_cache_fallback_reads_the_version_file(self) -> None:
        with unittest.mock.patch.object(
            _pa, "action_version", side_effect=_pa.ActionError("no VERSION")
        ):
            with self.assertRaises(_pa.ActionError):
                _pa.resolve_engine("attest-engine", "", {"PATH": ""})


class TestResolveEngine(ActionCase):
    def make(self, path: Path) -> str:
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("#!/bin/sh\n")
        path.chmod(0o755)
        return str(path)

    def test_custom_binary_is_used_as_given(self) -> None:
        exe = self.make(self.root / "mine" / "engine")
        self.assertEqual(_pa.resolve_engine(exe, "1.0.0-rc.3", {"PATH": ""}), exe)

    def test_order_is_env_override_then_path_then_pinned_cache(self) -> None:
        override = self.make(self.root / "override" / "attest-engine")
        on_path = self.make(self.root / "bin" / "attest-engine")
        home = self.root / "home"
        cached = self.make(home / ".attest" / "bin" / "1.0.0-rc.3" / "attest-engine")
        self.make(home / ".attest" / "bin" / "1.0.0-rc.9" / "attest-engine")
        with unittest.mock.patch.dict(os.environ, {"HOME": str(home)}):
            both = {"PATH": str(self.root / "bin"), "ATTEST_ENGINE_PATH": override}
            self.assertEqual(_pa.resolve_engine("attest-engine", "1.0.0-rc.3", both), override)
            self.assertEqual(
                _pa.resolve_engine("attest-engine", "1.0.0-rc.3", {"PATH": str(self.root / "bin")}),
                on_path,
            )
            self.assertEqual(_pa.resolve_engine("attest-engine", "1.0.0-rc.3", {"PATH": ""}), cached)
            self.assertIsNone(_pa.resolve_engine("attest-engine", "1.0.0-rc.4", {"PATH": ""}))

    def test_action_version_reads_the_repository_version_file(self) -> None:
        expected = (Path(__file__).resolve().parents[3] / "VERSION").read_text().strip()
        self.assertEqual(_pa.action_version(), expected)


class TestAnnotationsEndToEnd(ActionCase):
    def test_rows_become_root_relative_workflow_commands(self) -> None:
        (self.root / "py" / "tests").mkdir(parents=True)
        (self.root / "py" / "tests" / "test_refund.py").write_text("")
        rows = [
            {
                "file": "tests/test_refund.py",
                "line": 12,
                "test_id": "tests/test_refund.py::test_refund",
                "assertion_id": "effect_refund",
                "type": "effect",
                "status": "hard_fail",
                "reason": "",
                "explanation": "effect violated: 2 refunds for O-1042 (expected 1)",
            },
            {
                "file": "tests/test_refund.py",
                "line": 30,
                "test_id": "tests/test_refund.py::test_replay",
                "assertion_id": "",
                "type": "",
                "status": "not_evaluated",
                "reason": "replay_miss",
                "explanation": "replay miss: system prompt changed",
            },
        ]
        report = self.report("py/attest-report.json", rows_json=rows)

        code, out = self.run_action(report)

        self.assertEqual(code, 0, out)
        self.assertIn(
            "::error file=py/tests/test_refund.py,line=12,title=Attest failure%3A effect_refund (effect)"
            "::effect violated: 2 refunds for O-1042 (expected 1)%0Atest: tests/test_refund.py::test_refund",
            out,
        )
        self.assertIn("::error file=py/tests/test_refund.py,line=30,title=Attest replay miss", out)

    def test_each_report_is_resolved_against_its_own_directory(self) -> None:
        row = [{"file": "t.py", "line": 1, "status": "hard_fail", "assertion_id": "a"}]
        a = self.report("pkg-a/attest-report.json", rows_json=row)
        b = self.report("pkg-b/attest-report.json", rows_json=row)

        _, out = self.run_action(a, b)

        self.assertIn("file=pkg-a/t.py", out)
        self.assertIn("file=pkg-b/t.py", out)

    def test_row_without_location_is_annotated_without_file(self) -> None:
        report = self.report(rows_json=[{"status": "soft_fail", "assertion_id": "a"}])
        _, out = self.run_action(report)
        self.assertIn("::warning title=Attest soft failure%3A a::", out)


class TestFailures(ActionCase):
    def test_missing_report_fails_before_the_engine_runs(self) -> None:
        code, out = self.run_action(str(self.root / "nope.json"))
        self.assertEqual(code, 1)
        self.assertIn("::error::Attest report not found", out)
        self.assertFalse(self.log.exists())

    def test_summary_bad_input_exit_2_fails_the_step(self) -> None:
        code, out = self.run_action(self.report(), extra={"STUB_SUMMARY_EXIT": "2"})
        self.assertEqual(code, 1)
        self.assertIn("report summary exited 2 (bad input)", out)

    def test_rows_failure_fails_the_step_after_the_summary_is_written(self) -> None:
        report = self.report(rows_exit="2")
        code, out = self.run_action(report)
        self.assertEqual(code, 1)
        self.assertIn("### attest:", (self.root / "step-summary.md").read_text())
        self.assertIn("report rows exited 2", out)

    def test_rows_that_are_not_an_array_fail_the_step(self) -> None:
        code, out = self.run_action(self.report(rows_json={"a": 1}))
        self.assertEqual(code, 1)
        self.assertIn("must print a JSON array", out)

    def test_missing_engine_with_policy_reports_127_and_fails(self) -> None:
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            code = _pa.main(
                ["--report", self.report(), "--policy", "p.yaml", "--engine", str(self.root / "absent")],
                self.env,
            )
        self.assertEqual(code, 1)
        self.assertEqual(self.exit_code_output(), "127")

    def test_multiple_report_paths_split_on_newlines_and_spaces(self) -> None:
        self.assertEqual(_pa.split_reports("a.json\n b.json  c.json\n"), ["a.json", "b.json", "c.json"])


_REPO_ROOT = Path(__file__).resolve().parents[3]
_POLICY = "layers:\n  0:\n    max_hard_fails: 0\nnot_evaluated: warn\n"


@unittest.skipUnless(shutil.which("go"), "go toolchain not on PATH; CI (ci-github-config.yml) has it")
class TestRealEngine(ActionCase):
    """The action script against the real attest-engine built from this checkout."""

    engine_dir: tempfile.TemporaryDirectory[str]
    engine: str

    @classmethod
    def setUpClass(cls) -> None:
        cls.engine_dir = tempfile.TemporaryDirectory()
        cls.engine = str(Path(cls.engine_dir.name) / "attest-engine")
        subprocess.run(
            ["go", "build", "-o", cls.engine, "./cmd/attest-engine"],
            cwd=_REPO_ROOT / "engine",
            check=True,
            capture_output=True,
        )

    @classmethod
    def tearDownClass(cls) -> None:
        cls.engine_dir.cleanup()

    def test_summary_annotations_and_policy_come_from_the_real_commands(self) -> None:
        fixture = _REPO_ROOT / "engine" / "internal" / "report" / "testdata" / "ci"
        report = self.root / "py" / "attest-report.json"
        report.parent.mkdir()
        report.write_text((fixture / "mixed.json").read_text())
        (self.root / "attest.policy.yaml").write_text(_POLICY)
        out = io.StringIO()

        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(out):
            code = _pa.main(
                ["--report", str(report), "--policy", "attest.policy.yaml", "--engine", self.engine],
                self.env,
            )

        log = out.getvalue()
        self.assertEqual(code, 0, log)
        self.assertEqual(self.exit_code_output(), "1")
        self.assertIn(
            "::error file=py/tests/test_refund.py,line=40,title=Attest failure%3A effect_refunds (effect)"
            "::effect violated: 2 refunds for O-1042 (expected 1)",
            log,
        )
        self.assertIn("title=Attest replay miss", log)
        (comment,) = self.github.comments
        self.assertTrue(comment["body"].startswith(f"{_pa.MARKER}\n### attest: 1 passed · 2 failed"))
        self.assertIn("**Policy:** fail (exit 1)", comment["body"])
        self.assertIn("`effect violated: 2 refunds for O-1042 (expected 1)`", comment["body"])
        self.assertIn("[Workflow run]", comment["body"])
        self.assertNotIn(_TOKEN, log)


if __name__ == "__main__":
    unittest.main()
