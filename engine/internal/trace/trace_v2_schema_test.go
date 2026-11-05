package trace

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/attest-framework/attest/engine/pkg/types"
)

// trace-v2 (contract C3): the schema in proto/attest/v1 accepts every
// protocol-tests/trace-v2/valid fixture and every golden converted trace,
// and rejects every invalid fixture. The engine's own validation accepts
// the valid fixtures too. The Python and TypeScript SDKs run the same
// fixtures.
const traceV2SchemaPath = "../../../proto/attest/v1/trace-v2.schema.json"

func compileTraceV2Schema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	f, err := os.Open(traceV2SchemaPath)
	if err != nil {
		t.Fatalf("open schema: %v", err)
	}
	defer f.Close()
	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("trace-v2.schema.json", doc); err != nil {
		t.Fatalf("add schema: %v", err)
	}
	s, err := c.Compile("trace-v2.schema.json")
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	return s
}

func validateTraceV2(s *jsonschema.Schema, data []byte) error {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return err
	}
	return s.Validate(doc)
}

func TestTraceV2Schema_Fixtures(t *testing.T) {
	s := compileTraceV2Schema(t)
	for _, p := range append(fixturePaths(t, "valid/*.json"), fixturePaths(t, "otlp/*.trace.json")...) {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateTraceV2(s, data); err != nil {
			t.Errorf("%s: want valid, got %v", filepath.Base(p), err)
		}
	}
	for _, p := range fixturePaths(t, "invalid/*.json") {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if validateTraceV2(s, data) == nil {
			t.Errorf("%s: want schema rejection, got valid", filepath.Base(p))
		}
	}
}

func TestTraceV2Schema_EngineAcceptsValidFixtures(t *testing.T) {
	for _, p := range fixturePaths(t, "valid/*.json") {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var parsed types.Trace
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("%s: decode: %v", filepath.Base(p), err)
		}
		if rpcErr := Validate(&parsed, len(data)); rpcErr != nil {
			t.Errorf("%s: engine rejected valid trace: %s", filepath.Base(p), rpcErr.Message)
		}
	}
}
