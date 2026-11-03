package assertion

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/attest-ai/attest/engine/pkg/types"
)

// contractDSLFixture mirrors protocol-tests/contracts/dsl.json. The Python
// and TypeScript SDKs check that their DSL calls emit Spec; the engine checks
// that Spec evaluates to the listed results.
type contractDSLFixture struct {
	Traces map[string]string `json:"traces"`
	Cases  []struct {
		Name    string          `json:"name"`
		Spec    json.RawMessage `json:"spec"`
		Results []struct {
			Trace       string  `json:"trace"`
			Status      string  `json:"status"`
			Explanation *string `json:"explanation"`
		} `json:"results"`
	} `json:"cases"`
}

func TestContractDSLConformance(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "protocol-tests", "contracts", "dsl.json"))
	if err != nil {
		t.Fatalf("read dsl.json: %v", err)
	}
	var fixture contractDSLFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode dsl.json: %v", err)
	}
	if len(fixture.Cases) == 0 {
		t.Fatal("dsl.json has no cases")
	}
	traces := map[string]*types.Trace{}
	for name, path := range fixture.Traces {
		traces[name] = loadContractTrace(t, path)
	}
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			if len(tc.Results) == 0 {
				t.Fatal("case has no results")
			}
			for _, want := range tc.Results {
				trace, ok := traces[want.Trace]
				if !ok {
					t.Fatalf("unknown trace %q", want.Trace)
				}
				got := evalTraceSpec(t, trace, string(tc.Spec))
				if got.Status != want.Status {
					t.Errorf("%s: status = %q, want %q; explanation: %s", want.Trace, got.Status, want.Status, got.Explanation)
				}
				if want.Explanation != nil && got.Explanation != *want.Explanation {
					t.Errorf("%s: explanation = %q, want %q", want.Trace, got.Explanation, *want.Explanation)
				}
			}
		})
	}
}
