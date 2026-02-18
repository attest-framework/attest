//go:build !onnx

package embedding

// ONNXAvailable indicates whether the ONNX embedding provider was compiled in.
const ONNXAvailable = false

// NewONNXEmbedder returns an error when ONNX support is not compiled in.
func NewONNXEmbedder(_ EmbedderConfig) (Embedder, error) {
	return nil, errONNXNotAvailable
}
