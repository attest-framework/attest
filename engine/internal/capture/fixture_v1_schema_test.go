package capture

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// fixture-v1 (contract C3): the schema in proto/attest/v1 accepts every
// protocol-tests/fixture-v1/valid fixture and rejects every invalid one. The
// engine decodes and checks the valid fixtures too. The Python and
// TypeScript SDKs run the same files.
const schemaDir = "../../../proto/attest/v1"

func compileFixtureV1Schema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	for _, name := range []string{"trace-v2.schema.json", "fixture-v1.schema.json"} {
		f, err := os.Open(filepath.Join(schemaDir, name))
		if err != nil {
			t.Fatal(err)
		}
		doc, err := jsonschema.UnmarshalJSON(f)
		f.Close()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := c.AddResource("https://attest-framework.github.io/schemas/attest/v1/"+name, doc); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	s, err := c.Compile("https://attest-framework.github.io/schemas/attest/v1/fixture-v1.schema.json")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return s
}

func fixtureFiles(t *testing.T, kind string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("../../../protocol-tests/fixture-v1", kind, "*.attest.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no %s fixtures: %v", kind, err)
	}
	return paths
}

func validateFile(t *testing.T, s *jsonschema.Schema, path string) ([]byte, error) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return data, s.Validate(doc)
}

func TestFixtureV1Schema_ValidFixtures_AcceptedBySchemaAndEngine(t *testing.T) {
	s := compileFixtureV1Schema(t)
	for _, p := range fixtureFiles(t, "valid") {
		data, err := validateFile(t, s, p)
		if err != nil {
			t.Errorf("%s: want valid, got %v", filepath.Base(p), err)
		}
		if r := CheckFixture(data); !r.OK {
			t.Errorf("%s: engine check: %+v", filepath.Base(p), r.Issues)
		}
	}
}

func TestFixtureV1Schema_InvalidFixtures_Rejected(t *testing.T) {
	s := compileFixtureV1Schema(t)
	for _, p := range fixtureFiles(t, "invalid") {
		if _, err := validateFile(t, s, p); err == nil {
			t.Errorf("%s: want schema rejection, got valid", filepath.Base(p))
		}
	}
}

func TestFixtureV1Schema_FreshCapture_Valid(t *testing.T) {
	s := compileFixtureV1Schema(t)
	_, data := captureRun(t, newFakeUpstream(t), 2, true)

	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Validate(doc); err != nil {
		t.Fatalf("fresh capture fails the schema: %v", err)
	}
}

// The valid fixtures' contracts are exactly what the drafter writes for their
// traces, so the SDK emitter tests that read them cover every drafted kind.
func TestFixtureV1_ValidFixtures_ContractsMatchDrafter(t *testing.T) {
	kinds := map[string]bool{}
	for _, p := range fixtureFiles(t, "valid") {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		f, err := Unmarshal(data)
		if err != nil {
			t.Fatal(err)
		}
		drafted, err := Draft(f.Trace)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := Marshal(&Fixture{Contracts: f.Contracts})
		want, _ := Marshal(&Fixture{Contracts: drafted})
		if !bytes.Equal(got, want) {
			t.Errorf("%s: contracts differ from Draft", filepath.Base(p))
		}
		for _, c := range f.Contracts {
			kinds[c.Kind] = true
		}
	}
	for _, k := range []string{KindTools, KindOrder, KindCardinality, KindOutputSchema, KindCost} {
		if !kinds[k] {
			t.Errorf("no valid fixture has a %s contract", k)
		}
	}
}
