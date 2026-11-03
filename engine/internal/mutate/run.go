package mutate

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/attest-ai/attest/engine/internal/assertion"
	"github.com/attest-ai/attest/engine/pkg/types"
)

// Mutant statuses.
const (
	StatusKilled   = "killed"
	StatusSurvived = "survived"
	// StatusNoApplicableSpecs marks a mutant of a fixture none of whose specs
	// pass on the unmutated fixture; it says nothing about the suite and is
	// left out of the score.
	StatusNoApplicableSpecs = "no_applicable_specs"
)

// Spec is one assertion spec the suite evaluates.
type Spec struct {
	AssertionID string
	Type        string
	Spec        json.RawMessage
	TestID      string
	File        string
	Line        int
}

// Kill names a spec that hard-failed on a mutant.
type Kill struct {
	AssertionID string          `json:"assertion_id"`
	Type        string          `json:"type"`
	TestID      string          `json:"test_id,omitempty"`
	Spec        json.RawMessage `json:"spec"`
}

// MutantResult is the verdict on one mutant.
type MutantResult struct {
	ID          string   `json:"id"`
	Fixture     string   `json:"fixture"`
	Operator    Operator `json:"operator"`
	Description string   `json:"description"`
	Status      string   `json:"status"`
	KilledBy    []Kill   `json:"killed_by"`
}

// Suggestion is a contract that kills a surviving mutant.
type Suggestion struct {
	MutantID    string   `json:"mutant_id"`
	Contract    Contract `json:"contract"`
	Description string   `json:"description"`
}

// Report is the outcome of a mutation run.
type Report struct {
	// Score is killed / (killed + survived); nil when no mutant has an
	// applicable spec.
	Score                *float64       `json:"score"`
	Seed                 uint64         `json:"seed"`
	Fixtures             []string       `json:"fixtures"`
	Mutants              []MutantResult `json:"mutants"`
	SurvivorsSuggestions []Suggestion   `json:"survivors_suggestions"`
	SkippedSpecs         int            `json:"skipped_specs"`
	ApplicableSpecs      int            `json:"applicable_specs"`
}

// Counts returns how many mutants were killed and how many survived.
func (r *Report) Counts() (killed, survived int) {
	for _, m := range r.Mutants {
		switch m.Status {
		case StatusKilled:
			killed++
		case StatusSurvived:
			survived++
		}
	}
	return killed, survived
}

// applicable is a spec that passes on its fixture's unmutated state.
type applicable struct {
	spec Spec
	idx  int
}

// Run generates the mutants of subjects and evaluates specs against each.
//
// Only deterministic specs run (trace, schema, constraint, content,
// trace_tree and effect); the rest are counted in SkippedSpecs. A spec counts
// for a fixture when it passes on the unmutated fixture: an effect spec
// additionally needs the same world definition as the fixture, and is then
// evaluated on the fixture's snapshot in place of the one it embeds. A mutant
// is killed when an applicable spec hard-fails on it.
func Run(subjects []Subject, specs []Spec, opts Options) (*Report, error) {
	mutants, err := Generate(subjects, opts)
	if err != nil {
		return nil, err
	}
	reg := assertion.NewRegistry()
	var runnable []Spec
	skipped := 0
	for _, sp := range specs {
		if reg.HasEvaluator(sp.Type) {
			runnable = append(runnable, sp)
		} else {
			skipped++
		}
	}

	rep := &Report{
		Seed: opts.Seed, SkippedSpecs: skipped,
		Fixtures: []string{}, Mutants: []MutantResult{}, SurvivorsSuggestions: []Suggestion{},
	}
	subj := map[string]Subject{}
	for _, s := range subjects {
		subj[s.Name] = s
		rep.Fixtures = append(rep.Fixtures, s.Name)
	}
	sort.Strings(rep.Fixtures)

	applic := map[string][]applicable{}
	used := map[int]bool{}
	for _, name := range rep.Fixtures {
		s := subj[name]
		for i, sp := range runnable {
			ok, err := passes(reg, sp, s.Trace, s.World, s.World)
			if err != nil {
				return nil, err
			}
			if ok {
				applic[name] = append(applic[name], applicable{spec: sp, idx: i})
				used[i] = true
			}
		}
	}
	rep.ApplicableSpecs = len(used)

	for _, m := range mutants {
		s := subj[m.Fixture]
		res := MutantResult{ID: m.ID, Fixture: m.Fixture, Operator: m.Operator, Description: m.Description, KilledBy: []Kill{}}
		world := s.World
		if m.World != nil {
			world = m.World
		}
		list := applic[m.Fixture]
		for _, a := range list {
			hard, err := hardFails(reg, a.spec, m.Trace, s.World, world)
			if err != nil {
				return nil, err
			}
			if hard {
				res.KilledBy = append(res.KilledBy, Kill{
					AssertionID: a.spec.AssertionID, Type: a.spec.Type, TestID: a.spec.TestID, Spec: reportSpec(a.spec),
				})
			}
		}
		switch {
		case len(list) == 0:
			res.Status = StatusNoApplicableSpecs
		case len(res.KilledBy) > 0:
			res.Status = StatusKilled
		default:
			res.Status = StatusSurvived
		}
		rep.Mutants = append(rep.Mutants, res)
		if res.Status == StatusSurvived {
			if sg, ok, err := suggest(reg, m, s); err != nil {
				return nil, err
			} else if ok {
				rep.SurvivorsSuggestions = append(rep.SurvivorsSuggestions, sg)
			}
		}
	}
	if killed, survived := rep.Counts(); killed+survived > 0 {
		score := float64(killed) / float64(killed+survived)
		rep.Score = &score
	}
	return rep, nil
}

