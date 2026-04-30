/**
 * Calibration helpers — TS parity with engine/internal/assertion/judge/calibration.go
 * and sdks/python/src/attest/calibration.py.
 *
 * Computes Cohen's κ, agreement %, and ROC-AUC over (human, judge) score
 * pairs and parses CSV/JSONL labels files. Metrics match the engine
 * byte-for-byte on the same input.
 */

import { createHash } from "node:crypto";

export interface LabelPair {
  readonly human: number;
  readonly judge: number;
}

export interface AgreementResult {
  readonly threshold: number;
  readonly n: number;
  readonly agreement: number;
  readonly cohenKappa: number;
  readonly rocAuc: number;
}

export interface LabeledRecord {
  input: string;
  humanLabel: number;
  judgeScore: number;
  judgeKnown: boolean;
}

/**
 * Compute Cohen's κ, agreement %, and ROC-AUC over labeled pairs.
 *
 * Throws when pairs is empty or threshold is outside (0, 1). Cohen's κ
 * degenerates to 0 when all labels land in a single class for both
 * raters; ROC-AUC is reported as 0 when only one human-class is
 * present.
 */
export function computeAgreement(
  pairs: readonly LabelPair[],
  threshold: number,
): AgreementResult {
  if (pairs.length === 0) {
    throw new Error("no labeled pairs available");
  }
  if (threshold <= 0 || threshold >= 1) {
    throw new Error("threshold must be in (0, 1)");
  }

  const n = pairs.length;
  let humanPos = 0;
  let judgePos = 0;
  let bothPos = 0;
  let bothNeg = 0;
  for (const p of pairs) {
    const hPos = p.human >= threshold;
    const jPos = p.judge >= threshold;
    if (hPos) humanPos++;
    if (jPos) judgePos++;
    if (hPos && jPos) bothPos++;
    if (!hPos && !jPos) bothNeg++;
  }
  const humanNeg = n - humanPos;
  const judgeNeg = n - judgePos;

  const agreement = (bothPos + bothNeg) / n;
  const expected =
    (humanPos * judgePos + humanNeg * judgeNeg) / (n * n);
  const cohenKappa =
    expected < 1 ? (agreement - expected) / (1 - expected) : 0;

  const rocAuc = rocAUC(pairs, threshold);

  return { threshold, n, agreement, cohenKappa, rocAuc };
}

function rocAUC(pairs: readonly LabelPair[], threshold: number): number {
  const rows: { score: number; pos: boolean }[] = pairs.map((p) => ({
    score: p.judge,
    pos: p.human >= threshold,
  }));
  const pos = rows.filter((r) => r.pos).length;
  const neg = rows.length - pos;
  if (pos === 0 || neg === 0) return 0;

  rows.sort((a, b) => a.score - b.score);

  let rankSumPos = 0;
  let i = 0;
  while (i < rows.length) {
    let j = i;
    while (j < rows.length && rows[j]!.score === rows[i]!.score) {
      j++;
    }
    const avgRank = (i + j + 1) / 2;
    for (let k = i; k < j; k++) {
      if (rows[k]!.pos) rankSumPos += avgRank;
    }
    i = j;
  }

  const auc = (rankSumPos - (pos * (pos + 1)) / 2) / (pos * neg);
  if (Number.isNaN(auc) || !Number.isFinite(auc)) return 0;
  return auc;
}

/**
 * Parse a 2- or 3-column CSV (input, human_label[, judge_score]).
 * Header detected when row[0][1] does not parse as a float. Lines
 * beginning with `#` are skipped.
 */
