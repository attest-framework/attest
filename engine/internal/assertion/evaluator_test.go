package assertion

import (
	"testing"

	"github.com/attest-framework/attest/engine/pkg/types"
)

func TestRegistry_AllBuiltinTypes(t *testing.T) {
	r := NewRegistry()

	builtinTypes := []string{
		types.TypeSchema,
		types.TypeConstraint,
		types.TypeTrace,
		types.TypeContent,
	}

	for _, assertionType := range builtinTypes {
		t.Run(assertionType, func(t *testing.T) {
			eval, err := r.Get(assertionType)
			if err != nil {
				t.Fatalf("Get(%q) returned error: %v", assertionType, err)
			}
			if eval == nil {
				t.Fatalf("Get(%q) returned nil evaluator", assertionType)
			}
		})
	}
}

func TestRegistry_UnknownType(t *testing.T) {
	r := NewRegistry()

	eval, err := r.Get("nonexistent_type")
	if err == nil {
		t.Fatal("expected error for unknown type, got nil")
	}
	if eval != nil {
		t.Fatal("expected nil evaluator for unknown type")
	}
}

func TestRegistry_Register_Override(t *testing.T) {
	r := NewRegistry()

	custom := &ContentEvaluator{}
	r.Register("custom_type", custom)

	eval, err := r.Get("custom_type")
	if err != nil {
		t.Fatalf("Get(custom_type) returned error: %v", err)
	}
	if eval != custom {
		t.Fatal("returned evaluator is not the registered one")
	}
}
