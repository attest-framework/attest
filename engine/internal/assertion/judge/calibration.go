package judge

import (
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
)

// PromptHash returns the 16-character SHA-256 prefix the engine uses to
// correlate calibration history with JudgeMetadata.PromptHash. Shared so
// the engine evaluator and CLI cannot drift apart on the prefix length.
func PromptHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:8])
}

// AgreementResult holds the calibration metrics computed over a set of
// (human_label, judge_score) pairs. Threshold is the cut-off used to
// binarize continuous scores when computing Cohen's κ and ROC-AUC; a
// score >= threshold counts as a positive label.
type AgreementResult struct {
	Threshold  float64
	N          int
	Agreement  float64
	CohenKappa float64
	ROCAUC     float64
}

// LabelPair is one (human, judge) pair. Both scores are expected in [0, 1].
type LabelPair struct {
	Human float64
	Judge float64
}

// ComputeAgreement returns the agreement metrics for the given pairs at
// the supplied threshold. Returns an error when there are no pairs.
// Cohen's κ is undefined when expected agreement = 1 (all labels in a
// single class for both raters) and is reported as 0 in that case;
// ROC-AUC is omitted when only one human-class is present.
func ComputeAgreement(pairs []LabelPair, threshold float64) (AgreementResult, error) {
	if len(pairs) == 0 {
		return AgreementResult{}, errors.New("no labeled pairs available")
	}
	if threshold <= 0 || threshold >= 1 {
		return AgreementResult{}, errors.New("threshold must be in (0, 1)")
	}
	humanPos, judgePos := 0, 0
	bothPos, bothNeg := 0, 0
	for _, p := range pairs {
		hPos := p.Human >= threshold
		jPos := p.Judge >= threshold
		if hPos {
			humanPos++
		}
		if jPos {
			judgePos++
		}
		if hPos && jPos {
			bothPos++
		}
		if !hPos && !jPos {
			bothNeg++
		}
	}
	n := len(pairs)
	humanNeg := n - humanPos
	judgeNeg := n - judgePos

	agreement := float64(bothPos+bothNeg) / float64(n)

	expectedAgreement := (float64(humanPos)*float64(judgePos) + float64(humanNeg)*float64(judgeNeg)) / float64(n*n)
	kappa := 0.0
	if expectedAgreement < 1 {
		kappa = (agreement - expectedAgreement) / (1 - expectedAgreement)
	}

	result := AgreementResult{
		Threshold:  threshold,
		N:          n,
		Agreement:  agreement,
		CohenKappa: kappa,
	}
	if auc, err := rocAUC(pairs, threshold); err == nil {
		result.ROCAUC = auc
	}
	return result, nil
}

// rocAUC computes the area under the ROC curve using the Mann–Whitney
// U-statistic identity. Treats human scores >= threshold as positive
// labels. Returns an error if either class is empty (AUC undefined).
func rocAUC(pairs []LabelPair, threshold float64) (float64, error) {
	type indexed struct {
		score float64
		pos   bool
	}
	rows := make([]indexed, len(pairs))
	pos, neg := 0, 0
	for i, p := range pairs {
		isPos := p.Human >= threshold
		rows[i] = indexed{score: p.Judge, pos: isPos}
		if isPos {
			pos++
		} else {
			neg++
		}
	}
	if pos == 0 || neg == 0 {
		return 0, errors.New("ROC-AUC undefined: only one class in labels")
	}

	sort.SliceStable(rows, func(i, j int) bool { return rows[i].score < rows[j].score })

	// Mid-rank handling for ties.
	rankSumPos := 0.0
	i := 0
	for i < len(rows) {
		j := i
		for j < len(rows) && rows[j].score == rows[i].score {
			j++
		}
		avgRank := float64(i+j+1) / 2
		for k := i; k < j; k++ {
			if rows[k].pos {
				rankSumPos += avgRank
			}
		}
		i = j
	}

	auc := (rankSumPos - float64(pos)*float64(pos+1)/2) / (float64(pos) * float64(neg))
	if math.IsNaN(auc) || math.IsInf(auc, 0) {
		return 0, errors.New("ROC-AUC numerical error")
	}
	return auc, nil
}

