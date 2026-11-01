//go:build onnx

package embedding

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func serveBytes(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDownloadFile_TamperedModel_FailsChecksumAndLeavesNoFile(t *testing.T) {
	srv := serveBytes(t, []byte("tampered model bytes"))
	dest := filepath.Join(t.TempDir(), modelFileName)

	err := downloadFile(srv.URL+"/model.onnx", dest, modelSHA256)

	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v, want checksum mismatch", err)
	}
	entries, err := os.ReadDir(filepath.Dir(dest))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("files left after a failed checksum: %v", entries)
	}
}

func TestDownloadFile_MatchingChecksum_WritesFile(t *testing.T) {
	body := []byte("genuine bytes")
	sum := sha256.Sum256(body)
	srv := serveBytes(t, body)
	dest := filepath.Join(t.TempDir(), "file.bin")

	if err := downloadFile(srv.URL, dest, hex.EncodeToString(sum[:])); err != nil {
		t.Fatalf("downloadFile: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil || string(got) != string(body) {
		t.Fatalf("dest = %q (err %v), want %q", got, err, body)
	}
}

func TestEnsureONNXRuntime_ExplicitLibMissing_Errors(t *testing.T) {
	t.Setenv("ATTEST_ONNX_RUNTIME_LIB", filepath.Join(t.TempDir(), "missing-lib"))

	_, err := ensureONNXRuntime(t.TempDir())

	if err == nil || !strings.Contains(err.Error(), "ATTEST_ONNX_RUNTIME_LIB") {
		t.Fatalf("err = %v, want ATTEST_ONNX_RUNTIME_LIB error", err)
	}
}

func TestFileSHA256_DetectsTamperedCachedModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), modelFileName)
	if err := os.WriteFile(path, []byte("planted model"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := fileSHA256(path)

	if err != nil {
		t.Fatalf("fileSHA256: %v", err)
	}
	if got == modelSHA256 {
		t.Fatal("planted file matched the pinned model sha256")
	}
}