// evaluate runs one spec. For an effect spec it replaces the embedded world
// with world; it returns ok=false when the spec cannot be run against that
// fixture (an effect spec from a different world definition).
func evaluate(reg *assertion.Registry, sp Spec, trace *types.Trace, fixtureWorld, world json.RawMessage) (*types.AssertionResult, bool, error) {
	spec := sp.Spec
	if sp.Type == types.TypeEffect {
		var err error
		spec, err = substituteWorld(sp.Spec, fixtureWorld, world)
		if err != nil {
			return nil, false, nil
		}
	}
	ev, err := reg.Get(sp.Type)
	if err != nil {
		return nil, false, err
	}
	return ev.Evaluate(trace, &types.Assertion{AssertionID: sp.AssertionID, Type: sp.Type, Spec: spec}), true, nil
}

// passes reports whether the spec runs against the fixture and passes.
func passes(reg *assertion.Registry, sp Spec, trace *types.Trace, fixtureWorld, world json.RawMessage) (bool, error) {
	r, ok, err := evaluate(reg, sp, trace, fixtureWorld, world)
	if err != nil || !ok {
		return false, err
	}
	return r.Status == types.StatusPass, nil
}

func hardFails(reg *assertion.Registry, sp Spec, trace *types.Trace, fixtureWorld, world json.RawMessage) (bool, error) {
	r, ok, err := evaluate(reg, sp, trace, fixtureWorld, world)
	if err != nil || !ok {
		return false, err
	}
	return r.Status == types.StatusHardFail, nil
}

// substituteWorld returns spec with its embedded world replaced by world,
// provided the embedded world and the fixture's have the same definition.
func substituteWorld(spec, fixtureWorld, world json.RawMessage) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(spec, &fields); err != nil {
		return nil, err
	}
	embedded, fixture := definitionOf(fields["world"]), definitionOf(fixtureWorld)
	if embedded == "" || fixture == "" || embedded != fixture {
		return nil, fmt.Errorf("world definition differs")
	}
	fields["world"] = world
	out, err := json.Marshal(fields)
	return out, err
}

func definitionOf(world json.RawMessage) string {
	var w struct {
		Definition string `json:"definition_sha256"`
	}
	if len(world) == 0 || json.Unmarshal(world, &w) != nil {
		return ""
	}
	return w.Definition
}

// reportSpec is the spec as reported: an effect spec drops its embedded world
// snapshot, which can be large and says nothing about the contract.
func reportSpec(sp Spec) json.RawMessage {
	if sp.Type != types.TypeEffect {
		return sp.Spec
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(sp.Spec, &fields); err != nil {
		return sp.Spec
	}
	delete(fields, "world")
	out, err := json.Marshal(fields)
	if err != nil {
		return sp.Spec
	}
	return out
}

// suggest returns the first candidate contract that passes on the original
// fixture and hard-fails on the mutant, so every reported contract is known
// to kill what it is reported for.
func suggest(reg *assertion.Registry, m Mutant, s Subject) (Suggestion, bool, error) {
	for _, c := range m.candidates {
		spec := c.Spec
		if c.Type == types.TypeEffect {
			if len(s.World) == 0 {
				continue
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(c.Spec, &fields); err != nil {
				return Suggestion{}, false, err
			}
			fields["world"] = s.World
			b, err := json.Marshal(fields)
			if err != nil {
				return Suggestion{}, false, err
			}
			spec = b
		}
		probe := Spec{AssertionID: "suggestion", Type: c.Type, Spec: spec}
		world := s.World
		if m.World != nil {
			world = m.World
		}
		okOrig, err := passes(reg, probe, s.Trace, s.World, s.World)
		if err != nil {
			return Suggestion{}, false, err
		}
		if !okOrig {
			continue
		}
		killed, err := hardFails(reg, probe, m.Trace, s.World, world)
		if err != nil {
			return Suggestion{}, false, err
		}
		if killed {
			return Suggestion{MutantID: m.ID, Contract: c, Description: m.Description}, true, nil
		}
	}
	return Suggestion{}, false, nil
}
