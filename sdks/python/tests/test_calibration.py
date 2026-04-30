"""Tests for attest.calibration.

Mirrors engine/internal/assertion/judge/calibration_test.go so SDK and
engine produce byte-identical metrics on the same inputs.
"""

from __future__ import annotations

import json
import math
from pathlib import Path

import pytest

from attest.calibration import (
    LabelPair,
    compute_agreement,
    load_labels,
    load_labels_csv,
    load_labels_jsonl,
    prompt_hash,
)


def test_compute_agreement_perfect() -> None:
    pairs = [
        LabelPair(human=0.9, judge=0.9),
        LabelPair(human=0.8, judge=0.8),
        LabelPair(human=0.2, judge=0.2),
        LabelPair(human=0.1, judge=0.1),
    ]
    got = compute_agreement(pairs, 0.5)
    assert got.agreement == 1.0
    assert got.cohen_kappa == 1.0
    assert got.roc_auc == 1.0
    assert got.n == 4


def test_compute_agreement_random_kappa_zero() -> None:
    pairs = [
        LabelPair(human=0.9, judge=0.9),
        LabelPair(human=0.9, judge=0.1),
        LabelPair(human=0.1, judge=0.9),
        LabelPair(human=0.1, judge=0.1),
    ]
    got = compute_agreement(pairs, 0.5)
    assert got.agreement == 0.5
    assert math.isclose(got.cohen_kappa, 0.0, abs_tol=1e-9)


def test_compute_agreement_rejects_empty() -> None:
    with pytest.raises(ValueError, match="no labeled pairs"):
        compute_agreement([], 0.5)


def test_compute_agreement_rejects_bad_threshold() -> None:
    pairs = [LabelPair(human=0.5, judge=0.5)]
    for th in (0.0, 1.0, -0.1, 1.1):
        with pytest.raises(ValueError):
            compute_agreement(pairs, th)


def test_compute_agreement_one_class_no_auc() -> None:
    pairs = [
        LabelPair(human=0.9, judge=0.9),
        LabelPair(human=0.8, judge=0.7),
        LabelPair(human=0.85, judge=0.6),
    ]
    got = compute_agreement(pairs, 0.5)
    assert got.roc_auc == 0.0
    assert got.agreement > 0


def test_load_labels_csv_with_header() -> None:
    src = (
        "input,human_label,judge_score\n"
        "hello,0.9,0.85\n"
        "world,0.1,0.2\n"
        "# comment\n"
        "just-input,0.5,\n"
    )
    got = load_labels_csv(src)
    assert len(got) == 3
    assert got[0].judge_known and got[0].judge_score == 0.85
    assert not got[2].judge_known


def test_load_labels_csv_no_header() -> None:
    src = "hello,0.9\nworld,0.1\n"
    got = load_labels_csv(src)
    assert len(got) == 2


def test_load_labels_csv_rejects_bad_float() -> None:
    src = "input,label\nhello,not_a_number\n"
    with pytest.raises(ValueError, match="human_label not a float"):
        load_labels_csv(src)


def test_load_labels_jsonl_shape() -> None:
    src = (
        '{"input": "a", "human_label": 0.9, "judge_score": 0.85}\n'
        '{"input": "b", "human_label": 0.1}\n'
    )
    got = load_labels_jsonl(src)
    assert len(got) == 2
    assert got[0].judge_known
    assert not got[1].judge_known


def test_load_labels_jsonl_missing_human_label() -> None:
    with pytest.raises(ValueError, match="missing human_label"):
        load_labels_jsonl('{"input": "a"}\n')


def test_load_labels_dispatch(tmp_path: Path) -> None:
    csv_path = tmp_path / "labels.csv"
    csv_path.write_text("input,label\nx,0.9\n")
    jsonl_path = tmp_path / "labels.jsonl"
    jsonl_path.write_text('{"input": "x", "human_label": 0.9}\n')

    assert len(load_labels(csv_path)) == 1
    assert len(load_labels(jsonl_path)) == 1


def test_prompt_hash_matches_engine_format() -> None:
    # 16 hex chars (8 bytes) — same length engine emits.
    h = prompt_hash("hello world")
    assert len(h) == 16
    assert all(c in "0123456789abcdef" for c in h)
    # Deterministic.
    assert prompt_hash("hello world") == h


def test_calibrate_roundtrip(tmp_path: Path) -> None:
    """End-to-end: write CSV, call load + compute, match expected metrics."""
    src = "input,human,judge\n"
    src += "ok-1,0.9,0.95\nok-2,0.8,0.85\nbad-1,0.1,0.2\nbad-2,0.05,0.1\n"
    path = tmp_path / "labels.csv"
    path.write_text(src)
    records = load_labels(path)
    pairs = [LabelPair(human=r.human_label, judge=r.judge_score) for r in records]
    result = compute_agreement(pairs, 0.5)
    assert result.n == 4
    # Perfect class agreement on 0.5 threshold → κ=1, AUC=1.
    assert result.cohen_kappa == 1.0
    assert result.roc_auc == 1.0


def test_calibrate_cli_emits_json(tmp_path: Path) -> None:
    """Smoke-test the CLI subcommand by invoking through __main__."""
    import subprocess
    import sys as sysmod

    csv_path = tmp_path / "labels.csv"
    csv_path.write_text("input,human,judge\nok,0.9,0.85\nbad,0.1,0.15\n")
    proc = subprocess.run(
        [sysmod.executable, "-m", "attest", "calibrate", "--labels", str(csv_path)],
        capture_output=True,
        text=True,
        check=False,
    )
    assert proc.returncode == 0, proc.stderr
    payload = json.loads(proc.stdout)
    assert payload["label_count"] == 2
    assert payload["agreement"] == 1.0
