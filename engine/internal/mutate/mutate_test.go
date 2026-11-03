package mutate_test

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/attest-ai/attest/engine/internal/capture"
	"github.com/attest-ai/attest/engine/internal/mutate"
	"github.com/attest-ai/attest/engine/internal/trace"
	"github.com/attest-ai/attest/engine/pkg/types"
)

const fixturePath = "../../testdata/mutate/inc-1042.attest.json"

func loadSubject(t *testing.T) (mutate.Subject, *capture.Fixture) {
	t.Helper()
	fx, err := capture.LoadFixture(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	return mutate.Subject{Name: fx.Name, Trace: fx.Trace, World: fx.World}, fx
}

func draftedSpecs(fx *capture.Fixture) []mutate.Spec {
	var out []mutate.Spec
	for _, c := range fx.Contracts {
		out = append(out, mutate.Spec{AssertionID: c.ID, Type: c.Type, Spec: c.Spec})
	}
	return out
}

// outputOnly stands in for tests that look only at what the agent said.
func outputOnly() []mutate.Spec {
	return []mutate.Spec{
		{AssertionID: "out-schema", TestID: "test_refund_desk.py::test_output_is_object", Type: "schema",
			Spec: json.RawMessage(`{"target":"output","schema":{"type":"object"}}`)},
		{AssertionID: "tokens", TestID: "test_refund_desk.py::test_tokens", Type: "constraint",
			Spec: json.RawMessage(`{"field":"metadata.total_tokens","operator":"lte","value":5000}`)},
	}
}

func find(t *testing.T, rep *mutate.Report, operator mutate.Operator, desc string) mutate.MutantResult {
	t.Helper()
	for _, m := range rep.Mutants {
		if m.Operator == operator && strings.HasPrefix(m.Description, desc) {
			return m
		}
	}
	t.Fatalf("no %s mutant starting %q in %d mutants", operator, desc, len(rep.Mutants))
	return mutate.MutantResult{}
}

func TestGenerate_FixedSeed_IdenticalMutantSet(t *testing.T) {
	s, _ := loadSubject(t)
	for _, max := range []int{0, 7} {
		a, err := mutate.Generate([]mutate.Subject{s}, mutate.Options{Seed: 42, MaxMutants: max})
		if err != nil {
			t.Fatal(err)
		}
		b, err := mutate.Generate([]mutate.Subject{s}, mutate.Options{Seed: 42, MaxMutants: max})
		if err != nil {
			t.Fatal(err)
		}
		if len(a) == 0 || len(a) != len(b) {
			t.Fatalf("max %d: %d vs %d mutants", max, len(a), len(b))
		}
		for i := range a {
			if a[i].ID != b[i].ID || a[i].Description != b[i].Description {
				t.Fatalf("max %d: mutant %d differs: %s / %s", max, i, a[i].ID, b[i].ID)
			}
		}
	}
}

func TestGenerate_MaxMutants_CapsAndSeedChoosesSample(t *testing.T) {
	s, _ := loadSubject(t)
	all, err := mutate.Generate([]mutate.Subject{s}, mutate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ids := func(seed uint64) []string {
		ms, err := mutate.Generate([]mutate.Subject{s}, mutate.Options{Seed: seed, MaxMutants: 5})
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, m := range ms {
			out = append(out, m.ID)
		}
		return out
	}
	if len(all) <= 5 {
		t.Fatalf("fixture yields %d mutants; the cap test needs more than 5", len(all))
	}
	one, other := ids(1), ids(2)
	if len(one) != 5 || len(other) != 5 {
		t.Fatalf("cap not applied: %d, %d", len(one), len(other))
	}
	if reflect.DeepEqual(one, other) {
		t.Errorf("seeds 1 and 2 chose the same sample %v", one)
	}
	pos := map[string]int{}
	for i, m := range all {
		pos[m.ID] = i
	}
	for i := 1; i < len(one); i++ {
		if pos[one[i]] <= pos[one[i-1]] {
			t.Errorf("sample not in enumeration order: %v", one)
		}
	}
}

func TestGenerate_Operators_CoverEveryKindInOrder(t *testing.T) {
	s, _ := loadSubject(t)
	ms, err := mutate.Generate([]mutate.Subject{s}, mutate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	rank := map[mutate.Operator]int{}
	for i, op := range mutate.Operators {
		rank[op] = i
	}
	seen := map[mutate.Operator]bool{}
	last := -1
	for _, m := range ms {
		r := rank[m.Operator]
		if r < last {
			t.Errorf("%s follows a later operator", m.ID)
		}
		last = r
		seen[m.Operator] = true
		if !strings.HasPrefix(m.ID, "inc-1042:"+string(m.Operator)+":") {
			t.Errorf("mutant id %q is not <fixture>:<operator>:<site>", m.ID)
		}
	}
	// The fixture's output message is empty, so no truncate mutant exists.
	for _, op := range mutate.Operators {
		if op != mutate.TruncateOutput && !seen[op] {
			t.Errorf("no %s mutant generated", op)
		}
	}
}

func TestGenerate_DoesNotModifySubject(t *testing.T) {
	s, _ := loadSubject(t)
	before, _ := json.Marshal(s.Trace)
	if _, err := mutate.Generate([]mutate.Subject{s}, mutate.Options{}); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(s.Trace)
	if string(before) != string(after) {
		t.Fatal("Generate changed the subject trace")
	}
}

func TestRun_OutputOnlySpecs_DuplicateIssueRefundSurvives(t *testing.T) {
	s, _ := loadSubject(t)
	rep, err := mutate.Run([]mutate.Subject{s}, outputOnly(), mutate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	m := find(t, rep, mutate.DuplicateToolCall, "duplicate issue_refund")
	if m.Status != mutate.StatusSurvived {
		t.Fatalf("duplicate issue_refund: status %s, killed by %v", m.Status, m.KilledBy)
	}
	var sg *mutate.Suggestion
	for i := range rep.SurvivorsSuggestions {
		if rep.SurvivorsSuggestions[i].MutantID == m.ID {
			sg = &rep.SurvivorsSuggestions[i]
		}
	}
	if sg == nil {
		t.Fatal("survivor has no suggested contract")
	}
	var spec map[string]any
	if err := json.Unmarshal(sg.Contract.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	if sg.Contract.Type != "trace" || spec["check"] != "tool_cardinality" || spec["tool"] != "issue_refund" || spec["per"] != "order_id" {
		t.Errorf("suggestion = %s %s", sg.Contract.Type, sg.Contract.Spec)
	}
}

func TestRun_CardinalitySpec_KillsDuplicateIssueRefund(t *testing.T) {
	s, fx := loadSubject(t)
	rep, err := mutate.Run([]mutate.Subject{s}, append(outputOnly(), draftedSpecs(fx)...), mutate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	m := find(t, rep, mutate.DuplicateToolCall, "duplicate issue_refund")
	if m.Status != mutate.StatusKilled {
		t.Fatalf("status %s", m.Status)
	}
	ids := map[string]bool{}
	for _, k := range m.KilledBy {
		ids[k.AssertionID] = true
	}
	if !ids["cardinality-issue_refund-per-order_id"] || ids["out-schema"] {
		t.Errorf("killed_by = %v", ids)
	}
}

func TestRun_EffectCountSpec_KillsWorldDuplicate(t *testing.T) {
	s, fx := loadSubject(t)
	effect := mutate.Spec{
		AssertionID: "one-refund", TestID: "test_refund_desk.py::test_one_refund", Type: "effect",
		Spec: json.RawMessage(`{"check":"count","resource":"refunds","where":{"order_id":"O-1042"},"equals":1,"world":` + string(fx.World) + `}`),
	}
	without, err := mutate.Run([]mutate.Subject{s}, append(outputOnly(), draftedSpecs(fx)...), mutate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	with, err := mutate.Run([]mutate.Subject{s}, append(append(outputOnly(), draftedSpecs(fx)...), effect), mutate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	const desc = "duplicate refunds record 0"
	if m := find(t, without, mutate.AlterWorldEffect, desc); m.Status != mutate.StatusSurvived {
		t.Fatalf("without the effect spec: %s", m.Status)
	}
	m := find(t, with, mutate.AlterWorldEffect, desc)
	if m.Status != mutate.StatusKilled || len(m.KilledBy) != 1 || m.KilledBy[0].AssertionID != "one-refund" {
		t.Fatalf("with the effect spec: %s %v", m.Status, m.KilledBy)
	}
	if strings.Contains(string(m.KilledBy[0].Spec), `"world"`) {
		t.Errorf("reported effect spec still embeds the world: %s", m.KilledBy[0].Spec)
	}
	if with.Score == nil || without.Score == nil || *with.Score <= *without.Score {
		t.Errorf("score did not rise: %v -> %v", without.Score, with.Score)
	}
}

func TestRun_EffectSpecOfOtherWorld_IsNotApplicable(t *testing.T) {
	s, fx := loadSubject(t)
	var world map[string]json.RawMessage
	if err := json.Unmarshal(fx.World, &world); err != nil {
		t.Fatal(err)
	}
	world["definition_sha256"] = json.RawMessage(`"` + strings.Repeat("0", 64) + `"`)
	otherWorld, err := json.Marshal(world)
	if err != nil {
		t.Fatal(err)
	}
	other := string(otherWorld)
	effect := mutate.Spec{AssertionID: "x", Type: "effect",
		Spec: json.RawMessage(`{"check":"count","resource":"refunds","equals":1,"world":` + other + `}`)}
	rep, err := mutate.Run([]mutate.Subject{s}, []mutate.Spec{effect}, mutate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.ApplicableSpecs != 0 || rep.Score != nil {
		t.Errorf("applicable %d, score %v", rep.ApplicableSpecs, rep.Score)
	}
	for _, m := range rep.Mutants {
		if m.Status != mutate.StatusNoApplicableSpecs {
			t.Fatalf("%s: %s", m.ID, m.Status)
		}
	}
}

func TestRun_JudgeSpec_IsSkippedAndCounted(t *testing.T) {
	s, _ := loadSubject(t)
	specs := append(outputOnly(), mutate.Spec{AssertionID: "j", Type: "llm_judge", Spec: json.RawMessage(`{}`)})
	rep, err := mutate.Run([]mutate.Subject{s}, specs, mutate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.SkippedSpecs != 1 || rep.ApplicableSpecs != 2 {
		t.Errorf("skipped %d, applicable %d", rep.SkippedSpecs, rep.ApplicableSpecs)
	}
}

func TestRun_Suggestions_KillTheirMutants(t *testing.T) {
	s, _ := loadSubject(t)
	rep, err := mutate.Run([]mutate.Subject{s}, outputOnly(), mutate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	byOp := map[mutate.Operator]int{}
	ids := map[string]mutate.Operator{}
	for _, m := range rep.Mutants {
		ids[m.ID] = m.Operator
	}
	for _, sg := range rep.SurvivorsSuggestions {
		byOp[ids[sg.MutantID]]++
	}
	for _, op := range []mutate.Operator{mutate.DropToolCall, mutate.DuplicateToolCall, mutate.ReorderToolCalls, mutate.PerturbArgument, mutate.InjectToolError, mutate.AlterWorldEffect} {
		if byOp[op] == 0 {
			t.Errorf("no verified suggestion for %s", op)
		}
	}
	// Adopting every suggestion must kill every mutant that had one.
	var adopted []mutate.Spec
	for _, sg := range rep.SurvivorsSuggestions {
		spec := sg.Contract.Spec
		if sg.Contract.Type == "effect" {
			var f map[string]json.RawMessage
			_ = json.Unmarshal(spec, &f)
			f["world"] = s.World
			spec, _ = json.Marshal(f)
		}
		adopted = append(adopted, mutate.Spec{AssertionID: sg.MutantID, Type: sg.Contract.Type, Spec: spec})
	}
	after, err := mutate.Run([]mutate.Subject{s}, append(outputOnly(), adopted...), mutate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	k0, _ := rep.Counts()
	k1, _ := after.Counts()
	if k1 < k0+len(rep.SurvivorsSuggestions) {
		t.Errorf("killed %d -> %d after adopting %d suggestions", k0, k1, len(rep.SurvivorsSuggestions))
	}
}

func TestGenerate_DuplicateFixtureName_Errors(t *testing.T) {
	s, _ := loadSubject(t)
	if _, err := mutate.Generate([]mutate.Subject{s, s}, mutate.Options{}); err == nil {
		t.Fatal("two fixtures with one name were accepted")
	}
}

func TestMain(m *testing.M) {
	if _, err := os.Stat(fixturePath); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func TestGenerate_ArgumentThatOverflowsWhenDoubled_IsNotPerturbed(t *testing.T) {
	s, _ := loadSubject(t)
	s.Trace.Steps = append([]types.Step(nil), s.Trace.Steps...)
	for i := range s.Trace.Steps {
		if s.Trace.Steps[i].Type == types.StepTypeToolCall {
			s.Trace.Steps[i].Args = json.RawMessage(`{"big":1e308,"small":3}`)
		}
	}

	ms, err := mutate.Generate([]mutate.Subject{s}, mutate.Options{})

	if err != nil {
		t.Fatal(err)
	}
	var big, small int
	for _, m := range ms {
		if m.Operator != mutate.PerturbArgument {
			continue
		}
		if strings.Contains(m.ID, ".big") {
			big++
		}
		if strings.Contains(m.ID, ".small") {
			small++
		}
	}
	if big != 0 || small == 0 {
		t.Errorf("big mutants %d (want 0), small mutants %d (want > 0)", big, small)
	}
}

// retryTrace calls lookup twice in a row, then issue fails once and is
// retried, then notify.
func retryTrace() mutate.Subject {
	one := func(name, args string) types.Step {
		return types.Step{Type: types.StepTypeToolCall, Name: name, Args: json.RawMessage(args), Result: json.RawMessage(`{"ok":true}`)}
	}
	steps := []types.Step{
		one("lookup", `{"order_id":"A"}`),
		one("lookup", `{"order_id":"A"}`),
		one("issue", `{"order_id":"A"}`),
		one("issue", `{"order_id":"A"}`),
		one("notify", `{"order_id":"A"}`),
	}
	steps[2].Error = &types.StepError{Type: "tool_error", Message: "boom"}
	zero := 2
	steps[3].RetryOf = &zero
	return mutate.Subject{Name: "retry", Trace: &types.Trace{
		SchemaVersion: 2, TraceID: "trc_retry", AgentID: "a", Input: json.RawMessage(`{}`), Output: json.RawMessage(`{"message":"done"}`),
		Steps: steps, Turns: []types.Turn{{Index: 0, StepIndices: []int{0, 1, 2, 3, 4}}},
	}}
}

func TestGenerate_AdjacentCallsOfOneTool_AreNotSwapped(t *testing.T) {
	ms, err := mutate.Generate([]mutate.Subject{retryTrace()}, mutate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var swaps []string
	for _, m := range ms {
		if m.Operator == mutate.ReorderToolCalls {
			swaps = append(swaps, m.Description)
		}
	}
	// Pairs: lookup/lookup, lookup/issue, issue/issue (a failure and its
	// retry), issue/notify. Only the two across tools remain.
	if len(swaps) != 2 || swaps[0] != "swap lookup and issue" || swaps[1] != "swap issue and notify" {
		t.Errorf("reorder mutants = %v", swaps)
	}
}

func TestGenerate_EveryMutantTrace_PassesTraceValidation(t *testing.T) {
	inc, _ := loadSubject(t)
	for _, s := range []mutate.Subject{inc, retryTrace()} {
		if rpcErr := trace.Validate(s.Trace, 1); rpcErr != nil {
			t.Fatalf("%s: original trace is invalid: %v", s.Name, rpcErr.Message)
		}
		ms, err := mutate.Generate([]mutate.Subject{s}, mutate.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if len(ms) == 0 {
			t.Fatalf("%s: no mutants", s.Name)
		}
		for _, m := range ms {
			if rpcErr := trace.Validate(m.Trace, 1); rpcErr != nil {
				t.Errorf("%s: %s: %s", s.Name, m.ID, rpcErr.Message)
			}
		}
	}
}

func TestGenerate_DroppedRetryTarget_ClearsRetryOf(t *testing.T) {
	ms, err := mutate.Generate([]mutate.Subject{retryTrace()}, mutate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.ID == "retry:drop_tool_call:2" && m.Trace.Steps[2].RetryOf != nil {
			t.Fatalf("retry of a dropped step still points at %d", *m.Trace.Steps[2].RetryOf)
		}
	}
}

func TestRun_InjectErrorSuggestion_PassesOnTraceThatAlreadyHasFailures(t *testing.T) {
	s := retryTrace()
	rep, err := mutate.Run([]mutate.Subject{s}, []mutate.Spec{{AssertionID: "o", Type: "schema", Spec: json.RawMessage(`{"target":"output","schema":{"type":"object"}}`)}}, mutate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, sg := range rep.SurvivorsSuggestions {
		if strings.Contains(sg.MutantID, ":inject_tool_error:") {
			found = true
			if !strings.Contains(string(sg.Contract.Spec), `"max":1`) {
				t.Errorf("suggestion = %s, want max 1 (the original's longest streak)", sg.Contract.Spec)
			}
		}
	}
	if !found {
		t.Error("no inject_tool_error suggestion")
	}
}
