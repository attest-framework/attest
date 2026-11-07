package policy_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/attest-framework/attest/engine/internal/policy"
	"github.com/attest-framework/attest/engine/internal/report"
)

func TestTestsFailedViolation(t *testing.T) {
	tests := []report.CITest{
		{Outcome: report.OutcomePassed},
		{Outcome: report.OutcomeFailed},
		{Outcome: report.OutcomeSkipped},
		{Outcome: report.OutcomeFailed},
	}
	for _, tc := range []struct {
		name string
		p    *policy.Policy
		want string
	}{
		{"no policy", nil, ""},
		{"default", &policy.Policy{}, policy.SeverityBlock},
		{"independent of global warn", &policy.Policy{Severity: policy.SeverityWarn}, policy.SeverityBlock},
		{"block", &policy.Policy{TestsFailed: "block"}, policy.SeverityBlock},
		{"warn", &policy.Policy{TestsFailed: "warn"}, policy.SeverityWarn},
		{"off", &policy.Policy{TestsFailed: "off"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, ok := policy.TestsFailedViolation(tc.p, tests)
			if ok != (tc.want != "") {
				t.Fatalf("violation present=%v, want severity %q: %+v", ok, tc.want, v)
			}
			if ok && (v.Rule != "tests_failed" || v.Severity != tc.want || !strings.Contains(v.Detail, "2 tests failed")) {
				t.Fatalf("unexpected violation: %+v", v)
			}
		})
	}
	for _, tests := range [][]report.CITest{nil, {}, {{Outcome: report.OutcomePassed}, {Outcome: report.OutcomeSkipped}}} {
		if v, ok := policy.TestsFailedViolation(&policy.Policy{}, tests); ok {
			t.Fatalf("non-failed tests triggered violation: %+v", v)
		}
	}
}

func TestTestsFailedPolicyLoad(t *testing.T) {
	for _, format := range []string{"yaml", "json"} {
		for _, value := range []string{"", "block", "warn", "off", "blokc", "ignore"} {
			t.Run(format+"/"+value, func(t *testing.T) {
				body := "tests_failed: " + `"` + value + `"` + "\n"
				if format == "json" {
					body = `{"tests_failed":"` + value + `"}`
				}
				path := filepath.Join(t.TempDir(), "policy."+format)
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
				p, err := policy.Load(path)
				valid := value == "" || value == "block" || value == "warn" || value == "off"
				if valid {
					if err != nil || p.TestsFailed != value {
						t.Fatalf("load %s: policy=%+v, error=%v", body, p, err)
					}
				} else if err == nil || !strings.Contains(err.Error(), "tests_failed") {
					t.Fatalf("want error naming tests_failed, got: %v", err)
				}
			})
		}
	}
}
