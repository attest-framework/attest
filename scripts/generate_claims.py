#!/usr/bin/env python3
"""Render the generated claim regions of the README and guides.

The facts come from `attest-engine capabilities --json`. Each target file holds
regions between `<!-- claims:NAME:start -->` and `<!-- claims:NAME:end -->`
markers; this script rewrites them. `--check` rewrites nothing and exits 1 when
a region differs from what the engine reports, a marker is missing or
duplicated, or a file outside a generated region repeats a retired claim.

    python3 scripts/generate_claims.py            # rewrite the regions
    python3 scripts/generate_claims.py --check    # exit 1 on drift
    python3 scripts/generate_claims.py --engine PATH [--check]

The engine binary is --engine, else $ATTEST_ENGINE_PATH, else bin/attest-engine.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
from collections.abc import Callable
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parent.parent

# Generated regions per file. Every listed region must appear exactly once.
TARGETS: dict[str, list[str]] = {
    "README.md": ["layers", "adapters", "features"],
    "docs/guides/framework-adapters.md": ["adapter-tiers"],
}

# Files scanned for retired claims outside the generated regions.
SCAN_GLOBS = ["README.md", "CONTRIBUTING.md", "docs/guides/*.md"]

# Claims that are false or retired. Each is a (pattern, why) pair.
RETIRED_CLAIMS: list[tuple[re.Pattern[str], str]] = [
    (re.compile(r"\bGo SDK\b"), "there is no Go SDK"),
    (re.compile(r"\bsdks/go\b"), "there is no sdks/go directory"),
    (re.compile(r"\b(?:8|eight)[- ]layers?\b", re.I), "public docs describe six evaluated layers plus plugins"),
    (re.compile(r"\b(?:layers?|L)\s?[78]\b(?!\d)", re.I), "layers 7 and 8 are not public layers; plugins are described as plugins"),
    (re.compile(r"\b70\s?%"), "the 70% figure has no source"),
    (re.compile(r"decouples? SDK and engine release", re.I), "the SDK requires the exact engine version"),
]


def marker(name: str, edge: str) -> str:
    return f"<!-- claims:{name}:{edge} -->"


def load_capabilities(engine: Path) -> dict[str, Any]:
    if not engine.is_file():
        raise SystemExit(f"engine binary not found: {engine} (run `make engine`, or pass --engine)")
    proc = subprocess.run(
        [str(engine), "capabilities", "--json"], capture_output=True, text=True, check=False
    )
    if proc.returncode != 0:
        raise SystemExit(f"{engine} capabilities --json exited {proc.returncode}: {proc.stderr.strip()}")
    doc: dict[str, Any] = json.loads(proc.stdout)
    return doc


def tick(items: list[str]) -> str:
    return ", ".join(f"`{i}`" for i in items)


COST_LABELS = {
    "free": "free",
    "provider_dependent": "depends on provider",
    "plugin_defined": "plugin-defined",
}


def render_layers(doc: dict[str, Any]) -> str:
    rows = [
        "| Layer | Name | Checks | Assertion types | Cost | Deterministic |",
        "| --- | --- | --- | --- | --- | --- |",
    ]
    for layer in doc["layers"]:
        label = "plugins" if layer["name"] == "Plugins" else str(layer["id"])
        rows.append(
            f"| {label} | {layer['name']} | {layer['description']} | "
            f"{tick(layer['assertion_types'])} | {COST_LABELS[layer['cost']]} | "
            f"{'yes' if layer['deterministic'] else 'no'} |"
        )
    return "\n".join(rows)


def adapter_line(label: str, adapters: list[dict[str, str]]) -> str:
    names = ", ".join(f"{a['name']} ({a['maturity']})" for a in adapters)
    return f"- **{len(adapters)} {label} adapters** — {names}"


def render_adapters(doc: dict[str, Any]) -> str:
    return "\n".join(
        [
            adapter_line("Python", doc["adapters"]["python"]),
            adapter_line("TypeScript", doc["adapters"]["typescript"]),
        ]
    )


def render_features(doc: dict[str, Any]) -> str:
    rows = ["| Feature | Maturity |", "| --- | --- |"]
    rows += [f"| {f['name']} | {f['maturity']} |" for f in doc["features"]]
    defs = doc["maturity_definitions"]
    rows.append("")
    rows += [f"- **{tier}**: {defs[tier]}" for tier in ("stable", "beta", "experimental")]
    return "\n".join(rows)


def render_adapter_tiers(doc: dict[str, Any]) -> str:
    python = {a["module"]: a for a in doc["adapters"]["python"]}
    typescript = {a["module"]: a for a in doc["adapters"]["typescript"]}
    rows = ["| Adapter | Python | TypeScript |", "| --- | --- | --- |"]
    for module in list(python) + [m for m in typescript if m not in python]:
        name = (python.get(module) or typescript[module])["name"]
        py = python[module]["maturity"] if module in python else "—"
        ts = typescript[module]["maturity"] if module in typescript else "—"
        rows.append(f"| {name} | {py} | {ts} |")
    defs = doc["maturity_definitions"]
    rows.append("")
    rows += [f"- **{tier}**: {defs[tier]}" for tier in ("stable", "beta", "experimental")]
    return "\n".join(rows)


RENDERERS: dict[str, Callable[[dict[str, Any]], str]] = {
    "layers": render_layers,
    "adapters": render_adapters,
    "features": render_features,
    "adapter-tiers": render_adapter_tiers,
}


def replace_region(text: str, name: str, body: str, path: str) -> str:
    start, end = marker(name, "start"), marker(name, "end")
    if text.count(start) != 1 or text.count(end) != 1:
        raise SystemExit(f"{path}: needs exactly one {start} and one {end}")
    head, rest = text.split(start, 1)
    old, tail = rest.split(end, 1)
    if "claims:" in old:
        raise SystemExit(f"{path}: markers for {name} enclose another region")
    return f"{head}{start}\n{body}\n{end}{tail}"


def strip_regions(text: str) -> str:
    return re.sub(r"<!-- claims:([\w-]+):start -->.*?<!-- claims:\1:end -->", "", text, flags=re.S)


def retired_claims(root: Path) -> list[str]:
    problems: list[str] = []
    seen: set[Path] = set()
    for pattern in SCAN_GLOBS:
        for path in sorted(root.glob(pattern)):
            if path in seen:
                continue
            seen.add(path)
            rel = path.relative_to(root)
            for lineno, line in enumerate(strip_regions(path.read_text()).splitlines(), 1):
                for rx, why in RETIRED_CLAIMS:
                    if rx.search(line):
                        problems.append(f"{rel}:{lineno}: {rx.pattern!r} ({why})")
    return problems


def main(argv: list[str]) -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--check", action="store_true", help="exit 1 on drift instead of rewriting")
    ap.add_argument("--engine", type=Path, help="attest-engine binary")
    ap.add_argument("--root", type=Path, default=ROOT, help="repository root (default: this repository)")
    args = ap.parse_args(argv)

    engine = args.engine or Path(os.environ.get("ATTEST_ENGINE_PATH") or args.root / "bin" / "attest-engine")
    doc = load_capabilities(engine)

    problems: list[str] = []
    for rel, regions in TARGETS.items():
        path = args.root / rel
        text = original = path.read_text()
        for name in regions:
            text = replace_region(text, name, RENDERERS[name](doc), rel)
        if text != original:
            if args.check:
                problems.append(f"{rel}: generated regions differ from `attest-engine capabilities --json`")
            else:
                path.write_text(text)
                print(f"updated {rel}")
    problems += retired_claims(args.root)

    if problems:
        print("claims drift:", file=sys.stderr)
        for p in problems:
            print(f"  {p}", file=sys.stderr)
        print("run `make generate-claims` for generated regions; reword retired claims by hand", file=sys.stderr)
        return 1
    print("claims ok" if args.check else "claims generated")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
