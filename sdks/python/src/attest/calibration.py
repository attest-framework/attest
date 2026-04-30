"""Calibration helpers for the judge subsystem.

Provides Python-side parity with engine/internal/assertion/judge/calibration.go
so SDK callers can compute agreement metrics, load labeled CSV/JSONL files,
and derive prompt hashes without spawning the engine binary. The CLI
``python -m attest calibrate`` consumes this module.

The metrics match the engine implementation byte-for-byte on the same input
so a calibration result computed in Python and rendered through the engine
report look identical.
"""

from __future__ import annotations

import csv
import hashlib
import json
import math
from dataclasses import dataclass
from io import StringIO
from pathlib import Path


@dataclass(frozen=True)
class LabelPair:
    """One (human, judge) score pair, both in [0, 1]."""

    human: float
    judge: float


@dataclass(frozen=True)
class AgreementResult:
    """Output of ``compute_agreement``.

    Attributes mirror engine ``AgreementResult`` so reports from either
    side render identically. ``roc_auc`` is 0.0 when only one human-class
    is present (AUC undefined).
    """

    threshold: float
    n: int
    agreement: float
    cohen_kappa: float
    roc_auc: float


@dataclass
class LabeledRecord:
    """Parsed row from a CSV/JSONL labels file.

    ``judge_known`` distinguishes "we already have a judge score" from
    "fill it in by re-calling the judge". The CLI raises if no rows are
    pre-scored, since the offline calibrate command does not call LLMs.
    """

    input: str
    human_label: float
    judge_score: float = 0.0
    judge_known: bool = False


def compute_agreement(pairs: list[LabelPair], threshold: float) -> AgreementResult:
    """Compute Cohen's κ, agreement %, and ROC-AUC over labeled pairs.

    Raises ``ValueError`` when ``pairs`` is empty or ``threshold`` is
    outside (0, 1). Cohen's κ degenerates to 0 when all pairs land in a
    single class for both raters; ROC-AUC is reported as 0.0 when only
    one human-class is present.
    """

    if not pairs:
        raise ValueError("no labeled pairs available")
    if threshold <= 0 or threshold >= 1:
        raise ValueError("threshold must be in (0, 1)")

    n = len(pairs)
    human_pos = sum(1 for p in pairs if p.human >= threshold)
    judge_pos = sum(1 for p in pairs if p.judge >= threshold)
    both_pos = sum(1 for p in pairs if p.human >= threshold and p.judge >= threshold)
    both_neg = sum(1 for p in pairs if p.human < threshold and p.judge < threshold)
    human_neg = n - human_pos
    judge_neg = n - judge_pos

    agreement = (both_pos + both_neg) / n
    expected = (human_pos * judge_pos + human_neg * judge_neg) / (n * n)
    kappa = 0.0
    if expected < 1:
        kappa = (agreement - expected) / (1 - expected)

    auc = _roc_auc(pairs, threshold)
    return AgreementResult(
        threshold=threshold,
        n=n,
        agreement=agreement,
        cohen_kappa=kappa,
        roc_auc=auc,
    )


def _roc_auc(pairs: list[LabelPair], threshold: float) -> float:
    """Mann-Whitney U-statistic ROC-AUC with mid-rank tie handling.

    Returns 0.0 when only one human-class is present (undefined AUC).
    """

    rows = [(p.judge, p.human >= threshold) for p in pairs]
    pos = sum(1 for _, is_pos in rows if is_pos)
    neg = len(rows) - pos
    if pos == 0 or neg == 0:
        return 0.0

    rows.sort(key=lambda r: r[0])
    rank_sum_pos = 0.0
    i = 0
    while i < len(rows):
        j = i
        while j < len(rows) and rows[j][0] == rows[i][0]:
            j += 1
        avg_rank = (i + j + 1) / 2
        for k in range(i, j):
            if rows[k][1]:
                rank_sum_pos += avg_rank
        i = j

    auc = (rank_sum_pos - pos * (pos + 1) / 2) / (pos * neg)
    if math.isnan(auc) or math.isinf(auc):
        return 0.0
    return auc


def load_labels_csv(text: str) -> list[LabeledRecord]:
    """Parse a 2- or 3-column CSV (input, human_label[, judge_score]).

    Detects a header row when the second column of the first row does
    not parse as a float. Lines beginning with ``#`` are skipped. Raises
    ``ValueError`` if no rows can be parsed.
    """

    rows = list(csv.reader(StringIO(text)))
    if not rows:
        raise ValueError("empty CSV")
    start = 0
    if len(rows[0]) >= 2:
        try:
            float(rows[0][1].strip())
        except ValueError:
            start = 1

    out: list[LabeledRecord] = []
    for offset, row in enumerate(rows[start:]):
        line = offset + start + 1
        if not row:
            continue
        if row[0].lstrip().startswith("#"):
            continue
        if len(row) < 2:
            raise ValueError(
                f"CSV line {line}: want at least 2 columns (input, human_label)"
            )
        try:
            human = float(row[1].strip())
        except ValueError as exc:
            raise ValueError(f"CSV line {line}: human_label not a float: {exc}") from exc
        rec = LabeledRecord(input=row[0], human_label=human)
        if len(row) >= 3 and row[2].strip():
            try:
                rec.judge_score = float(row[2].strip())
            except ValueError as exc:
                raise ValueError(
                    f"CSV line {line}: judge_score not a float: {exc}"
                ) from exc
            rec.judge_known = True
        out.append(rec)
    if not out:
        raise ValueError("CSV contained no labeled rows")
    return out


def load_labels_jsonl(text: str) -> list[LabeledRecord]:
    """Parse a newline-delimited JSON file.

    Each line is ``{"input": "...", "human_label": 0.9, "judge_score": 0.8}``.
    ``judge_score`` is optional; lines decoding to an empty object are
    skipped (handles trailing newlines from editors).
    """

    out: list[LabeledRecord] = []
    for lineno, raw in enumerate(text.splitlines(), start=1):
        stripped = raw.strip()
        if not stripped:
            continue
        try:
            obj = json.loads(stripped)
        except json.JSONDecodeError as exc:
            raise ValueError(f"JSONL line {lineno}: {exc}") from exc
        if not obj:
            continue
        if "human_label" not in obj:
            raise ValueError(f"JSONL line {lineno}: missing human_label")
        rec = LabeledRecord(
            input=obj.get("input", ""),
            human_label=float(obj["human_label"]),
        )
        if "judge_score" in obj and obj["judge_score"] is not None:
            rec.judge_score = float(obj["judge_score"])
            rec.judge_known = True
        out.append(rec)
    if not out:
        raise ValueError("JSONL contained no labeled rows")
    return out


def load_labels(path: Path) -> list[LabeledRecord]:
    """Dispatch to CSV or JSONL loader based on file extension."""

    text = path.read_text()
    suffix = path.suffix.lower()
    if suffix in (".jsonl", ".ndjson"):
        return load_labels_jsonl(text)
    if suffix == ".csv":
        return load_labels_csv(text)
    if text.lstrip().startswith("{"):
        return load_labels_jsonl(text)
    return load_labels_csv(text)


def prompt_hash(text: str) -> str:
    """Return the 16-character SHA-256 prefix used by the engine.

    Mirrors ``promptHash`` in engine/internal/assertion/judge_eval.go so
    SDK-recorded calibration rows align with the JudgeMetadata
    prompt_hash field on assertion results.
    """

    digest = hashlib.sha256(text.encode("utf-8")).digest()
    return digest[:8].hex()
