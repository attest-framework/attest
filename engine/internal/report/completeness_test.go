package report_test

import (
	"strings"
	"testing"

	"github.com/attest-ai/attest/engine/internal/report"
)

func TestCheckComplete(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"collected with no tests", `{"tests":[],"tests_collected":4}`, "tests_collected is 4"},
		{"collected, tests absent", `{"tests_collected":2}`, "tests_collected is 2"},
		{"collected absent", `{"tests":[]}`, ""},
		{"collected zero", `{"tests":[],"tests_collected":0}`, ""},
		{"collected with tests", `{"tests":[{"test_id":"t"}],"tests_collected":1}`, ""},
		{"not JSON", `nope`, "decode report completeness"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := report.CheckComplete([]byte(tc.body))
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}
