"""scripts/check-action-pins.py: what counts as a pinned `uses:` and the remote tag check."""

from __future__ import annotations

import importlib.util
import os
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
    pin = checker.Pin("w.yml:1", "actions/checkout", _SHA, "v99")

    assert "no tag v99" in checker.remote_violations([pin])[0]


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
