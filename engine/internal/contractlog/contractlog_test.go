package contractlog_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/attest-framework/attest/engine/internal/contractlog"
	"github.com/attest-framework/attest/engine/pkg/types"
)

func assertions(ids ...string) []types.Assertion {
	var out []types.Assertion
	for _, id := range ids {
		out = append(out, types.Assertion{AssertionID: id, Type: "trace", Spec: json.RawMessage(`{"check":"required_tools","tools":["` + id + `"]}`), RequestID: "req-" + id})
	}
	return out
}

func TestNewFromEnv_Unset_ReturnsNil(t *testing.T) {
	t.Setenv(contractlog.EnvDir, "")

	r, err := contractlog.NewFromEnv()

	if r != nil || err != nil {
		t.Fatalf("got %v, %v", r, err)
	}
}

func TestNewFromEnv_UncreatableDir_Errors(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(contractlog.EnvDir, filepath.Join(blocker, "sub"))

	if _, err := contractlog.NewFromEnv(); err == nil || !strings.Contains(err.Error(), contractlog.EnvDir) {
		t.Fatalf("err = %v, want one naming %s", err, contractlog.EnvDir)
	}
}

func TestRecordThenLoad_RoundTripsWithoutRequestIDs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "c")
	r, err := contractlog.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	if err := r.Record(&types.BatchContext{TestID: "t::a", File: "t.py", Line: 3}, assertions("x", "y")); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(r.Path())
	if strings.Contains(string(raw), "req-x") {
		t.Errorf("request_id was recorded: %s", raw)
	}
	entries, err := contractlog.Load(dir)

	if err != nil || len(entries) != 2 {
		t.Fatalf("%d entries, %v", len(entries), err)
	}
	if e := entries[0]; e.TestID != "t::a" || e.File != "t.py" || e.Line != 3 || e.Assertion.AssertionID != "x" {
		t.Errorf("entry = %+v", e)
	}
}

func TestLoad_SameSpecTwiceFromTwoProcesses_KeptOnceAndOrderStable(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 2; i++ {
		r, err := contractlog.New(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Record(&types.BatchContext{TestID: "t::a"}, assertions("b", "a")); err != nil {
			t.Fatal(err)
		}
		r.Close()
	}

	entries, err := contractlog.Load(dir)

	if err != nil || len(entries) != 2 {
		t.Fatalf("%d entries, %v", len(entries), err)
	}
	if entries[0].Assertion.AssertionID != "a" || entries[1].Assertion.AssertionID != "b" {
		t.Errorf("not sorted by spec: %s, %s", entries[0].Assertion.AssertionID, entries[1].Assertion.AssertionID)
	}
}

func TestLoad_MalformedLine_NamesFileAndLine(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "c.ndjson"), []byte("{\"assertion\":{\"type\":\"trace\",\"spec\":{}}}\nnot json\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := contractlog.Load(dir)

	if err == nil || !strings.Contains(err.Error(), "c.ndjson:2") {
		t.Fatalf("err = %v, want c.ndjson:2", err)
	}
}

func TestLoad_MissingDir_Errors(t *testing.T) {
	if _, err := contractlog.Load(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("missing directory accepted")
	}
}

func TestLoad_DirectoryNamedLikeAGlob_IsRead(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run[1]*")
	r, err := contractlog.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Record(nil, assertions("x")); err != nil {
		t.Fatal(err)
	}
	r.Close()

	entries, err := contractlog.Load(dir)

	if err != nil || len(entries) != 1 {
		t.Fatalf("%d entries, %v", len(entries), err)
	}
}

func TestLoad_NonRegularEntryNamedNdjson_IsIgnored(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub.ndjson"), 0o700); err != nil {
		t.Fatal(err)
	}

	entries, err := contractlog.Load(dir)

	if err != nil || len(entries) != 0 {
		t.Fatalf("%d entries, %v", len(entries), err)
	}
}

func TestLoad_SameSpecRecordedUnderTwoIDs_KeepsTheSameEntryWhicheverFileSortsFirst(t *testing.T) {
	line := func(id string) string {
		return `{"test_id":"t","assertion":{"assertion_id":"` + id + `","type":"trace","spec":{"check":"required_tools","tools":["a"]}}}` + "\n"
	}
	for _, names := range [][2]string{{"a.ndjson", "b.ndjson"}, {"b.ndjson", "a.ndjson"}} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, names[0]), []byte(line("assert_2")), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, names[1]), []byte(line("assert_1")), 0o600); err != nil {
			t.Fatal(err)
		}

		entries, err := contractlog.Load(dir)

		if err != nil || len(entries) != 1 || entries[0].Assertion.AssertionID != "assert_1" {
			t.Fatalf("files %v: %+v, %v", names, entries, err)
		}
	}
}
