#!/usr/bin/env python3
"""Execute every runnable code block in the README and the guides.

Sources: README.md, sdks/python/README.md, sdks/typescript/**/README.md and
docs/guides/*.md. Fenced `python`, `ts`/`typescript` and `bash`/`sh` blocks are
executed offline, each in its own temporary directory:

* Python: a block that defines `test_*` functions runs under pytest with the
  attest plugin (`attest` fixture); any other block runs as a script. Both use
  the interpreter running this script, so start it with
  `uv run --project sdks/python python scripts/run_doc_snippets.py`.
* TypeScript: the block runs as a vitest test file resolved against the built
  `sdks/typescript` workspace packages (`pnpm build` first).
* Shell: every command word must be on the allowlist (`attest`, `attest-engine`,
  `pytest`, `export`) and the block may not contain backticks, `$(`, `<(` or `>(`; the
  block runs under `bash -e` with the engine and SDK on PATH. The allowlist is a
  lint against copy-paste mistakes, not a sandbox: it does not model every
  shell construct (variables, aliases, here-documents). The security boundary
  is the CI job, which has read-only repository permissions and no secrets.

Every environment runs with ATTEST_SIMULATION=1, the provider keys removed, no
engine download, and the HTTP(S) proxy variables pointing at an unreachable
address (Node is told to honor them). That is best-effort, not network
isolation: a client that ignores proxy variables still reaches the network.

A block that cannot run standalone must say so in an HTML comment on the line
above its fence (blank lines between are allowed):

    <!-- snippet: skip reason="needs a provider key" -->
    <!-- snippet: fragment reason="excerpt of a larger test file" -->
    <!-- snippet: continue -->
    <!-- snippet: setup=project -->
    <!-- snippet: exit=1 -->
    <!-- snippet: mode=script -->
    <!-- snippet: harness=simulation_scenario -->

`skip` and `fragment` need a reason and are counted per file in the report.
`continue` joins the block to the previous runnable block of the same file (it
runs as one program). `setup=NAME[,NAME...]` copies scripts/doc_snippet_fixtures/NAME/
into the working directory first and runs its setup.sh, if any. `exit=N` is the expected shell exit status
(default 0). `mode=script|pytest` overrides the detection that a block defining
`test_*` functions runs under pytest. `harness=NAME` (Python only) embeds the
block into scripts/doc_snippet_harness/NAME.py at its `# SNIPPET` line; the
harness supplies the reader's own objects (an offline `my_agent`) and calls
what the block defines. A Python script block that only defines functions or
classes runs nothing, so it needs a harness, a `test_*` function, or a
`fragment` marker. `setup=` and `harness=` names (setup takes a comma-separated list, applied in order) match `[A-Za-z0-9_-]+`. A directive with nothing to attach to, an unknown directive or
an unrecognised fence language is an error: nothing is skipped silently.

    python3 scripts/run_doc_snippets.py [--root DIR] [--engine PATH] [FILE ...]
"""

from __future__ import annotations

import argparse
import ast
import os
import re
import shlex
import shutil
import subprocess
import sys
import tempfile
from collections.abc import Sequence
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass, field
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
FIXTURES = Path(__file__).resolve().parent / "doc_snippet_fixtures"
HARNESSES = Path(__file__).resolve().parent / "doc_snippet_harness"
NAME_RE = re.compile(r"[A-Za-z0-9_-]+")
SHELL_FORBIDDEN = ("`", "$(", "<(", ">(")

SOURCE_GLOBS = [
    "README.md",
    "sdks/python/README.md",
    "sdks/typescript/README.md",
    "sdks/typescript/packages/*/README.md",
    "docs/guides/*.md",
]

PYTHON_LANGS = {"python", "py"}
TS_LANGS = {"ts", "typescript"}
SHELL_LANGS = {"bash", "sh", "shell", "zsh"}
DATA_LANGS = {
    "",
    "text",
    "txt",
    "json",
    "jsonc",
    "yaml",
    "yml",
    "toml",
    "ini",
    "diff",
    "md",
    "markdown",
    "html",
    "xml",
    "mermaid",
    "console",
    "proto",
    "dockerfile",
    "make",
}

