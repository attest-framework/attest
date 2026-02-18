package embedding

import (
	"context"
	"errors"
)

// Embedder produces vector embeddings for text.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
	Model() string
}

var errONNXNotAvailable = errors.New("onnx embedding: not compiled — rebuild with -tags onnx")

// EmbedderConfig holds configuration for creating an Embedder.
type EmbedderConfig struct {
	Provider string
	Model    string
	APIKey   string
	BaseURL  string
	ModelDir string
}
