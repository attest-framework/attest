"""scripts/check-action-pins.py: what counts as a pinned `uses:` and the remote tag check."""

from __future__ import annotations

import importlib.util
import os
import subprocess
import sys
from pathlib import Path
from types import ModuleType

import pytest

_REPO = Path(__file__).resolve().parents[3]
_SCRIPT = Path(os.environ.get("ATTEST_PIN_CHECKER", _REPO / "scripts" / "check-action-pins.py"))
_SHA = "d23441a48e516b6c34aea4fa41551a30e30af803"
_DIGEST = "a" * 64


def _load() -> ModuleType:
    spec = importlib.util.spec_from_file_location("check_action_pins", _SCRIPT)
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


checker = _load()


def _problems(tmp_path: Path, text: str) -> list[str]:
    workflow = tmp_path / "w.yml"
    workflow.write_text(text)
    return checker.violations(workflow)


@pytest.mark.parametrize(
    "line",
    [
        f"      - uses: actions/checkout@{_SHA} # v6.1.0",
        f"      - uses: 'actions/checkout@{_SHA}' # v6.1.0",
        f'      - uses: "actions/checkout@{_SHA}" # v6.1.0',
        f"      - {{ uses: actions/checkout@{_SHA}, with: {{ ref: x }} }} # v6.1.0",
        f'      - {{ "name": c, uses: "actions/checkout@{_SHA}" }} # v6.1.0',
        f"    uses: org/repo/.github/workflows/r.yml@{_SHA} # v1.2.3",
        f"      - uses: docker://alpine@sha256:{_DIGEST}",
        "      - uses: ./.github/actions/local",
        "      - uses: ./",
        "      # - uses: actions/checkout@v6",
    ],
)
def test_violations_accepts_pinned_local_and_commented_out_references(
    tmp_path: Path, line: str
) -> None:
    assert _problems(tmp_path, f"jobs:\n  a:\n    steps:\n{line}\n") == []


@pytest.mark.parametrize(
    ("line", "fragment"),
    [
        ("      - uses: actions/checkout@v6", "40-hex"),
        ("      - uses: actions/checkout@main # main", "40-hex"),
        ("      - uses: actions/checkout", "40-hex"),
        (f"      - uses: actions/checkout@{_SHA[:39]}", "40-hex"),
        (f"      - uses: actions/checkout@{_SHA.upper()} # v6", "40-hex"),
        ("      - { uses: actions/checkout@v6 }", "40-hex"),
        ("      - { name: c, uses: actions/checkout@v6, with: { ref: x } }", "40-hex"),
        ('      - { uses: "actions/checkout@v6" }', "40-hex"),
        ("      - {uses: 'actions/checkout@v6'}", "40-hex"),
        ("    uses: org/repo/.github/workflows/r.yml@v1", "40-hex"),
        (f"      - uses: actions/checkout@{_SHA}", "no version comment"),
        (f"      - {{ uses: actions/checkout@{_SHA} }}", "no version comment"),
        ("      - uses: docker://alpine:3.20", "sha256"),
        ("      - uses: docker://alpine@sha256:not-a-digest", "sha256"),
        (f"      - uses: docker://alpine@sha256:{_DIGEST[:63]}", "sha256"),
        (f"      - uses: docker://alpine@sha256:{'g' * 64}", "sha256"),
    ],
)
def test_violations_rejects_unpinned_references(tmp_path: Path, line: str, fragment: str) -> None:
    problems = _problems(tmp_path, f"jobs:\n  a:\n    steps:\n{line}\n")

    assert len(problems) == 1
    assert fragment in problems[0]


def test_violations_finds_every_flow_style_step_on_one_line(tmp_path: Path) -> None:
    text = f"steps: [{{ uses: a/b@v1 }}, {{ uses: c/d@{_SHA} }}, {{ uses: e/f@v2 }}] # v1\n"

    assert len(_problems(tmp_path, text)) == 2


def test_pins_reads_repo_sha_and_tag_from_flow_and_block_style(tmp_path: Path) -> None:
    workflow = tmp_path / "w.yml"
    workflow.write_text(
        f"- uses: actions/checkout@{_SHA} # v6.1.0\n"
        f"- {{ uses: org/repo/sub/dir@{_SHA}, with: {{ a: b }} }} # v1.0.1 extra words\n"
        "- uses: ./local\n"
    )

    found = checker.pins(workflow)

    assert [(p.repo, p.sha, p.tag) for p in found] == [
        ("actions/checkout", _SHA, "v6.1.0"),
        ("org/repo", _SHA, "v1.0.1"),
    ]