SHELL_ALLOWLIST = {"attest", "attest-engine", "pytest", "export"}  # export: environment setup only
DIRECTIVE_KINDS = {"skip", "fragment", "continue"}
PROVIDER_KEYS = (
    "OPENAI_API_KEY",
    "ANTHROPIC_API_KEY",
    "GOOGLE_API_KEY",
    "GEMINI_API_KEY",
    "ATTEST_JUDGE_API_KEY",
    "ATTEST_JUDGE_PROVIDER",
    "ATTEST_JUDGE_MODEL",
    "ATTEST_JUDGE_BASE_URL",
)
TIMEOUT_S = 180

FENCE_RE = re.compile(
    r"^(?P<indent>\s*)(?P<fence>`{3,}|~{3,})\s*(?P<lang>[A-Za-z0-9_+-]*)\s*(?P<info>.*)$"
)
DIRECTIVE_RE = re.compile(r"^\s*<!--\s*snippet:\s*(?P<body>.*?)\s*-->\s*$")
TOKEN_RE = re.compile(r'(?P<key>[a-z]+)(?:=(?P<val>"[^"]*"|\S+))?')


@dataclass
class Block:
    file: str
    line: int  # 1-based line of the opening fence
    lang: str
    body: str
    directives: dict[str, str] = field(default_factory=dict)
    kind: str = "run"  # run | skip | fragment | data
    continues: bool = False


@dataclass
class Unit:
    """One program to run: a block plus the blocks that `continue` it."""

    blocks: list[Block]

    @property
    def head(self) -> Block:
        return self.blocks[0]

    @property
    def lang(self) -> str:
        return self.head.lang

    @property
    def body(self) -> str:
        return "\n".join(b.body for b in self.blocks)

    @property
    def where(self) -> str:
        first = f"{self.head.file}:{self.head.line}"
        if len(self.blocks) == 1:
            return first
        return first + " (+ " + ", ".join(str(b.line) for b in self.blocks[1:]) + ")"


@dataclass
class Result:
    unit: Unit
    ok: bool
    detail: str = ""


def parse_directive(body: str, where: str) -> dict[str, str]:
    out: dict[str, str] = {}
    pos = 0
    body = body.strip()
    while pos < len(body):
        m = TOKEN_RE.match(body, pos)
        if not m:
            raise SystemExit(f"{where}: cannot parse snippet directive {body!r}")
        key, val = m.group("key"), m.group("val")
        out[key] = val.strip('"') if val is not None else ""
        pos = m.end()
        while pos < len(body) and body[pos].isspace():
            pos += 1
    return out


def validate_directive(d: dict[str, str], where: str) -> str:
    """Return the block kind a directive set selects."""
    unknown = set(d) - DIRECTIVE_KINDS - {"reason", "setup", "exit", "mode", "harness"}
    if unknown:
        raise SystemExit(f"{where}: unknown snippet directive(s) {sorted(unknown)}")
    kinds = [k for k in ("skip", "fragment") if k in d]
    if len(kinds) > 1:
        raise SystemExit(f"{where}: a snippet is either skip or fragment, not both")
    if kinds and not d.get("reason"):
        raise SystemExit(
            f'{where}: `{kinds[0]}` needs a reason, e.g. <!-- snippet: {kinds[0]} reason="..." -->'
        )
    if "reason" in d and not kinds:
        raise SystemExit(f"{where}: `reason` only goes with `skip` or `fragment`")
    if d.get("mode", "script") not in ("script", "pytest"):
        raise SystemExit(f"{where}: mode= is script or pytest")
    for key in ("setup", "harness"):
        names = d[key].split(",") if key in d and key == "setup" else [d[key]] if key in d else []
        if not all(NAME_RE.fullmatch(n) for n in names):
            raise SystemExit(f"{where}: {key}= takes names matching [A-Za-z0-9_-]+")
    if "exit" in d and not d["exit"].isdigit():
        raise SystemExit(f"{where}: exit= needs an integer")
    return kinds[0] if kinds else "run"