export function loadLabelsCSV(text: string): LabeledRecord[] {
  const rows = parseCSV(text);
  if (rows.length === 0) {
    throw new Error("empty CSV");
  }
  let start = 0;
  if (rows[0]!.length >= 2 && Number.isNaN(Number(rows[0]![1]!.trim()))) {
    start = 1;
  }
  const out: LabeledRecord[] = [];
  for (let idx = start; idx < rows.length; idx++) {
    const row = rows[idx]!;
    const lineNum = idx + 1;
    if (row.length === 0) continue;
    if (row[0]!.trimStart().startsWith("#")) continue;
    if (row.length < 2) {
      throw new Error(
        `CSV line ${lineNum}: want at least 2 columns (input, human_label)`,
      );
    }
    const human = Number(row[1]!.trim());
    if (Number.isNaN(human)) {
      throw new Error(`CSV line ${lineNum}: human_label not a float: ${row[1]}`);
    }
    const rec: LabeledRecord = {
      input: row[0]!,
      humanLabel: human,
      judgeScore: 0,
      judgeKnown: false,
    };
    if (row.length >= 3 && row[2]!.trim() !== "") {
      const judge = Number(row[2]!.trim());
      if (Number.isNaN(judge)) {
        throw new Error(
          `CSV line ${lineNum}: judge_score not a float: ${row[2]}`,
        );
      }
      rec.judgeScore = judge;
      rec.judgeKnown = true;
    }
    out.push(rec);
  }
  if (out.length === 0) {
    throw new Error("CSV contained no labeled rows");
  }
  return out;
}

/**
 * Parse a newline-delimited JSON file. Each line is an object with the
 * shape {"input": "...", "human_label": 0.9, "judge_score": 0.8}.
 * `judge_score` is optional.
 */
export function loadLabelsJSONL(text: string): LabeledRecord[] {
  const out: LabeledRecord[] = [];
  const lines = text.split(/\r?\n/);
  for (let i = 0; i < lines.length; i++) {
    const stripped = lines[i]!.trim();
    if (!stripped) continue;
    let obj: Record<string, unknown>;
    try {
      obj = JSON.parse(stripped) as Record<string, unknown>;
    } catch (err) {
      throw new Error(`JSONL line ${i + 1}: ${(err as Error).message}`);
    }
    if (Object.keys(obj).length === 0) continue;
    if (obj["human_label"] === undefined) {
      throw new Error(`JSONL line ${i + 1}: missing human_label`);
    }
    const rec: LabeledRecord = {
      input: typeof obj["input"] === "string" ? obj["input"] : "",
      humanLabel: Number(obj["human_label"]),
      judgeScore: 0,
      judgeKnown: false,
    };
    if (obj["judge_score"] !== undefined && obj["judge_score"] !== null) {
      rec.judgeScore = Number(obj["judge_score"]);
      rec.judgeKnown = true;
    }
    out.push(rec);
  }
  if (out.length === 0) {
    throw new Error("JSONL contained no labeled rows");
  }
  return out;
}

/**
 * Return the 16-character SHA-256 prefix used by the engine for prompt
 * hashes. Mirrors promptHash in engine/internal/assertion/judge_eval.go
 * so SDK-recorded calibration rows align with JudgeMetadata.prompt_hash.
 */
export function promptHash(text: string): string {
  return createHash("sha256").update(text).digest("hex").slice(0, 16);
}

/**
 * Minimal RFC-4180-ish CSV parser. Handles quoted fields with embedded
 * commas and CRLF line endings. Sufficient for calibration label files;
 * callers needing full CSV support should bring their own parser.
 */
function parseCSV(text: string): string[][] {
  const rows: string[][] = [];
  let current: string[] = [];
  let field = "";
  let inQuotes = false;
  let i = 0;
  while (i < text.length) {
    const ch = text[i]!;
    if (inQuotes) {
      if (ch === '"') {
        if (text[i + 1] === '"') {
          field += '"';
          i += 2;
          continue;
        }
        inQuotes = false;
        i++;
        continue;
      }
      field += ch;
      i++;
      continue;
    }
    if (ch === '"') {
      inQuotes = true;
      i++;
      continue;
    }
    if (ch === ",") {
      current.push(field);
      field = "";
      i++;
      continue;
    }
    if (ch === "\n") {
      current.push(field);
      rows.push(current);
      current = [];
      field = "";
      i++;
      continue;
    }
    if (ch === "\r") {
      i++;
      continue;
    }
    field += ch;
    i++;
  }
  if (field.length > 0 || current.length > 0) {
    current.push(field);
    rows.push(current);
  }
  return rows;
}
