// Package mutate builds behavior mutants of captured fixtures and scores a
// suite's contracts by how many of them they kill. A mutant is a fixture
// whose trace or world snapshot was altered in one deliberate way; no source
// code is mutated and no model is called.
package mutate

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"sort"

	"github.com/attest-ai/attest/engine/pkg/types"
)

// Operator names one kind of behavior mutation.
type Operator string

// The operators, in the fixed order mutants are enumerated.
const (
	DropToolCall      Operator = "drop_tool_call"
	DuplicateToolCall Operator = "duplicate_tool_call"
	ReorderToolCalls  Operator = "reorder_tool_calls"
	PerturbArgument   Operator = "perturb_argument"
	InjectToolError   Operator = "inject_tool_error"
	AlterWorldEffect  Operator = "alter_world_effect"
	TruncateOutput    Operator = "truncate_output"
)

// Operators lists every operator in enumeration order.
var Operators = []Operator{
	DropToolCall, DuplicateToolCall, ReorderToolCalls, PerturbArgument,
	InjectToolError, AlterWorldEffect, TruncateOutput,
}

// Subject is a fixture reduced to what mutation needs.
type Subject struct {
	// Name identifies the fixture in mutant ids; it must be unique per run.
	Name string
	// Trace is the captured trace. Mutation works on copies.
	Trace *types.Trace
	// World is the world-v1 snapshot, nil when the fixture has none.
	World json.RawMessage
}

// Contract is a suggested contract: an assertion type and spec.
type Contract struct {
	Type        string          `json:"type"`
	Spec        json.RawMessage `json:"spec"`
	Description string          `json:"description"`
}

// Mutant is one altered copy of a subject.
type Mutant struct {
	// ID is <fixture>:<operator>:<site>, stable for a given fixture.
	// ID is <fixture>:<operator>:<site>, stable for a given fixture.
	ID          string
	Fixture     string
	Operator    Operator
	Description string
	// Trace is the mutated trace. A mutant that changes the trace owns a copy;
	// a world mutant shares the subject's trace, so treat it as read-only.
	Trace *types.Trace
	// World is the mutated world snapshot, nil when the mutant leaves it alone.
	World json.RawMessage
	// candidates are contracts that could kill this mutant, best first. Run
	// verifies each against the mutant before reporting one.
	candidates []Contract
	// site locates the mutation inside the fixture; Generate folds it into ID.
	site string
}

// Options bounds and seeds generation.
type Options struct {
	// Seed drives the sample taken when MaxMutants caps the set.
	Seed uint64
	// MaxMutants caps the mutant set; 0 keeps every mutant.
	MaxMutants int
}

// Generate enumerates the mutants of every subject in a fixed order (subject
// name, operator, site), drops mutants identical to the original or to an
// earlier mutant, and, when opts.MaxMutants caps the set, keeps a sample
// chosen by a PCG seeded with opts.Seed. The result depends only on the
// subjects and the options.
func Generate(subjects []Subject, opts Options) ([]Mutant, error) {
	if opts.MaxMutants < 0 {
		return nil, fmt.Errorf("max mutants must be >= 0, got %d", opts.MaxMutants)
	}
	sorted := append([]Subject(nil), subjects...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	seenName := map[string]bool{}
	var all []Mutant
	for _, s := range sorted {
		if s.Name == "" || s.Trace == nil {
			return nil, fmt.Errorf("subject %q needs a name and a trace", s.Name)
		}
		if seenName[s.Name] {
			return nil, fmt.Errorf("two fixtures are named %q; mutant ids would collide", s.Name)
		}
		seenName[s.Name] = true
		ms, err := generateOne(s)
		if err != nil {
			return nil, fmt.Errorf("fixture %s: %w", s.Name, err)
		}
		all = append(all, ms...)
	}
	if opts.MaxMutants > 0 && len(all) > opts.MaxMutants {
		all = sample(all, opts.MaxMutants, opts.Seed)
	}
	return all, nil
}

// sample keeps n of ms with a partial Fisher-Yates shuffle driven directly by
// the PCG stream, so the choice does not depend on math/rand/v2's derived
// methods. The kept mutants stay in enumeration order.
func sample(ms []Mutant, n int, seed uint64) []Mutant {
	pcg := rand.NewPCG(seed, 0)
	idx := make([]int, len(ms))
	for i := range idx {
		idx[i] = i
	}
	for i := 0; i < n; i++ {
		j := i + int(pcg.Uint64()%uint64(len(idx)-i))
		idx[i], idx[j] = idx[j], idx[i]
	}
	keep := idx[:n]
	sort.Ints(keep)
	out := make([]Mutant, n)
	for i, k := range keep {
		out[i] = ms[k]
	}
	return out
}

func generateOne(s Subject) ([]Mutant, error) {
	origTrace, err := json.Marshal(s.Trace)
	if err != nil {
		return nil, err
	}
	seen := map[[32]byte]bool{fingerprint(origTrace, s.World): true}
	var out []Mutant
	add := func(m Mutant) error {
		if m.Trace == nil {
			return fmt.Errorf("%s:%s: mutant has no trace", m.Operator, m.site)
		}
		tb, err := json.Marshal(m.Trace)
		if err != nil {
			return err
		}
		w := s.World
		if m.World != nil {
			w = m.World
		}
		fp := fingerprint(tb, w)
		if seen[fp] {
			return nil
		}
		seen[fp] = true
		m.Fixture = s.Name
		m.ID = fmt.Sprintf("%s:%s:%s", s.Name, m.Operator, m.site)
		out = append(out, m)
		return nil
	}
	gens := []func(Subject, func(Mutant) error) error{
		genDrop, genDuplicate, genReorder, genPerturb, genInjectError, genWorld, genTruncate,
	}
	for _, g := range gens {
		if err := g(s, add); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func fingerprint(trace, world []byte) [32]byte {
	h := sha256.New()
	h.Write(trace)
	h.Write([]byte{0})
	h.Write(world)
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum
}