def parse_blocks(path: Path, rel: str) -> list[Block]:
    lines = path.read_text().splitlines()
    blocks: list[Block] = []
    claimed: set[int] = set()  # directive lines attached to a fence
    i = 0
    while i < len(lines):
        m = FENCE_RE.match(lines[i])
        if not m:
            i += 1
            continue
        fence, indent, lang = m.group("fence"), m.group("indent"), m.group("lang").lower()
        start = i
        body: list[str] = []
        i += 1
        while i < len(lines) and not (
            lines[i].strip().startswith(fence) and lines[i].strip().strip(fence[0]) == ""
        ):
            line = lines[i]
            body.append(line[len(indent) :] if line.startswith(indent) else line.lstrip())
            i += 1
        if i >= len(lines):
            raise SystemExit(f"{rel}:{start + 1}: unterminated code fence")
        i += 1

        directives: dict[str, str] = {}
        j = start - 1
        while j >= 0 and not lines[j].strip():
            j -= 1
        while j >= 0 and (dm := DIRECTIVE_RE.match(lines[j])):
            directives.update(parse_directive(dm.group("body"), f"{rel}:{j + 1}"))
            claimed.add(j)
            j -= 1
        where = f"{rel}:{start + 1}"
        kind = validate_directive(directives, where)
        runnable = lang in PYTHON_LANGS | TS_LANGS | SHELL_LANGS
        if not runnable and lang not in DATA_LANGS:
            raise SystemExit(
                f"{where}: unrecognised fence language {lang!r}; add it to the runner or retag the block"
            )
        if not runnable:
            if directives:
                raise SystemExit(
                    f"{where}: snippet directive on a non-code ({lang or 'untagged'}) block"
                )
            kind = "data"
        blocks.append(
            Block(
                rel,
                start + 1,
                lang,
                "\n".join(body) + "\n",
                directives,
                kind,
                "continue" in directives,
            )
        )

    for idx, line in enumerate(lines):
        if DIRECTIVE_RE.match(line) and idx not in claimed:
            raise SystemExit(
                f"{rel}:{idx + 1}: snippet directive is not directly above a code fence"
            )
    return blocks


def build_units(blocks: Sequence[Block]) -> list[Unit]:
    units: list[Unit] = []
    for b in blocks:
        if b.kind != "run":
            continue
        if "harness" in b.directives and b.lang not in PYTHON_LANGS:
            raise SystemExit(f"{b.file}:{b.line}: harness= applies to Python blocks only")
        if b.continues:
            if not units or units[-1].lang != b.lang or units[-1].head.file != b.file:
                raise SystemExit(
                    f"{b.file}:{b.line}: `continue` needs a previous runnable {b.lang} block in the same file"
                )
            units[-1].blocks.append(b)
        else:
            units.append(Unit([b]))
    return units


UV_SHIM = """#!/bin/sh
# Snippets say `uv run attest ...`; the runner's interpreter already has the SDK, so run the command directly.
if [ "$1" != "run" ]; then
  echo "uv $1 is not available while running documentation snippets" >&2
  exit 2
fi
shift
exec "$@"
"""


def base_env(root: Path, engine: Path, home: Path) -> dict[str, str]:
    env = {k: v for k, v in os.environ.items() if k not in PROVIDER_KEYS}
    shim_dir = home / "shims"
    shim_dir.mkdir()
    shim = shim_dir / "uv"
    shim.write_text(UV_SHIM)
    shim.chmod(0o755)
    bindirs = [str(shim_dir), str(Path(sys.executable).parent), str(engine.parent)]
    env.update(
        PATH=os.pathsep.join(bindirs + [env.get("PATH", "")]),
        HOME=str(home),
        ATTEST_SIMULATION="1",
        ATTEST_ENGINE_PATH=str(engine),
        ATTEST_ENGINE_NO_DOWNLOAD="1",
        ATTEST_CACHE_DIR=str(home / "cache"),
        HTTP_PROXY="http://127.0.0.1:9",
        HTTPS_PROXY="http://127.0.0.1:9",
        http_proxy="http://127.0.0.1:9",
        https_proxy="http://127.0.0.1:9",
        NO_PROXY="127.0.0.1,localhost,::1",
        no_proxy="127.0.0.1,localhost,::1",
        NODE_USE_ENV_PROXY="1",
        PYTHONDONTWRITEBYTECODE="1",
    )
    return env


def run_cmd(cmd: list[str], cwd: Path, env: dict[str, str]) -> tuple[int, str]:
    try:
        proc = subprocess.run(
            cmd, cwd=cwd, env=env, capture_output=True, text=True, timeout=TIMEOUT_S, check=False
        )
    except subprocess.TimeoutExpired:
        return 124, f"timed out after {TIMEOUT_S}s"
    return proc.returncode, (proc.stdout + proc.stderr)


