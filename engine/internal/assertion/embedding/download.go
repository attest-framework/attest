//go:build onnx

package embedding

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

const (
	// maxDownloadBytes caps a download so a hostile mirror cannot fill the
	// disk before the checksum check; the pinned model is about 90 MB.
	maxDownloadBytes = 256 << 20
	downloadTimeout  = 15 * time.Minute
)

// The model is pinned to a Hugging Face commit so its sha256 cannot change
// under the engine.
const (
	modelFileName = "all-MiniLM-L6-v2.onnx"
	modelURL      = "https://huggingface.co/sentence-transformers/all-MiniLM-L6-v2/resolve/1110a243fdf4706b3f48f1d95db1a4f5529b4d41/onnx/model.onnx"
	modelSHA256   = "6fd5d72fe4589f189f8ebc006442dbb529bb7ce38f8082112682524616046452"
)

// runtimeArchive is an ONNX Runtime release archive with its published sha256.
type runtimeArchive struct {
	url    string
	sha256 string
}

// onnxRuntimeArchives maps platform to the ONNX Runtime 1.26.0 release
// archive the engine was built against: onnxruntime_go v1.31 requires ORT API
// 26. The engine does not download the runtime; these are shown so users can
// install and verify it manually. ORT 1.26.0 publishes no macOS x86_64 build.
var onnxRuntimeArchives = map[string]runtimeArchive{
	"darwin-arm64": {
		"https://github.com/microsoft/onnxruntime/releases/download/v1.26.0/onnxruntime-osx-arm64-1.26.0.tgz",
		"7a1280bbb1701ea514f71828765237e7896e0f2e1cd332f1f70dbd5c3e33aca3",
	},
	"linux-amd64": {
		"https://github.com/microsoft/onnxruntime/releases/download/v1.26.0/onnxruntime-linux-x64-1.26.0.tgz",
		"1254da24fb389cf39dc0ff3451ab48301740ffbfcbaf646849df92f80ee92c57",
	},
	"linux-arm64": {
		"https://github.com/microsoft/onnxruntime/releases/download/v1.26.0/onnxruntime-linux-aarch64-1.26.0.tgz",
		"34ff1c2d0f12e2cf3d33a0c5f82e39792e1d581fbd6968fd7c30d173654be01a",
	},
	"windows-amd64": {
		"https://github.com/microsoft/onnxruntime/releases/download/v1.26.0/onnxruntime-win-x64-1.26.0.zip",
		"6ebe99b5564bf4d029b6e93eac9ff423682b6212eade769e9ca3f685eaf500b4",
	},
}

// defaultModelDir returns the default model directory (~/.attest/models/).
func defaultModelDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".attest", "models")
	}
	return filepath.Join(home, ".attest", "models")
}

// ensureModel returns the path of the pinned ONNX model in modelDir. A cached
// file is used only when its sha256 matches; otherwise the model is downloaded
// and verified.
func ensureModel(modelDir string) (string, error) {
	if modelDir == "" {
		modelDir = defaultModelDir()
	}

	modelPath := filepath.Join(modelDir, modelFileName)
	got, err := fileSHA256(modelPath)
	switch {
	case err == nil && got == modelSHA256:
		return modelPath, nil
	case err != nil && !os.IsNotExist(err):
		return "", fmt.Errorf("onnx: read cached model %s: %w", modelPath, err)
	}
	// Missing, or cached by an unpinned engine version, or corrupt: download
	// the pinned file, which replaces the cached one only after verification.

	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		return "", fmt.Errorf("onnx: create model dir %s: %w", modelDir, err)
	}

	if err := downloadFile(modelURL, modelPath, modelSHA256); err != nil {
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

// ensureONNXRuntime returns the path of the ONNX Runtime shared library:
// ATTEST_ONNX_RUNTIME_LIB when set, otherwise modelDir or a standard system
// location. It never downloads the runtime.
func ensureONNXRuntime(modelDir string) (string, error) {
	if lib := os.Getenv("ATTEST_ONNX_RUNTIME_LIB"); lib != "" {
		if _, err := os.Stat(lib); err != nil {
			return "", fmt.Errorf("onnx runtime: ATTEST_ONNX_RUNTIME_LIB=%s: %w", lib, err)
		}
		return lib, nil
	}
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
	archive, ok := onnxRuntimeArchives[platform]
	if !ok {
		return "", fmt.Errorf("onnx runtime: no ONNX Runtime 1.26.0 release for %s — build it, then set ATTEST_ONNX_RUNTIME_LIB to %s", platform, libName)
	}

	return "", fmt.Errorf(
		"onnx runtime: %s not found in %s or system library paths — download ONNX Runtime 1.26.0 from %s (sha256 %s), "+
			"then copy lib/%s into %s or set ATTEST_ONNX_RUNTIME_LIB",
		libName, modelDir, archive.url, archive.sha256, libName, modelDir)
}

// downloadFile downloads url to destPath and verifies its sha256 against
// wantSHA256 before moving it into place. A mismatch removes the partial file
// and returns a checksum error, so a tampered download is never used.
func downloadFile(url, destPath, wantSHA256 string) error {
	client := &http.Client{Timeout: downloadTimeout}
	resp, err := client.Get(url) //nolint:gosec // URL is a pinned constant
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}

	out, err := os.CreateTemp(filepath.Dir(destPath), filepath.Base(destPath)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file for %s: %w", destPath, err)
	}
	tmpPath := out.Name()

	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, hash), io.LimitReader(resp.Body, maxDownloadBytes+1))
	if err != nil {
		out.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write %s: %w", tmpPath, err)
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("sync %s: %w", tmpPath, err)
	}
	if err := out.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close %s: %w", tmpPath, err)
	}
	if n > maxDownloadBytes {
		os.Remove(tmpPath)
		return fmt.Errorf("download %s: exceeds %d bytes", url, maxDownloadBytes)
	}

	if got := hex.EncodeToString(hash.Sum(nil)); got != wantSHA256 {
		os.Remove(tmpPath)
		return fmt.Errorf("download %s: checksum mismatch: got sha256 %s, want %s", url, got, wantSHA256)
	}

	if err := os.Rename(tmpPath, destPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename %s → %s: %w", tmpPath, destPath, err)
	}

	return nil
}

// fileSHA256 returns the hex sha256 of the file at path.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
