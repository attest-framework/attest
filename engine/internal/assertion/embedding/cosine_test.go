package embedding_test

import (
	"math"
	"testing"

	"github.com/attest-ai/attest/engine/internal/assertion/embedding"
)

func TestCosineSimilarity_Identical(t *testing.T) {
	a := []float32{1, 2, 3}
	sim, err := embedding.CosineSimilarity(a, a)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if math.Abs(sim-1.0) > 1e-6 {
		t.Errorf("identical vectors: got %f, want 1.0", sim)
	}
}

func TestCosineSimilarity_Orthogonal(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{0, 1, 0}
	sim, err := embedding.CosineSimilarity(a, b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if math.Abs(sim-0.0) > 1e-6 {
		t.Errorf("orthogonal vectors: got %f, want 0.0", sim)
	}
}

func TestCosineSimilarity_Opposite(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{-1, 0, 0}
	sim, err := embedding.CosineSimilarity(a, b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if math.Abs(sim-(-1.0)) > 1e-6 {
		t.Errorf("opposite vectors: got %f, want -1.0", sim)
	}
}

func TestCosineSimilarity_KnownValue(t *testing.T) {
	a := []float32{1, 1, 0}
	b := []float32{1, 0, 0}
	// cos(45°) = 1/sqrt(2) ≈ 0.7071
	sim, err := embedding.CosineSimilarity(a, b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := 1.0 / math.Sqrt2
	if math.Abs(sim-expected) > 1e-6 {
		t.Errorf("known value: got %f, want %f", sim, expected)
	}
}

func TestCosineSimilarity_LengthMismatch(t *testing.T) {
	a := []float32{1, 2, 3}
	b := []float32{1, 2}
	_, err := embedding.CosineSimilarity(a, b)
	if err == nil {
		t.Fatal("expected error for length mismatch, got nil")
	}
}

func TestCosineSimilarity_ZeroVector(t *testing.T) {
	a := []float32{0, 0, 0}
	b := []float32{1, 2, 3}
	_, err := embedding.CosineSimilarity(a, b)
	if err == nil {
		t.Fatal("expected error for zero magnitude vector, got nil")
	}
}

func TestCosineSimilarity_BothZero(t *testing.T) {
	a := []float32{0, 0, 0}
	_, err := embedding.CosineSimilarity(a, a)
	if err == nil {
		t.Fatal("expected error for zero magnitude vectors, got nil")
	}
}
