package embedding

import "context"

// Embedder produces vector embeddings for text.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
	Model() string
}

// EmbedderConfig holds configuration for creating an Embedder.
type EmbedderConfig struct {
	Provider string
	Model    string
	APIKey   string
	BaseURL  string
	ModelDir string
}
