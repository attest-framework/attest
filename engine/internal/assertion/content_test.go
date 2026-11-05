package assertion

import (
	"encoding/json"
	"testing"

	"github.com/attest-framework/attest/engine/pkg/types"
)

func TestContentEvaluator(t *testing.T) {
	evaluator := &ContentEvaluator{}

	makeTrace := func(message string) *types.Trace {
		output, _ := json.Marshal(map[string]string{"message": message})
		return &types.Trace{
			TraceID: "trc_test",
			Output:  output,
		}
	}

	makeAssertion := func(spec string) *types.Assertion {
		return &types.Assertion{
			AssertionID: "assert_test",
			Type:        types.TypeContent,
			Spec:        json.RawMessage(spec),
		}
	}

	tests := []struct {
		name       string
		trace      *types.Trace
		spec       string
		wantStatus string
	}{
		// contains
		{
			name:       "contains passes",
			trace:      makeTrace("Hello, World!"),
			spec:       `{"target":"output.message","check":"contains","value":"World"}`,
			wantStatus: types.StatusPass,
		},
		{
			name:       "contains fails",
			trace:      makeTrace("Hello, World!"),
			spec:       `{"target":"output.message","check":"contains","value":"Goodbye"}`,
			wantStatus: types.StatusHardFail,
		},
		{
			name:       "contains case insensitive passes",
			trace:      makeTrace("Hello, World!"),
			spec:       `{"target":"output.message","check":"contains","value":"world","case_sensitive":false}`,
			wantStatus: types.StatusPass,
		},
		{
			name:       "contains case sensitive fails on wrong case",
			trace:      makeTrace("Hello, World!"),
			spec:       `{"target":"output.message","check":"contains","value":"world","case_sensitive":true}`,
			wantStatus: types.StatusHardFail,
		},
		{
			name:       "contains soft fail",
			trace:      makeTrace("Hello, World!"),
			spec:       `{"target":"output.message","check":"contains","value":"Goodbye","soft":true}`,
			wantStatus: types.StatusSoftFail,
		},

		// not_contains
		{
			name:       "not_contains passes",
			trace:      makeTrace("Hello, World!"),
			spec:       `{"target":"output.message","check":"not_contains","value":"Goodbye"}`,
			wantStatus: types.StatusPass,
		},
		{
			name:       "not_contains fails",
			trace:      makeTrace("Hello, World!"),
			spec:       `{"target":"output.message","check":"not_contains","value":"World"}`,
			wantStatus: types.StatusHardFail,
		},
		{
			name:       "not_contains soft fail",
			trace:      makeTrace("Hello, World!"),
			spec:       `{"target":"output.message","check":"not_contains","value":"World","soft":true}`,
			wantStatus: types.StatusSoftFail,
		},

		// regex_match
		{
			name:       "regex_match passes",
			trace:      makeTrace("Order #12345 confirmed"),
			spec:       `{"target":"output.message","check":"regex_match","value":"Order #\\d+"}`,
			wantStatus: types.StatusPass,
		},
		{
			name:       "regex_match fails",
			trace:      makeTrace("No order here"),
			spec:       `{"target":"output.message","check":"regex_match","value":"Order #\\d+"}`,
			wantStatus: types.StatusHardFail,
		},
		{
			name:       "regex_match invalid regex fails",
			trace:      makeTrace("anything"),
			spec:       `{"target":"output.message","check":"regex_match","value":"[invalid"}`,
			wantStatus: types.StatusHardFail,
		},

		// keyword_all
		{
			name:       "keyword_all passes when all present",
			trace:      makeTrace("The quick brown fox jumps"),
			spec:       `{"target":"output.message","check":"keyword_all","values":["quick","brown","fox"]}`,
			wantStatus: types.StatusPass,
		},
		{
			name:       "keyword_all fails when some missing",
			trace:      makeTrace("The quick fox jumps"),
			spec:       `{"target":"output.message","check":"keyword_all","values":["quick","brown","fox"]}`,
			wantStatus: types.StatusHardFail,
		},
		{
			name:       "keyword_all case insensitive passes",
			trace:      makeTrace("The Quick Brown Fox"),
			spec:       `{"target":"output.message","check":"keyword_all","values":["quick","brown"],"case_sensitive":false}`,
			wantStatus: types.StatusPass,
		},
		{
			name:       "keyword_all soft fail",
			trace:      makeTrace("only quick here"),
			spec:       `{"target":"output.message","check":"keyword_all","values":["quick","brown","fox"],"soft":true}`,
			wantStatus: types.StatusSoftFail,
		},

		// keyword_any
		{
			name:       "keyword_any passes on first match",
			trace:      makeTrace("The quick brown fox"),
			spec:       `{"target":"output.message","check":"keyword_any","values":["quick","missing"]}`,
			wantStatus: types.StatusPass,
		},
		{
			name:       "keyword_any fails when none match",
			trace:      makeTrace("The slow green turtle"),
			spec:       `{"target":"output.message","check":"keyword_any","values":["quick","brown","fox"]}`,
			wantStatus: types.StatusHardFail,
		},
		{
			name:       "keyword_any soft fail",
			trace:      makeTrace("nothing matches"),
			spec:       `{"target":"output.message","check":"keyword_any","values":["quick","brown"],"soft":true}`,
			wantStatus: types.StatusSoftFail,
		},

		// forbidden
		{
			name:       "forbidden passes when none present",
			trace:      makeTrace("This is safe content"),
			spec:       `{"target":"output.message","check":"forbidden","values":["badword","slur"]}`,
			wantStatus: types.StatusPass,
		},
		{
			name:       "forbidden fails when term found",
			trace:      makeTrace("This contains a badword"),
			spec:       `{"target":"output.message","check":"forbidden","values":["badword","slur"]}`,
			wantStatus: types.StatusHardFail,
		},
		{
			name:       "forbidden always hard_fail even with soft=true",
			trace:      makeTrace("This contains a badword"),
			spec:       `{"target":"output.message","check":"forbidden","values":["badword"],"soft":true}`,
			wantStatus: types.StatusHardFail,
		},
		{
			name:       "forbidden case insensitive fails",
			trace:      makeTrace("This contains a BADWORD"),
			spec:       `{"target":"output.message","check":"forbidden","values":["badword"],"case_sensitive":false}`,
			wantStatus: types.StatusHardFail,
		},

		// error cases
		{
			name:       "invalid target fails",
			trace:      makeTrace("hello"),
			spec:       `{"target":"nonexistent.field","check":"contains","value":"hello"}`,
			wantStatus: types.StatusHardFail,
		},
		{
			name:       "invalid spec json fails",
			trace:      makeTrace("hello"),
			spec:       `{not valid json`,
			wantStatus: types.StatusHardFail,
		},
		{
			name:       "unknown check type fails",
			trace:      makeTrace("hello"),
			spec:       `{"target":"output.message","check":"unknown_check","value":"hello"}`,
			wantStatus: types.StatusHardFail,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertion := makeAssertion(tt.spec)
			result := evaluator.Evaluate(tt.trace, assertion)
			if result.Status != tt.wantStatus {
				t.Errorf("got status %q, want %q; explanation: %s", result.Status, tt.wantStatus, result.Explanation)
			}
		})
	}
}
