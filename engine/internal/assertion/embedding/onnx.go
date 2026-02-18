//go:build onnx

package embedding

import (
	"context"
	"fmt"
	"math"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

const (
	onnxModelName    = "all-MiniLM-L6-v2"
	onnxEmbeddingDim = 384
	onnxMaxTokenLen  = 128
	onnxBatchSize    = 1
)

// ONNXAvailable indicates that the ONNX embedding provider is compiled in.
const ONNXAvailable = true

// ONNXEmbedder produces embeddings using a local ONNX model.
type ONNXEmbedder struct {
	mu        sync.Mutex
	modelPath string
}

// NewONNXEmbedder creates an Embedder backed by a local ONNX model.
// On first use it downloads the model to cfg.ModelDir (default ~/.attest/models/).
func NewONNXEmbedder(cfg EmbedderConfig) (Embedder, error) {
	modelDir := cfg.ModelDir
	if modelDir == "" {
		modelDir = defaultModelDir()
	}

	libPath, err := ensureONNXRuntime(modelDir)
	if err != nil {
		return nil, fmt.Errorf("onnx embedder: %w", err)
	}
	ort.SetSharedLibraryPath(libPath)

	if err := ort.InitializeEnvironment(); err != nil {
		return nil, fmt.Errorf("onnx embedder: initialize environment: %w", err)
	}

	modelPath, err := ensureModel(modelDir)
	if err != nil {
		return nil, fmt.Errorf("onnx embedder: %w", err)
	}

	return &ONNXEmbedder{
		modelPath: modelPath,
	}, nil
}

// Model returns the ONNX model name.
func (e *ONNXEmbedder) Model() string { return onnxModelName }

// Embed produces a normalized embedding vector for the given text.
func (e *ONNXEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	ids, mask := tokenize(text, onnxMaxTokenLen)
	typeIDs := make([]int64, onnxMaxTokenLen)

	shape := ort.NewShape(int64(onnxBatchSize), int64(onnxMaxTokenLen))
	outShape := ort.NewShape(int64(onnxBatchSize), int64(onnxMaxTokenLen), int64(onnxEmbeddingDim))

	inputTensor, err := ort.NewTensor(shape, ids)
	if err != nil {
		return nil, fmt.Errorf("onnx embed: create input_ids tensor: %w", err)
	}
	defer inputTensor.Destroy()

	maskTensor, err := ort.NewTensor(shape, mask)
	if err != nil {
		return nil, fmt.Errorf("onnx embed: create attention_mask tensor: %w", err)
	}
	defer maskTensor.Destroy()

	typeTensor, err := ort.NewTensor(shape, typeIDs)
	if err != nil {
		return nil, fmt.Errorf("onnx embed: create token_type_ids tensor: %w", err)
	}
	defer typeTensor.Destroy()

	outputData := make([]float32, onnxBatchSize*onnxMaxTokenLen*onnxEmbeddingDim)
	outputTensor, err := ort.NewTensor(outShape, outputData)
	if err != nil {
		return nil, fmt.Errorf("onnx embed: create output tensor: %w", err)
	}
	defer outputTensor.Destroy()

	session, err := ort.NewAdvancedSession(
		e.modelPath,
		[]string{"input_ids", "attention_mask", "token_type_ids"},
		[]string{"last_hidden_state"},
		[]ort.Value{inputTensor, maskTensor, typeTensor},
		[]ort.Value{outputTensor},
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("onnx embed: create session: %w", err)
	}
	defer session.Destroy()

	if err := session.Run(); err != nil {
		return nil, fmt.Errorf("onnx embed: run inference: %w", err)
	}

	rawOutput := outputTensor.GetData()
	result := meanPool(rawOutput, mask, onnxMaxTokenLen, onnxEmbeddingDim)
	l2Normalize(result)

	return result, nil
}

// meanPool computes the mean of token embeddings weighted by attention mask.
func meanPool(output []float32, mask []int64, seqLen, dim int) []float32 {
	result := make([]float32, dim)
	var count float32

	for i := 0; i < seqLen; i++ {
		if mask[i] == 0 {
			continue
		}
		count++
		offset := i * dim
		for j := 0; j < dim; j++ {
			result[j] += output[offset+j]
		}
	}

	if count > 0 {
		for j := range result {
			result[j] /= count
		}
	}
	return result
}

// l2Normalize applies L2 normalization in-place.
func l2Normalize(vec []float32) {
	var sum float64
	for _, v := range vec {
		sum += float64(v) * float64(v)
	}
	norm := float32(math.Sqrt(sum))
	if norm > 0 {
		for i := range vec {
			vec[i] /= norm
		}
	}
}