def check_shell_commands(body: str) -> str | None:
    """Reject any command word outside the allowlist; return an error string."""
    for marker in SHELL_FORBIDDEN:
        if marker in body:
            return f"{marker!r} is not allowed in a runnable shell snippet (command substitution)"
    text = re.sub(r"\\\n", " ", body)
    for raw in text.splitlines():
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        try:
            lexer = shlex.shlex(line, posix=True, punctuation_chars=True)
            lexer.whitespace_split = True
            lexer.commenters = "#"
            tokens = list(lexer)
        except ValueError as exc:
            return f"cannot parse shell line {line!r}: {exc}"
        expect_cmd = True
        after_uv = False
        for tok in tokens:
            if tok and all(c in "|&;()<>" for c in tok):
                expect_cmd = tok in ("|", "||", "&&", ";", "(")
                after_uv = False
                continue
            if expect_cmd and re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*=.*", tok):
                continue  # VAR=value prefix
            if not expect_cmd:
                continue
            if tok == "uv":
                after_uv = True  # the shim only accepts `uv run CMD`
                continue
            if after_uv:
                after_uv = False
                if tok == "run":
                    continue
                return f"`uv {tok}` is not allowed in a runnable shell snippet (only `uv run`)"
            if tok not in SHELL_ALLOWLIST:
                return f"command {tok!r} is not allowed in a runnable shell snippet (allowed: {', '.join(sorted(SHELL_ALLOWLIST))}, `uv run` of those)"
            expect_cmd = False
    return None


def apply_harness(name: str, body: str) -> str:
    path = HARNESSES / f"{name}.py"
    if not path.is_file():
        raise SystemExit(f"harness not found: {path}")
    template = path.read_text()
    if template.count("# SNIPPET\n") != 1:
        raise SystemExit(f"{path}: needs exactly one `# SNIPPET` line")
    return template.replace("# SNIPPET\n", body)


def python_defines_only(body: str) -> bool:
    """True when a script defines functions or classes but nothing in it ever runs them."""
    tree = ast.parse(body)
    defs = (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef)
    inert = (ast.Import, ast.ImportFrom, *defs)
    return any(isinstance(n, defs) for n in tree.body) and all(
        isinstance(n, inert) or (isinstance(n, ast.Expr) and isinstance(n.value, ast.Constant))
        for n in tree.body
    )


TS_DECLARATION_RE = re.compile(
    r"(import|export\s+(default\s+)?(async\s+)?(function|class|interface|type|enum)|"
    r"(async\s+)?function|class|interface|type|enum|declare)\b"
)


def typescript_defines_only(body: str) -> bool:
    """True when every top-level TS statement is an import or a declaration (nothing runs)."""
    starts = [
        ln
        for ln in body.splitlines()
        if ln and not ln[0].isspace() and ln[0] not in "}])/*"
    ]
    return any(not ln.startswith("import") for ln in starts) and all(
        TS_DECLARATION_RE.match(ln) for ln in starts
    )


