"""scripts/run_doc_snippets.py: snippets execute, markers are enforced, a wrong method fails."""

from __future__ import annotations

import importlib.util
import sys
from pathlib import Path
from types import ModuleType

import pytest

_SCRIPT = Path(__file__).resolve().parents[3] / "scripts" / "run_doc_snippets.py"

OK_SNIPPET = """\
```python
from attest import TraceBuilder, expect
from attest.agent import AgentResult

trace = TraceBuilder("a").set_output(message="refund done").build()
chain = expect(AgentResult(trace=trace)).output_contains("refund")
assert len(chain.assertions) == 1
```
"""


def _load() -> ModuleType:
    spec = importlib.util.spec_from_file_location("run_doc_snippets", _SCRIPT)
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


runner = _load()


@pytest.fixture
def root(tmp_path: Path) -> Path:
    (tmp_path / "bin").mkdir()
    (tmp_path / "bin" / "attest-engine").write_text(
        "#!/bin/sh\n"
    )  # the script only checks it exists
    (tmp_path / "docs" / "guides").mkdir(parents=True)
    return tmp_path


def _run(
    root: Path, name: str, text: str, capsys: pytest.CaptureFixture[str]
) -> tuple[int, str, str]:
    (root / name).write_text(text)
    code = runner.main(["--root", str(root), "--engine", str(root / "bin" / "attest-engine"), name])
    out = capsys.readouterr()
    return code, out.out, out.err


def test_run_valid_python_snippet_passes(root: Path, capsys: pytest.CaptureFixture[str]) -> None:
    code, out, _ = _run(root, "README.md", OK_SNIPPET, capsys)

    assert code == 0
    assert "snippets ok: 1 executed, 0 skipped, 0 fragments" in out


def test_run_planted_wrong_method_fails_naming_file_and_line(
    root: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    planted = OK_SNIPPET.replace("output_contains", "output_containz")

    code, _, err = _run(root, "README.md", "Intro\n\n" + planted, capsys)

    assert code == 1
    assert "README.md:3 [python]" in err
    assert "output_containz" in err


def test_run_planted_wrong_method_in_fragment_fails(
    root: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    text = (
        '<!-- snippet: fragment reason="needs the real agent result" -->\n'
        "```python\n"
        'expect(result).output_containz("refund")\n'
        "```\n"
    )

    code, _, err = _run(root, "README.md", text, capsys)

    assert code == 1
    assert "README.md:2" in err
    assert "ExpectChain has no method 'output_containz'" in err


def test_run_fragment_with_real_method_is_counted_not_failed(
    root: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    text = (
        '<!-- snippet: fragment reason="needs the real agent result" -->\n'
        "```python\n"
        'expect(result).output_contains("refund")\n'
        "```\n"
    )

    code, out, _ = _run(root, "README.md", text, capsys)

    assert code == 0
    assert "README.md:2 fragment: needs the real agent result" in out
    assert "snippets ok: 0 executed, 0 skipped, 1 fragments" in out


def test_run_skip_is_listed_with_its_reason(root: Path, capsys: pytest.CaptureFixture[str]) -> None:
    text = '<!-- snippet: skip reason="needs a provider key" -->\n```bash\nuv add openai\n```\n'

    code, out, _ = _run(root, "docs/guides/g.md", text, capsys)

    assert code == 0
    assert "docs/guides/g.md:2 skip: needs a provider key" in out


def test_run_unmarked_unrunnable_snippet_fails(
    root: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    code, _, err = _run(root, "README.md", "```python\nundefined_name.run()\n```\n", capsys)

    assert code == 1
    assert "README.md:1 [python]" in err
    assert "NameError" in err


def test_run_shell_command_outside_allowlist_fails(
    root: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    code, _, err = _run(root, "README.md", "```bash\nrm -rf /tmp/x\n```\n", capsys)

    assert code == 1
    assert "'rm' is not allowed" in err


def test_run_uv_add_in_unmarked_shell_snippet_fails(
    root: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    code, _, err = _run(root, "README.md", "```bash\nuv add attest-ai\n```\n", capsys)

    assert code == 1
    assert "`uv add` is not allowed" in err


def test_run_shell_exit_status_must_match(root: Path, capsys: pytest.CaptureFixture[str]) -> None:
    text = "<!-- snippet: exit=3 -->\n```bash\nattest-engine --no-such-flag\n```\n"

    code, _, err = _run(root, "README.md", text, capsys)

    assert code == 1
    assert "expected 3" in err


def test_run_continue_joins_blocks_into_one_program(
    root: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    text = (
        "```python\nvalue = 41\n```\n\n"
        "<!-- snippet: continue -->\n```python\nassert value + 1 == 42\n```\n"
    )

    code, out, _ = _run(root, "README.md", text, capsys)

    assert code == 0
    assert "snippets ok: 1 executed" in out


def test_run_python_runs_with_simulation_and_without_provider_keys(
    root: Path, capsys: pytest.CaptureFixture[str], monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setenv("OPENAI_API_KEY", "sk-not-for-snippets")
    text = (
        "```python\nimport os\n"
        'assert os.environ["ATTEST_SIMULATION"] == "1"\n'
        'assert "OPENAI_API_KEY" not in os.environ\n```\n'
    )

    code, _, err = _run(root, "README.md", text, capsys)

    assert code == 0, err


@pytest.mark.parametrize(
    ("directive", "message"),
    [
        ("<!-- snippet: skip -->", "needs a reason"),
        ('<!-- snippet: fragment reason="" -->', "needs a reason"),
        ('<!-- snippet: skip reason="x" fragment -->', "either skip or fragment"),
        ("<!-- snippet: frobnicate -->", "unknown snippet directive"),
        ('<!-- snippet: reason="x" -->', "only goes with"),
    ],
)
def test_run_malformed_directive_is_an_error(
    root: Path, capsys: pytest.CaptureFixture[str], directive: str, message: str
) -> None:
    with pytest.raises(SystemExit, match=message):
        _run(root, "README.md", f"{directive}\n```python\npass\n```\n", capsys)


def test_run_directive_without_fence_is_an_error(
    root: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    with pytest.raises(SystemExit, match="not directly above a code fence"):
        _run(root, "README.md", '<!-- snippet: skip reason="x" -->\n\nplain text\n', capsys)


def test_run_unrecognised_fence_language_is_an_error(
    root: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    with pytest.raises(SystemExit, match="unrecognised fence language 'ruby'"):
        _run(root, "README.md", "```ruby\nputs 1\n```\n", capsys)


def test_run_continue_without_predecessor_is_an_error(
    root: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    with pytest.raises(SystemExit, match="`continue` needs a previous runnable"):
        _run(root, "README.md", "<!-- snippet: continue -->\n```python\npass\n```\n", capsys)


def test_run_data_blocks_are_ignored(root: Path, capsys: pytest.CaptureFixture[str]) -> None:
    code, out, _ = _run(
        root, "README.md", '```json\n{"a": 1}\n```\n\n```text\nplain\n```\n', capsys
    )

    assert code == 0
    assert "snippets ok: 0 executed, 0 skipped, 0 fragments" in out


def test_run_repository_sources_have_no_unmarked_directives() -> None:
    root = _SCRIPT.parent.parent
    for rel in runner.collect(root, []):
        runner.parse_blocks(
            root / rel, rel
        )  # raises SystemExit on a malformed or orphaned directive
