package assertionid

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type vector struct {
	Name            string `json:"name"`
	ScopeID         string `json:"scope_id"`
	AssertionType   string `json:"assertion_type"`
	SpecJSON        string `json:"spec_json"`
	OccurrenceIndex int    `json:"occurrence_index"`
	CanonicalSpec   string `json:"canonical_spec"`
	CanonicalInput  string `json:"canonical_input"`
	ID              string `json:"id"`
}

func loadVectors(t *testing.T) []vector {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "protocol-tests", "ids", "assertion-ids.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Vectors []vector `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Vectors) == 0 {
		t.Fatal("no vectors in the golden file")
	}
	return doc.Vectors
}

func TestGoldenVectors_ReproduceCanonicalBytesAndIDs(t *testing.T) {
	for _, v := range loadVectors(t) {
		t.Run(v.Name, func(t *testing.T) {
			canon, err := Canonicalize([]byte(v.SpecJSON))
			if err != nil {
				t.Fatal(err)
			}
			if string(canon) != v.CanonicalSpec {
				t.Errorf("canonical spec = %q, want %q", canon, v.CanonicalSpec)
			}
			in, err := Input(v.ScopeID, v.AssertionType, []byte(v.SpecJSON), v.OccurrenceIndex)
			if err != nil {
				t.Fatal(err)
			}
			if string(in) != v.CanonicalInput {
				t.Errorf("canonical input = %q, want %q", in, v.CanonicalInput)
			}
			id, err := ID(v.ScopeID, v.AssertionType, []byte(v.SpecJSON), v.OccurrenceIndex)
			if err != nil {
				t.Fatal(err)
			}
			if id != v.ID {
				t.Errorf("id = %s, want %s", id, v.ID)
			}
		})
	}
}

func TestGoldenVectors_EquivalentSpecsShareIDsDistinctSpecsDoNot(t *testing.T) {
	byName := map[string]vector{}
	for _, v := range loadVectors(t) {
		byName[v.Name] = v
	}
	same := [][]string{
		{"flat_keys_sorted", "key_order_irrelevant", "whitespace_irrelevant"},
		{"unicode_bmp", "unicode_raw_equals_escaped"},
		{"int_float_equiv_int", "int_float_equiv_float"},
		{"negative_zero", "negative_zero_float"},
	}
	for _, g := range same {
		for _, n := range g[1:] {
			if byName[n].ID != byName[g[0]].ID {
				t.Errorf("%s and %s must share an id", g[0], n)
			}
		}
	}
	distinct := []string{"occurrence_0", "occurrence_1", "occurrence_2", "array_order_significant", "array_order_significant_2"}
	seen := map[string]string{}
	for _, n := range distinct {
		if prev, dup := seen[byName[n].ID]; dup {
			t.Errorf("%s and %s must have distinct ids", prev, n)
		}
		seen[byName[n].ID] = n
	}
}

func TestCanonicalize_RejectsInputsWithNoCanonicalForm(t *testing.T) {
	for name, in := range map[string]string{
		"duplicate key":  `{"a":1,"a":2}`,
		"overflow":       `{"a":1e999}`,
		"trailing data":  `{"a":1} {}`,
		"malformed":      `{"a":`,
		"invalid utf-8":  "{\"a\":\"\xff\"}",
		"empty document": ``,
	} {
		if _, err := Canonicalize([]byte(in)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestInput_NegativeOccurrence_Errors(t *testing.T) {
	if _, err := Input("s", "t", []byte(`{}`), -1); err == nil || !strings.Contains(err.Error(), "negative") {
		t.Fatalf("err = %v, want a negative-occurrence error", err)
	}
}

type invalidVector struct {
	Name             string `json:"name"`
	ScopeID          string `json:"scope_id"`
	ScopeIDBytesHex  string `json:"scope_id_bytes_hex"`
	AssertionType    string `json:"assertion_type"`
	SpecJSON         string `json:"spec_json"`
	SpecJSONBytesHex string `json:"spec_json_bytes_hex"`
	OccurrenceIndex  int    `json:"occurrence_index"`
	Expected         string `json:"expected"`
}

func TestInvalidVectors_EveryOneErrors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "protocol-tests", "ids", "assertion-ids.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Invalid []invalidVector `json:"invalid"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Invalid) == 0 {
		t.Fatal("no invalid vectors in the golden file")
	}
	for _, v := range doc.Invalid {
		t.Run(v.Name, func(t *testing.T) {
			if v.Expected != "error" {
				t.Fatalf("expected = %q, want \"error\"", v.Expected)
			}
			scope, spec := v.ScopeID, []byte(v.SpecJSON)
			if v.ScopeIDBytesHex != "" {
				b, err := hex.DecodeString(v.ScopeIDBytesHex)
				if err != nil {
					t.Fatal(err)
				}
				scope = string(b)
			}
			if v.SpecJSONBytesHex != "" {
				b, err := hex.DecodeString(v.SpecJSONBytesHex)
				if err != nil {
					t.Fatal(err)
				}
				spec = b
			}
			if id, err := ID(scope, v.AssertionType, spec, v.OccurrenceIndex); err == nil {
				t.Fatalf("ID succeeded with %q, want an error", id)
			}
		})
	}
}

func TestInput_InvalidAssertionType_Errors(t *testing.T) {
	if _, err := Input("s", "t\xff", []byte(`{}`), 0); err == nil {
		t.Fatal("expected an error for invalid UTF-8 in assertion_type")
	}
}