def run_unit(unit: Unit, root: Path, engine: Path, ts_root: Path) -> Result:
    d = unit.head.directives
    with tempfile.TemporaryDirectory(prefix="attest-snippet-") as tmp:
        work = Path(tmp) / "work"
        work.mkdir()
        home = Path(tmp) / "home"
        home.mkdir()
        env = base_env(root, engine, home)
        for name in d.get("setup", "").split(",") if "setup" in d else []:
            fixture = FIXTURES / name
            if not fixture.is_dir():
                return Result(unit, False, f"setup fixture not found: {fixture}")
            shutil.copytree(fixture, work, dirs_exist_ok=True)
            setup_script = work / "setup.sh"
            if setup_script.is_file():
                code, out = run_cmd(["sh", "setup.sh"], work, env)
                setup_script.unlink()
                if code:
                    return Result(unit, False, f"setup {name}/setup.sh exited {code}\n{out}")
        body = unit.body
        if unit.lang in PYTHON_LANGS:
            if "harness" in d:
                body = apply_harness(d["harness"], body)
            is_test = (
                d["mode"] == "pytest"
                if "mode" in d
                else re.search(r"^(async\s+)?def test_|^class Test", body, re.M) is not None
            )
            if not is_test and python_defines_only(body):
                return Result(
                    unit,
                    False,
                    "block only defines functions or classes, so nothing runs: add a test_* function, "
                    "a harness=NAME that calls it, or mark it fragment",
                )
            if is_test:
                (work / "test_snippet.py").write_text(body)
                (work / "pytest.ini").write_text("[pytest]\nasyncio_mode = auto\n")
                code, out = run_cmd(
                    [
                        sys.executable,
                        "-m",
                        "pytest",
                        "-q",
                        "-p",
                        "no:cacheprovider",
                        "test_snippet.py",
                    ],
                    work,
                    env,
                )
            else:
                (work / "snippet.py").write_text(body)
                code, out = run_cmd([sys.executable, "snippet.py"], work, env)
            return Result(unit, code == 0, out if code else "")
        if unit.lang in SHELL_LANGS:
            problem = check_shell_commands(body)
            if problem:
                return Result(unit, False, problem)
            (work / "snippet.sh").write_text(body)
            code, out = run_cmd(["bash", "-e", "snippet.sh"], work, env)
            want = int(d.get("exit", "0"))
            return Result(
                unit, code == want, "" if code == want else f"exit {code}, expected {want}\n{out}"
            )
        return run_ts(unit, ts_root, env, work)


def run_ts(unit: Unit, ts_root: Path, env: dict[str, str], work: Path) -> Result:
    host = ts_root / "packages" / "vitest"
    if not (host / "node_modules" / "vitest").exists():
        return Result(
            unit,
            False,
            f"{host}/node_modules/vitest missing: run `pnpm install --frozen-lockfile && pnpm build` in sdks/typescript",
        )
    body = unit.body
    if not re.search(r"^\s*(it|test)\(", body, re.M):
        if typescript_defines_only(body):
            return Result(
                unit,
                False,
                "block only declares functions, classes or types, so nothing runs: "
                "add an it(...) test or mark it fragment",
            )
        # A script, not a test file: vitest needs one test to accept the file.
        body += '\nimport { test as __snippetRan } from "vitest";\n__snippetRan("snippet ran", () => {});\n'
    # Resolve @attest-ai/* and vitest from the workspace package that depends on them.
    scratch = Path(tempfile.mkdtemp(prefix=".doc-snippet-", dir=host))
    try:
        for item in work.iterdir():
            shutil.move(str(item), scratch / item.name)
        (scratch / "snippet.test.ts").write_text(body)
        (scratch / "vitest.config.ts").write_text(
            'import { defineConfig } from "vitest/config";\n'
            "export default defineConfig({ test: { include: ['snippet.test.ts'], root: __dirname } });\n"
        )
        code, out = run_cmd(
            [
                "node",
                str(host / "node_modules" / "vitest" / "vitest.mjs"),
                "run",
                "--config",
                str(scratch / "vitest.config.ts"),
            ],
            scratch,
            env,
        )
        return Result(unit, code == 0, out if code else "")
    finally:
        shutil.rmtree(scratch, ignore_errors=True)


def expect_chain_methods() -> set[str]:
    # `.world(snapshot)` hands the chain to WorldChecks, whose methods return to the chain.
    from attest.expect import ExpectChain, WorldChecks

    return {
        name for cls in (ExpectChain, WorldChecks) for name in dir(cls) if not name.startswith("_")
    }


def unknown_chain_methods(tree: ast.AST, known: set[str]) -> list[tuple[int, str]]:
    """Attribute names used on an `expect(...)` chain that ExpectChain does not define."""

    def rooted_in_expect(node: ast.expr) -> bool:
        cur: ast.expr = node
        while isinstance(cur, (ast.Attribute, ast.Call)):
            if isinstance(cur, ast.Call):
                if isinstance(cur.func, ast.Name) and cur.func.id == "expect":
                    return True
                cur = cur.func
            else:
                cur = cur.value
        return False

    return [
        (node.lineno, node.attr)
        for node in ast.walk(tree)
        if isinstance(node, ast.Attribute)
        and node.attr not in known
        and rooted_in_expect(node.value)
    ]


