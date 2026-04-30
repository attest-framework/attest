"""CLI entry point for Attest: python -m attest."""

from __future__ import annotations

import json
import os
import sys
from pathlib import Path


def _cache_dir() -> Path:
    """Return the cache directory, respecting ATTEST_CACHE_DIR env override."""
    env_override = os.environ.get("ATTEST_CACHE_DIR")
    if env_override:
        return Path(env_override)
    return Path.home() / ".attest" / "cache"


def _cache_db_path() -> Path:
    """Return the path to the cache database file."""
    return _cache_dir() / "attest.db"


def _cmd_cache_stats() -> None:
    """Print JSON stats about the cache database."""
    db_path = _cache_db_path()
    exists = db_path.exists()
    file_size = db_path.stat().st_size if exists else 0
    stats = {
        "exists": exists,
        "file_size": file_size,
        "path": str(db_path),
    }
    print(json.dumps(stats))


def _cmd_cache_clear() -> None:
    """Delete the cache database file."""
    db_path = _cache_db_path()
    if db_path.exists():
        db_path.unlink()
        print(f"Cleared cache: {db_path}")
    else:
        print(f"No cache to clear: {db_path}")


def _cmd_calibrate(argv: list[str]) -> None:
    """Run ``attest calibrate --labels <path>``.

    Mirrors the engine's calibrate subcommand: expects the labels file
    to already include both human_label and judge_score so agreement can
    run offline without LLM credentials. Prints metrics as JSON.
    """
    import argparse

    from attest.calibration import LabelPair, compute_agreement, load_labels

    parser = argparse.ArgumentParser(
        prog="attest calibrate",
        description="Compute Cohen's κ, agreement, and ROC-AUC over labeled judge outputs.",
    )
    parser.add_argument("--labels", required=True, help="Path to CSV or JSONL labels file")
    parser.add_argument("--rubric", default="default", help="Rubric name for the report")
    parser.add_argument(
        "--rubric-version",
        default="",
        help="Rubric version to associate with the result (defaults to engine builtin if known)",
    )
    parser.add_argument(
        "--threshold",
        type=float,
        default=0.5,
        help="Binarization threshold for κ and ROC-AUC (must be in (0, 1))",
    )
    args = parser.parse_args(argv)

    records = load_labels(Path(args.labels))
    pairs = [LabelPair(human=r.human_label, judge=r.judge_score) for r in records if r.judge_known]
    missing = sum(1 for r in records if not r.judge_known)
    if not pairs:
        print(
            f"calibrate: no rows had judge_score ({missing} rows missing); "
            "pre-score the labels file first",
            file=sys.stderr,
        )
        sys.exit(1)

    try:
        result = compute_agreement(pairs, args.threshold)
    except ValueError as exc:
        print(f"calibrate: {exc}", file=sys.stderr)
        sys.exit(1)

    out = {
        "rubric_name": args.rubric,
        "rubric_version": args.rubric_version,
        "threshold": result.threshold,
        "label_count": result.n,
        "agreement": round(result.agreement, 3),
        "cohen_kappa": round(result.cohen_kappa, 3),
        "roc_auc": round(result.roc_auc, 3),
        "missing_judge": missing,
    }
    print(json.dumps(out, indent=2, sort_keys=True))


def main() -> None:
    """Run attest CLI."""
    args = sys.argv[1:]

    if args and args[0] == "--version":
        from attest import __version__

        print(f"attest {__version__}")
        sys.exit(0)

    # `attest cache stats` and `attest cache clear`
    if len(args) >= 2 and args[0] == "cache":
        subcommand = args[1]
        if subcommand == "stats":
            _cmd_cache_stats()
            sys.exit(0)
        elif subcommand == "clear":
            _cmd_cache_clear()
            sys.exit(0)
        else:
            print(f"Unknown cache subcommand: {subcommand}", file=sys.stderr)
            print("Available: cache stats, cache clear", file=sys.stderr)
            sys.exit(1)

    if args and args[0] == "init":
        from attest.scaffold import scaffold_project

        scaffold_project(Path.cwd())
        sys.exit(0)

    if args and args[0] == "validate":
        from attest.scaffold import validate_suite

        validate_suite(Path.cwd())
        sys.exit(0)

    if args and args[0] == "calibrate":
        _cmd_calibrate(args[1:])
        sys.exit(0)

    # `attest run [args]` — explicit alias for pytest passthrough
    if args and args[0] == "run":
        args = args[1:]

    # Default: run pytest with attest plugin loaded, pass through remaining args
    try:
        import pytest
    except ImportError:
        print("pytest is required. Install with: uv add pytest", file=sys.stderr)
        sys.exit(1)

    sys.exit(pytest.main(args))


if __name__ == "__main__":
    main()
