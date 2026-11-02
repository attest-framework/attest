package trace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// FuzzOTLP feeds arbitrary bytes through parsing, conversion and checking.
// Malformed input must produce an error, never a panic, and anything that
// converts must marshal and pass engine validation. Seeds are the
// protocol-tests/trace-v2 OTLP fixtures plus the committed corpus in
// testdata/fuzz/FuzzOTLP. Run longer locally with
// `go test ./internal/trace -run '^$' -fuzz FuzzOTLP -fuzztime 60s`.
func FuzzOTLP(f *testing.F) {
	for _, pattern := range []string{"otlp/*.otlp.json", "otlp-invalid/*.json"} {
		paths, err := filepath.Glob(filepath.Join(traceV2Fixtures, pattern))
		if err != nil || len(paths) == 0 {
			f.Fatalf("no fuzz seeds match %s (err=%v)", pattern, err)
		}
		for _, p := range paths {
			data, err := os.ReadFile(p)
			if err != nil {
				f.Fatal(err)
			}
			f.Add(data)
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		spans, err := ParseOTLP(data)
		if err != nil {
			return
		}
		report := Check(spans)
		if _, err := json.Marshal(report); err != nil {
			t.Fatalf("marshal check report: %v", err)
		}
		tr, err := FromSpans(spans, ConvertOptions{})
		if err != nil {
			return
		}
		if _, err := json.Marshal(tr); err != nil {
			t.Fatalf("marshal converted trace: %v", err)
		}
		if rpcErr := Validate(tr, 0); rpcErr != nil {
			t.Fatalf("converted trace fails validation: %s", rpcErr.Message)
		}
	})
}
