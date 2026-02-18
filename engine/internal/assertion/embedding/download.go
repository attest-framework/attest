//go:build onnx

package embedding

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
)

const (
	modelFileName = "all-MiniLM-L6-v2.onnx"
	modelURL      = "https://huggingface.co/sentence-transformers/all-MiniLM-L6-v2/resolve/main/onnx/model.onnx"
)

// onnxRuntimeURLs maps platform to shared library download URL.
var onnxRuntimeURLs = map[string]string{
	"darwin-arm64":  "https://github.com/microsoft/onnxruntime/releases/download/v1.17.1/onnxruntime-osx-arm64-1.17.1.tgz",
	"darwin-amd64":  "https://github.com/microsoft/onnxruntime/releases/download/v1.17.1/onnxruntime-osx-x86_64-1.17.1.tgz",
	"linux-amd64":   "https://github.com/microsoft/onnxruntime/releases/download/v1.17.1/onnxruntime-linux-x64-1.17.1.tgz",
	"linux-arm64":   "https://github.com/microsoft/onnxruntime/releases/download/v1.17.1/onnxruntime-linux-aarch64-1.17.1.tgz",
	"windows-amd64": "https://github.com/microsoft/onnxruntime/releases/download/v1.17.1/onnxruntime-win-x64-1.17.1.zip",
}

// defaultModelDir returns the default model directory (~/.attest/models/).
func defaultModelDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".attest", "models")
	}
	return filepath.Join(home, ".attest", "models")
}

// ensureModel checks for the ONNX model file and downloads it if missing.
// Returns the absolute path to the model file.
func ensureModel(modelDir string) (string, error) {
	if modelDir == "" {
		modelDir = defaultModelDir()
	}

	modelPath := filepath.Join(modelDir, modelFileName)
	if _, err := os.Stat(modelPath); err == nil {
		return modelPath, nil
	}

	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		return "", fmt.Errorf("onnx: create model dir %s: %w", modelDir, err)
	}

	if err := downloadFile(modelURL, modelPath); err != nil {
		return "", fmt.Errorf("onnx: download model: %w", err)
	}

	return modelPath, nil
}

// onnxRuntimeLibName returns the expected shared library filename for the current platform.
func onnxRuntimeLibName() string {
	switch runtime.GOOS {
	case "darwin":
		return "libonnxruntime.dylib"
	case "windows":
		return "onnxruntime.dll"
	default:
		return "libonnxruntime.so"
	}
}

// ensureONNXRuntime checks for the ONNX Runtime shared library and returns its path.
// The library is expected in modelDir or a system-discoverable location.
func ensureONNXRuntime(modelDir string) (string, error) {
	if modelDir == "" {
		modelDir = defaultModelDir()
	}

	libName := onnxRuntimeLibName()
	libPath := filepath.Join(modelDir, libName)

	if _, err := os.Stat(libPath); err == nil {
		return libPath, nil
	}

	// Check if ONNX Runtime is available system-wide (e.g., via brew or apt)
	// by looking in standard library paths.
	systemPaths := []string{
		"/usr/local/lib/" + libName,
		"/usr/lib/" + libName,
		"/opt/homebrew/lib/" + libName,
	}
	for _, p := range systemPaths {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}

	platform := runtime.GOOS + "-" + runtime.GOARCH
	url, ok := onnxRuntimeURLs[platform]
	if !ok {
		return "", fmt.Errorf("onnx runtime: unsupported platform %s — install ONNX Runtime manually and set ATTEST_ONNX_RUNTIME_LIB", platform)
	}

	return "", fmt.Errorf("onnx runtime: shared library not found at %s — download from %s or install via package manager", libPath, url)
}

// downloadFile downloads a URL to a local file path.
func downloadFile(url, destPath string) error {
	resp, err := http.Get(url) //nolint:gosec // URL is a hardcoded constant
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}

	tmpPath := destPath + ".tmp"
	out, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmpPath, err)
	}

	if _, err := io.Copy(out, resp.Body); err != nil {
		out.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write %s: %w", tmpPath, err)
	}
	out.Close()

	if err := os.Rename(tmpPath, destPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename %s → %s: %w", tmpPath, destPath, err)
	}

	return nil
}
