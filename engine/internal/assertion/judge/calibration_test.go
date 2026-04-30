package judge_test

import (
	"math"
	"strings"
	"testing"

	"github.com/attest-ai/attest/engine/internal/assertion/judge"
)

func TestComputeAgreement_PerfectAgreement(t *testing.T) {
	pairs := []judge.LabelPair{
		{Human: 0.9, Judge: 0.9},
		{Human: 0.8, Judge: 0.8},
		{Human: 0.2, Judge: 0.2},
		{Human: 0.1, Judge: 0.1},
	}
	got, err := judge.ComputeAgreement(pairs, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if got.Agreement != 1.0 {
		t.Errorf("expected agreement 1.0, got %f", got.Agreement)
	}
	if got.CohenKappa != 1.0 {
		t.Errorf("expected κ=1.0, got %f", got.CohenKappa)
	}
	if got.ROCAUC != 1.0 {
		t.Errorf("expected AUC=1.0, got %f", got.ROCAUC)
	}
	if got.N != 4 {
		t.Errorf("expected N=4, got %d", got.N)
	}
}

func TestComputeAgreement_RandomAgreement(t *testing.T) {
	// 50/50 split with judge agreeing on 50% — κ should be ~0.
	pairs := []judge.LabelPair{
		{Human: 0.9, Judge: 0.9}, // both pos
		{Human: 0.9, Judge: 0.1}, // disagree
		{Human: 0.1, Judge: 0.9}, // disagree
		{Human: 0.1, Judge: 0.1}, // both neg
	}
	got, err := judge.ComputeAgreement(pairs, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if got.Agreement != 0.5 {
		t.Errorf("expected agreement 0.5, got %f", got.Agreement)
	}
	// expected agreement = (2/4 * 2/4 + 2/4 * 2/4) = 0.5; κ = (0.5-0.5)/(1-0.5) = 0
	if math.Abs(got.CohenKappa) > 1e-9 {
		t.Errorf("expected κ≈0, got %f", got.CohenKappa)
	}
}

func TestComputeAgreement_RejectsEmpty(t *testing.T) {
	_, err := judge.ComputeAgreement(nil, 0.5)
	if err == nil {
		t.Fatal("expected error for empty pairs")
	}
}

func TestComputeAgreement_RejectsBadThreshold(t *testing.T) {
	pairs := []judge.LabelPair{{Human: 0.5, Judge: 0.5}}
	for _, th := range []float64{0, 1, -0.1, 1.1} {
		if _, err := judge.ComputeAgreement(pairs, th); err == nil {
			t.Errorf("expected error for threshold=%f", th)
		}
	}
}

func TestComputeAgreement_OneClassNoAUC(t *testing.T) {
	// All human labels positive — AUC undefined, but agreement/κ still computed.
	pairs := []judge.LabelPair{
		{Human: 0.9, Judge: 0.9},
		{Human: 0.8, Judge: 0.7},
		{Human: 0.85, Judge: 0.6},
	}
	got, err := judge.ComputeAgreement(pairs, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if got.ROCAUC != 0 {
		t.Errorf("expected AUC=0 (omitted) when single class, got %f", got.ROCAUC)
	}
	if got.Agreement <= 0 {
		t.Error("agreement should still be computed when AUC is undefined")
	}
}

func TestLoadLabelsCSV_HeaderAndDataRows(t *testing.T) {
	src := `input,human_label,judge_score
hello,0.9,0.85
world,0.1,0.2
# comment row
just-input,0.5,
`
	got, err := judge.LoadLabelsCSV(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(got))
	}
	if !got[0].JudgeKnown || got[0].JudgeScore != 0.85 {
		t.Errorf("row 0: judge_score not parsed: %+v", got[0])
	}
	if got[2].JudgeKnown {
		t.Errorf("row 2 had empty judge_score; should be JudgeKnown=false")
	}
}

func TestLoadLabelsCSV_NoHeader(t *testing.T) {
	src := "hello,0.9\nworld,0.1\n"
	got, err := judge.LoadLabelsCSV(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 rows, got %d", len(got))
	}
}

func TestLoadLabelsCSV_RejectsBadFloat(t *testing.T) {
	src := "input,label\nhello,not_a_number\n"
	_, err := judge.LoadLabelsCSV(strings.NewReader(src))
	if err == nil {
		t.Fatal("expected error for non-float human_label")
	}
}

func TestLoadLabelsJSONL_Shape(t *testing.T) {
	src := `{"input": "a", "human_label": 0.9, "judge_score": 0.85}
{"input": "b", "human_label": 0.1}
`
	got, err := judge.LoadLabelsJSONL(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(got))
	}
	if !got[0].JudgeKnown {
		t.Error("first row should have JudgeKnown=true")
	}
	if got[1].JudgeKnown {
		t.Error("second row should have JudgeKnown=false")
	}
}

func TestLoadLabelsJSONL_RejectsMissingHumanLabel(t *testing.T) {
	src := `{"input": "a"}` + "\n"
	_, err := judge.LoadLabelsJSONL(strings.NewReader(src))
	if err == nil {
		t.Fatal("expected error for missing human_label")
	}
}
