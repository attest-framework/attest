package capture

import (
	"fmt"
	"strings"
)

// Capture outcomes, reported as `outcome` in the capture result.
const (
	// OutcomeVerified: every drafted contract held (or, with --expect-fail,
	// the incident reproduced as recorded).
	OutcomeVerified = "verified"
	// OutcomeNotVerified: the capture failed a check or has no evidence; the
	// fixture is still written but must not be trusted. `attest capture`
	// exits 1.
	OutcomeNotVerified = "not_verified"
	// OutcomeNothingToVerify: a healthy run (command succeeded, trace check
	// clean, model or tool steps present) with nothing to draft a contract
	// from. The fixture is written with verified false; `attest capture`
	// exits 0.
	OutcomeNothingToVerify = "nothing_to_verify"
)

// NothingToDraft is the cause text for a trace with no tool call, structured
// output or cost to draft a contract from.
const NothingToDraft = "no tool call, structured output or cost to draft a contract from"

// OutcomeOf classifies a built fixture. For a not_verified fixture it also
// returns every cause that holds, so a failure is never hidden behind another.
func OutcomeOf(f *Fixture) (string, []string) {
	v := f.Verification
	if v.Verified {
		return OutcomeVerified, nil
	}
	var causes []string
	steps := HasModelOrToolSteps(f.Trace)
	if !v.TraceCheck.OK {
		causes = append(causes, "the captured trace fails trace check")
	}
	if !steps {
		causes = append(causes, noStepsCause)
	}
	if c := f.Source.ExitCode; c != nil && *c != 0 {
		causes = append(causes, fmt.Sprintf("the command exited %d", *c))
	}
	if f.Source.ReplayCapable && len(f.Exchanges) == 0 {
		causes = append(causes, "no model calls were recorded")
	}
	switch {
	case len(v.Results) == 0 && v.ExpectFail:
		causes = append(causes, "--expect-fail drafted no contract: "+NothingToDraft)
	case len(v.Results) == 0 && steps:
		if len(causes) == 0 {
			return OutcomeNothingToVerify, nil
		}
		causes = append(causes, "no contracts were drafted: "+NothingToDraft)
	case len(v.Results) > 0:
		if failed := failedContracts(f); len(failed) > 0 {
			msg := "failing " + strings.Join(failed, ", ")
			if !v.ExpectFail {
				msg += "; edit the drafted contracts or re-run with --expect-fail to save an incident reproduction"
			}
			causes = append(causes, msg)
		} else if v.ExpectFail && len(v.ExpectedFailures) == 0 {
			causes = append(causes, "--expect-fail given but every drafted contract passes")
		}
	}
	return OutcomeNotVerified, causes
}

func failedContracts(f *Fixture) []string {
	var failed []string
	for _, r := range f.Verification.Results {
		if r.Status == "pass" {
			continue
		}
		expected := false
		for _, e := range f.Verification.ExpectedFailures {
			if e == r.Contract {
				expected = true
			}
		}
		if !expected {
			failed = append(failed, r.Contract)
		}
	}
	return failed
}

// noStepsCause explains a fixture whose trace holds no model or tool step:
// spans arrived, but no llm_call or tool_call step was mapped from them.
const noStepsCause = "no llm_call or tool_call step was mapped from the spans, so there is nothing to verify. " +
	"Either no span follows the GenAI semantic conventions attest maps (gen_ai.operation.name chat, execute_tool, ...) " +
	"or only agent and retrieval spans do. Switch the framework's OpenTelemetry " +
	"instrumentation to one attest maps; see the Frameworks section of the capture guide (attest-engine guide capture)"