// LabeledRecord is one parsed calibration row. JudgeKnown signals whether
// the caller already has a judge score (e.g., re-loading a prior calibration
// run); when false, the calibration runner will re-call the judge.
type LabeledRecord struct {
	Input      string
	HumanLabel float64
	JudgeScore float64
	JudgeKnown bool
}

// LoadLabelsCSV parses a 2- or 3-column CSV (input, human_label[,
// judge_score]) into LabeledRecord slice. The first row is detected as a
// header if the second column does not parse as a float. Lines beginning
// with "#" are treated as comments and skipped. Returns an error if no
// rows can be parsed so config bugs fail fast.
func LoadLabelsCSV(r io.Reader) ([]LabeledRecord, error) {
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1
	rows, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read CSV: %w", err)
	}
	if len(rows) == 0 {
		return nil, errors.New("empty CSV")
	}
	out := make([]LabeledRecord, 0, len(rows))
	start := 0
	if len(rows[0]) >= 2 {
		if _, err := strconv.ParseFloat(strings.TrimSpace(rows[0][1]), 64); err != nil {
			start = 1
		}
	}
	for i, row := range rows[start:] {
		lineNum := i + start + 1
		if len(row) == 0 {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(row[0]), "#") {
			continue
		}
		if len(row) < 2 {
			return nil, fmt.Errorf("CSV line %d: want at least 2 columns (input,human_label)", lineNum)
		}
		human, err := strconv.ParseFloat(strings.TrimSpace(row[1]), 64)
		if err != nil {
			return nil, fmt.Errorf("CSV line %d: human_label not a float: %w", lineNum, err)
		}
		rec := LabeledRecord{Input: row[0], HumanLabel: human}
		if len(row) >= 3 && strings.TrimSpace(row[2]) != "" {
			judgeScore, err := strconv.ParseFloat(strings.TrimSpace(row[2]), 64)
			if err != nil {
				return nil, fmt.Errorf("CSV line %d: judge_score not a float: %w", lineNum, err)
			}
			rec.JudgeScore = judgeScore
			rec.JudgeKnown = true
		}
		out = append(out, rec)
	}
	if len(out) == 0 {
		return nil, errors.New("CSV contained no labeled rows")
	}
	return out, nil
}

// LoadLabelsJSONL parses a newline-delimited JSON file. Each line is an
// object with the shape:
//
//	{"input": "...", "human_label": 0.9, "judge_score": 0.8}
//
// judge_score is optional. Lines that decode to an empty object are
// skipped so editors that append trailing newlines don't break the
// loader. Returns an error on the first malformed line so config bugs
// surface early.
func LoadLabelsJSONL(r io.Reader) ([]LabeledRecord, error) {
	dec := json.NewDecoder(r)
	out := make([]LabeledRecord, 0)
	line := 0
	for {
		line++
		var raw struct {
			Input      string   `json:"input"`
			HumanLabel *float64 `json:"human_label"`
			JudgeScore *float64 `json:"judge_score,omitempty"`
		}
		err := dec.Decode(&raw)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("JSONL line %d: %w", line, err)
		}
		if raw.Input == "" && raw.HumanLabel == nil {
			continue
		}
		if raw.HumanLabel == nil {
			return nil, fmt.Errorf("JSONL line %d: missing human_label", line)
		}
		rec := LabeledRecord{Input: raw.Input, HumanLabel: *raw.HumanLabel}
		if raw.JudgeScore != nil {
			rec.JudgeScore = *raw.JudgeScore
			rec.JudgeKnown = true
		}
		out = append(out, rec)
	}
	if len(out) == 0 {
		return nil, errors.New("JSONL contained no labeled rows")
	}
	return out, nil
}