def check_unexecuted(blocks: Sequence[Block]) -> list[Result]:
    """Python that is not executed still has to parse and may only call real `expect(...)` methods."""
    known = expect_chain_methods()
    bad: list[Result] = []
    for b in blocks:
        if b.kind not in ("fragment", "skip") or b.lang not in PYTHON_LANGS:
            continue
        try:
            tree = ast.parse(b.body)
        except SyntaxError as exc:
            bad.append(
                Result(
                    Unit([b]), False, f"{b.kind} block does not parse: line {exc.lineno}: {exc.msg}"
                )
            )
            continue
        for lineno, attr in unknown_chain_methods(tree, known):
            bad.append(
                Result(
                    Unit([b]), False, f"line {b.line + lineno}: ExpectChain has no method {attr!r}"
                )
            )
    return bad


def collect(root: Path, files: Sequence[str]) -> list[str]:
    if files:
        return list(files)
    found: list[str] = []
    for pattern in SOURCE_GLOBS:
        found += sorted(str(p.relative_to(root)) for p in root.glob(pattern))
    return found


def tail(text: str, n: int = 25) -> str:
    lines = text.rstrip().splitlines()
    return "\n".join(("    " + ln) for ln in lines[-n:])


def main(argv: list[str]) -> int:
    ap = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    ap.add_argument(
        "files", nargs="*", help="markdown files, relative to --root (default: all sources)"
    )
    ap.add_argument(
        "--root", type=Path, default=ROOT, help="repository root (default: this repository)"
    )
    ap.add_argument(
        "--engine",
        type=Path,
        help="attest-engine binary (default: $ATTEST_ENGINE_PATH or bin/attest-engine)",
    )
    ap.add_argument("--jobs", type=int, default=4, help="snippets to run in parallel")
    args = ap.parse_args(argv)

    root: Path = args.root.resolve()
    engine = (
        args.engine or Path(os.environ.get("ATTEST_ENGINE_PATH") or root / "bin" / "attest-engine")
    ).resolve()
    if not engine.is_file():
        raise SystemExit(f"engine binary not found: {engine} (run `make engine`, or pass --engine)")
    ts_root = root / "sdks" / "typescript"

    rels = collect(root, args.files)
    all_blocks: dict[str, list[Block]] = {rel: parse_blocks(root / rel, rel) for rel in rels}
    units = [u for rel in rels for u in build_units(all_blocks[rel])]

    with ThreadPoolExecutor(max_workers=args.jobs) as pool:
        results = list(pool.map(lambda u: run_unit(u, root, engine, ts_root), units))

    results += check_unexecuted([b for rel in rels for b in all_blocks[rel]])
    failures = [r for r in results if not r.ok]
    print(f"{'file':44} {'ran':>4} {'pass':>4} {'fail':>4} {'skip':>4} {'frag':>4}")
    totals = [0, 0, 0, 0, 0]
    for rel in rels:
        blocks = all_blocks[rel]
        mine = [r for r in results if r.unit.head.file == rel and r.unit.head.kind == "run"]
        row = [
            len(mine),
            sum(r.ok for r in mine),
            sum(not r.ok for r in results if r.unit.head.file == rel),
            sum(b.kind == "skip" for b in blocks),
            sum(b.kind == "fragment" for b in blocks),
        ]
        totals = [a + b for a, b in zip(totals, row)]
        print(f"{rel:44} {row[0]:>4} {row[1]:>4} {row[2]:>4} {row[3]:>4} {row[4]:>4}")
    print(
        f"{'total':44} {totals[0]:>4} {totals[1]:>4} {totals[2]:>4} {totals[3]:>4} {totals[4]:>4}"
    )

    marked = [b for rel in rels for b in all_blocks[rel] if b.kind in ("skip", "fragment")]
    if marked:
        print("\nnot executed (reason required):")
        for b in marked:
            print(f"  {b.file}:{b.line} {b.kind}: {b.directives['reason']}")

    if failures:
        print("\nFAILED snippets:", file=sys.stderr)
        for r in failures:
            print(f"  {r.unit.where} [{r.unit.lang}]\n{tail(r.detail)}", file=sys.stderr)
        return 1
    print(f"\nsnippets ok: {totals[1]} executed, {totals[3]} skipped, {totals[4]} fragments")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
