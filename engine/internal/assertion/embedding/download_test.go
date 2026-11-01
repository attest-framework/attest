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

func TestEnsureModelFrom_MismatchedCache_ReplacedByVerifiedDownload(t *testing.T) {
	genuine := []byte("genuine model")
	sum := sha256.Sum256(genuine)
	srv := serveBytes(t, genuine)
	path := filepath.Join(t.TempDir(), modelFileName)
	if err := os.WriteFile(path, []byte("planted model"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := ensureModelFrom(srv.URL, hex.EncodeToString(sum[:]), path)

	if err != nil {
		t.Fatalf("ensureModelFrom: %v", err)
	}
	data, err := os.ReadFile(got)
	if err != nil || string(data) != string(genuine) {
		t.Fatalf("model = %q (err %v), want the verified download", data, err)
	}
}

func TestEnsureModelFrom_MatchingCache_SkipsDownload(t *testing.T) {
	cached := []byte("cached model")
	sum := sha256.Sum256(cached)
	path := filepath.Join(t.TempDir(), modelFileName)
	if err := os.WriteFile(path, cached, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := ensureModelFrom("http://127.0.0.1:1/unreachable", hex.EncodeToString(sum[:]), path); err != nil {
		t.Fatalf("ensureModelFrom with verified cache: %v", err)
	}
}

func TestDownloadFile_Oversize_Rejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		chunk := make([]byte, 1<<20)
		for written := 0; written <= maxDownloadBytes; written += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()

	err := downloadFile(srv.URL, filepath.Join(dir, "big.bin"), modelSHA256)

	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("err = %v, want size-cap error", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("files left after oversize download: %v", entries)
	}
}
