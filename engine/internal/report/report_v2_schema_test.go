package report

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/attest-ai/attest/engine/pkg/types"
)

// report-v2 (contract C3): the schema in proto/attest/v1 accepts every
// protocol-tests/report-v2/valid fixture, rejects every invalid one, and
// accepts what GenerateJSONReport emits. The Python and TypeScript SDKs run
// the same fixtures against the reports their test runners write.
const (
	reportV2SchemaPath = "../../../proto/attest/v1/report-v2.schema.json"
	reportV2Fixtures   = "../../../protocol-tests/report-v2"
)

func compileReportV2Schema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	f, err := os.Open(reportV2SchemaPath)
	if err != nil {
		t.Fatalf("open schema: %v", err)
	}
	defer f.Close()
	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	if err := c.AddResource("report-v2.schema.json", doc); err != nil {
		t.Fatalf("add schema: %v", err)
	}
	s, err := c.Compile("report-v2.schema.json")
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	return s
}

func validateReportV2(s *jsonschema.Schema, data []byte) error {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return err
	}
	return s.Validate(doc)
}

func TestReportV2Fixtures(t *testing.T) {
	s := compileReportV2Schema(t)
	for _, kind := range []string{"valid", "invalid"} {
		paths, err := filepath.Glob(filepath.Join(reportV2Fixtures, kind, "*.json"))
		if err != nil || len(paths) == 0 {
			t.Fatalf("no %s report-v2 fixtures (err=%v)", kind, err)
		}
		for _, p := range paths {
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatalf("read %s: %v", p, err)
			}
			err = validateReportV2(s, data)
			if kind == "valid" && err != nil {
				t.Errorf("%s: want valid, got %v", filepath.Base(p), err)
			}
			if kind == "invalid" && err == nil {
				t.Errorf("%s: want invalid, schema accepted it", filepath.Base(p))
			}
		}
	}
}

func TestGenerateJSONReportMatchesReportV2Schema(t *testing.T) {
	s := compileReportV2Schema(t)
	results := []types.AssertionResult{
		{AssertionID: "a1", Status: types.StatusPass, Score: 1, Layer: 4, Type: "content", DurationMS: 2},
		{AssertionID: "a2", Status: types.StatusHardFail, Layer: 3, Type: "trace", Expected: "x", Actual: "y", FailureClass: "broken_code"},
		{AssertionID: "a3", Status: types.StatusNotEvaluated, Reason: types.ReasonProviderNotConfigured, Layer: 6, Type: "llm_judge"},
	}
	out, err := GenerateJSONReport(results, 0, 5)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if err := validateReportV2(s, out); err != nil {
		t.Fatalf("generated report does not match report-v2: %v\n%s", err, out)
	}
}
