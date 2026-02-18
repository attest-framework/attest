package embedding

import (
	"errors"
	"math"
)

// ErrLengthMismatch is returned when vectors have different lengths.
var ErrLengthMismatch = errors.New("vectors must have the same length")

// ErrZeroMagnitude is returned when a vector has zero magnitude.
var ErrZeroMagnitude = errors.New("vector has zero magnitude")

// CosineSimilarity computes the cosine similarity between two float32 vectors.
// Returns a value in [-1.0, 1.0]. Errors if lengths differ or either vector has zero magnitude.
func CosineSimilarity(a, b []float32) (float64, error) {
	if len(a) != len(b) {
		return 0, ErrLengthMismatch
	}

	var dot, magA, magB float64
	for i := range a {
		av := float64(a[i])
		bv := float64(b[i])
		dot += av * bv
		magA += av * av
		magB += bv * bv
	}

	magA = math.Sqrt(magA)
	magB = math.Sqrt(magB)

	if magA == 0 || magB == 0 {
		return 0, ErrZeroMagnitude
	}

	return dot / (magA * magB), nil
}
