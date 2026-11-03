package main_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	// cliProtoRoot is the directory the registry's schema paths are relative to.
	cliProtoRoot = "../../../proto/attest/v1"
	// cliFixtureRoot holds protocol-tests/cli/<stem>/{valid,invalid}/*.json,
	// one directory per proto/attest/v1/cli/<stem>.schema.json.
	cliFixtureRoot = "../../../protocol-tests/cli"
)

// compileCLISchema compiles the schema at rel, a path relative to
// proto/attest/v1 as the registry declares it.
func compileCLISchema(t *testing.T, rel string) *jsonschema.Schema {
	t.Helper()
	path := filepath.Join(cliProtoRoot, filepath.FromSlash(rel))
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open schema %s: %v", rel, err)
	}
	defer f.Close()
	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		t.Fatalf("decode schema %s: %v", rel, err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(path, doc); err != nil {
		t.Fatalf("add schema %s: %v", rel, err)
	}
	s, err := c.Compile(path)
	if err != nil {
		t.Fatalf("compile schema %s: %v", rel, err)
	}
	return s
}

// validateOneJSONDocument checks that data is exactly one JSON document
// (surrounding whitespace allowed) and that it matches s.
func validateOneJSONDocument(s *jsonschema.Schema, data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("stdout holds more than one JSON document")
	}
	return s.Validate(doc)
}

// TestCLIFixtures_MatchSchemas validates every protocol-tests/cli/<stem>
// fixture against proto/attest/v1/cli/<stem>.schema.json: valid/* must pass,
// invalid/* must fail. Every directory needs at least one of each.
func TestCLIFixtures_MatchSchemas(t *testing.T) {
	dirs, err := os.ReadDir(cliFixtureRoot)
	if err != nil || len(dirs) == 0 {
		t.Fatalf("no CLI fixture directories under %s (err=%v)", cliFixtureRoot, err)
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		stem := d.Name()
		t.Run(stem, func(t *testing.T) {
			s := compileCLISchema(t, "cli/"+stem+".schema.json")
			for _, kind := range []string{"valid", "invalid"} {
				paths, err := filepath.Glob(filepath.Join(cliFixtureRoot, stem, kind, "*.json"))
				if err != nil || len(paths) == 0 {
					t.Fatalf("no %s fixtures for %s (err=%v)", kind, stem, err)
				}
				for _, p := range paths {
					data, err := os.ReadFile(p)
					if err != nil {
						t.Fatal(err)
					}
					err = validateOneJSONDocument(s, data)
					if kind == "valid" && err != nil {
						t.Errorf("%s/%s: want valid, got %v", kind, filepath.Base(p), err)
					}
					if kind == "invalid" && err == nil {
						t.Errorf("%s/%s: want invalid, schema accepted it", kind, filepath.Base(p))
					}
				}
			}
		})
	}
}