def test_remote_violations_requires_the_pinned_sha_to_equal_the_peeled_tag_commit(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    other = "b" * 40
    monkeypatch.setattr(checker, "_peeled_commit", lambda _repo, _tag: _SHA)
    good = checker.Pin("w.yml:1", "actions/checkout", _SHA, "v6.1.0")
    impostor = checker.Pin("w.yml:2", "actions/checkout", other, "v6.1.0")

    assert checker.remote_violations([good]) == []
    messages = checker.remote_violations([impostor])
    assert len(messages) == 1
    assert "w.yml:2" in messages[0] and other[:12] in messages[0] and _SHA[:12] in messages[0]


def test_remote_violations_flags_a_missing_tag(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(checker, "_peeled_commit", lambda _repo, _tag: None)
    pin = checker.Pin("w.yml:1", "actions/checkout", _SHA, "v99.0.0")

    assert "no tag v99.0.0" in checker.remote_violations([pin])[0]


def test_remote_violations_queries_each_repo_and_tag_once(monkeypatch: pytest.MonkeyPatch) -> None:
    calls: list[tuple[str, str]] = []

    def peeled(repo: str, tag: str) -> str:
        calls.append((repo, tag))
        return _SHA

    monkeypatch.setattr(checker, "_peeled_commit", peeled)
    pin = checker.Pin("w.yml:1", "actions/checkout", _SHA, "v6.1.0")

    checker.remote_violations([pin, pin])

    assert calls == [("actions/checkout", "v6.1.0")]


def test_main_with_verify_remote_fails_on_a_wrong_sha(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]
) -> None:
    workflow = tmp_path / "w.yml"
    workflow.write_text(f"- uses: actions/checkout@{'c' * 40} # v6.1.0\n")
    monkeypatch.setattr(checker, "_peeled_commit", lambda _repo, _tag: _SHA)

    assert checker.main(["--verify-remote", str(workflow)]) == 1
    assert "do not match their tag" in capsys.readouterr().err
    assert checker.main([str(workflow)]) == 0


def test_remote_violations_rejects_a_bare_major_comment_without_querying(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    def peeled(_repo: str, _tag: str) -> str:
        raise AssertionError("a moving tag must not be looked up")

    monkeypatch.setattr(checker, "_peeled_commit", peeled)
    pin = checker.Pin("w.yml:3", "actions/checkout", _SHA, "v7")

    messages = checker.remote_violations([pin])

    assert len(messages) == 1
    assert "w.yml:3" in messages[0] and "not a full vX.Y.Z tag" in messages[0]


def test_peeled_commit_disables_prompts_and_credential_helpers_and_sets_a_timeout(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    seen: dict[str, object] = {}

    def run(command: list[str], **kwargs: object) -> subprocess.CompletedProcess[str]:
        seen["command"], seen["kwargs"] = command, kwargs
        return subprocess.CompletedProcess(command, 0, stdout="", stderr="")

    monkeypatch.setattr(subprocess, "run", run)

    assert checker._peeled_commit("actions/checkout", "v6.1.0") is None

    kwargs = seen["kwargs"]
    assert isinstance(kwargs, dict)
    assert kwargs["env"]["GIT_TERMINAL_PROMPT"] == "0"
    assert kwargs["timeout"] == 30
    assert seen["command"][:3] == ["git", "-c", "credential.helper="]


def test_peeled_commit_turns_a_timeout_into_a_clear_error(monkeypatch: pytest.MonkeyPatch) -> None:
    def run(command: list[str], **_kwargs: object) -> subprocess.CompletedProcess[str]:
        raise subprocess.TimeoutExpired(command, 30)

    monkeypatch.setattr(subprocess, "run", run)

    with pytest.raises(RuntimeError, match="timed out after 30s"):
        checker._peeled_commit("actions/checkout", "v6.1.0")


def test_main_reports_a_remote_lookup_failure_as_exit_2(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]
) -> None:
    workflow = tmp_path / "w.yml"
    workflow.write_text(f"- uses: actions/checkout@{_SHA} # v6.1.0\n")

    def boom(_repo: str, _tag: str) -> str:
        raise RuntimeError("git ls-remote actions/checkout timed out after 30s")

    monkeypatch.setattr(checker, "_peeled_commit", boom)

    assert checker.main(["--verify-remote", str(workflow)]) == 2
    assert "timed out after 30s" in capsys.readouterr().err


@pytest.mark.parametrize(
    "text",
    [
        "steps:\n  - uses:\n      actions/checkout@v4\n",
        "steps:\n  - uses:   # the action\n\n      actions/checkout@v4\n",
        'steps:\n  - "uses": actions/checkout@v4\n',
        "steps:\n  - 'uses': actions/checkout@v4\n",
        'steps:\n  - { "uses": actions/checkout@v4 }\n',
        'steps:\n  - "uses":\n      "actions/checkout@v4"\n',
    ],
)
def test_violations_finds_next_line_values_and_quoted_keys(tmp_path: Path, text: str) -> None:
    assert len(_problems(tmp_path, text)) == 1


def test_violations_reports_the_value_line_for_a_next_line_reference(tmp_path: Path) -> None:
    problems = _problems(tmp_path, "steps:\n  - uses:\n      actions/checkout@v4\n")

    assert ":3:" in problems[0]


def test_pins_reads_a_next_line_value_with_its_comment(tmp_path: Path) -> None:
    workflow = tmp_path / "w.yml"
    workflow.write_text(
        f"- uses:\n    actions/checkout@{_SHA} # v6.1.0\n- 'uses': a/b@{_SHA} # v1.2.3\n"
    )

    found = checker.pins(workflow)

    assert [(p.repo, p.tag) for p in found] == [("actions/checkout", "v6.1.0"), ("a/b", "v1.2.3")]


def test_violations_accepts_pinned_next_line_and_quoted_key_forms(tmp_path: Path) -> None:
    text = (
        f"- uses:\n    actions/checkout@{_SHA} # v6.1.0\n"
        f"- 'uses': actions/checkout@{_SHA} # v6.1.0\n"
        f'- "uses":\n    "actions/checkout@{_SHA}" # v6.1.0\n'
    )

    assert _problems(tmp_path, text) == []
