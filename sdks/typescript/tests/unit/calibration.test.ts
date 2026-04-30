import { describe, it, expect } from "vitest";
import {
  computeAgreement,
  loadLabelsCSV,
  loadLabelsJSONL,
  promptHash,
  type LabelPair,
} from "../../packages/core/src/calibration.js";

describe("computeAgreement", () => {
  it("returns perfect κ and AUC for identical labels", () => {
    const pairs: LabelPair[] = [
      { human: 0.9, judge: 0.9 },
      { human: 0.8, judge: 0.8 },
      { human: 0.2, judge: 0.2 },
      { human: 0.1, judge: 0.1 },
    ];
    const got = computeAgreement(pairs, 0.5);
    expect(got.agreement).toBe(1);
    expect(got.cohenKappa).toBe(1);
    expect(got.rocAuc).toBe(1);
    expect(got.n).toBe(4);
  });

  it("computes κ≈0 for fully random agreement", () => {
    const pairs: LabelPair[] = [
      { human: 0.9, judge: 0.9 },
      { human: 0.9, judge: 0.1 },
      { human: 0.1, judge: 0.9 },
      { human: 0.1, judge: 0.1 },
    ];
    const got = computeAgreement(pairs, 0.5);
    expect(got.agreement).toBe(0.5);
    expect(Math.abs(got.cohenKappa)).toBeLessThan(1e-9);
  });

  it("rejects empty pairs", () => {
    expect(() => computeAgreement([], 0.5)).toThrow(/no labeled pairs/);
  });

  it("rejects out-of-range threshold", () => {
    const pairs: LabelPair[] = [{ human: 0.5, judge: 0.5 }];
    for (const th of [0, 1, -0.1, 1.1]) {
      expect(() => computeAgreement(pairs, th)).toThrow(/threshold/);
    }
  });

  it("returns AUC 0 when only one human-class is present", () => {
    const pairs: LabelPair[] = [
      { human: 0.9, judge: 0.9 },
      { human: 0.85, judge: 0.7 },
      { human: 0.8, judge: 0.6 },
    ];
    const got = computeAgreement(pairs, 0.5);
    expect(got.rocAuc).toBe(0);
    expect(got.agreement).toBeGreaterThan(0);
  });
});

describe("loadLabelsCSV", () => {
  it("parses 3-column CSV with header", () => {
    const src = `input,human_label,judge_score
hello,0.9,0.85
world,0.1,0.2
# comment row
just-input,0.5,
`;
    const got = loadLabelsCSV(src);
    expect(got.length).toBe(3);
    expect(got[0]!.judgeKnown).toBe(true);
    expect(got[0]!.judgeScore).toBe(0.85);
    expect(got[2]!.judgeKnown).toBe(false);
  });

  it("parses 2-column CSV without header", () => {
    const got = loadLabelsCSV("hello,0.9\nworld,0.1\n");
    expect(got.length).toBe(2);
  });

  it("rejects non-float human_label", () => {
    expect(() => loadLabelsCSV("input,label\nhello,not_a_number\n")).toThrow(
      /human_label not a float/,
    );
  });
});

describe("loadLabelsJSONL", () => {
  it("parses optional judge_score", () => {
    const src =
      '{"input": "a", "human_label": 0.9, "judge_score": 0.85}\n' +
      '{"input": "b", "human_label": 0.1}\n';
    const got = loadLabelsJSONL(src);
    expect(got.length).toBe(2);
    expect(got[0]!.judgeKnown).toBe(true);
    expect(got[1]!.judgeKnown).toBe(false);
  });

  it("rejects rows missing human_label", () => {
    expect(() => loadLabelsJSONL('{"input": "a"}\n')).toThrow(
      /missing human_label/,
    );
  });
});

describe("promptHash", () => {
  it("matches 16-character SHA-256 prefix", () => {
    const h = promptHash("hello world");
    expect(h.length).toBe(16);
    expect(/^[0-9a-f]+$/.test(h)).toBe(true);
    expect(promptHash("hello world")).toBe(h);
  });
});
