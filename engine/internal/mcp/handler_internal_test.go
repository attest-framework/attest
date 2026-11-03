package mcp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/attest-ai/attest/engine/internal/assertion"
	"github.com/attest-ai/attest/engine/internal/pathguard"
	"github.com/attest-ai/attest/engine/pkg/types"
)

type panicEvaluator struct{}

func (panicEvaluator) Evaluate(*types.Trace, *types.Assertion) *types.AssertionResult {
	panic("evaluator exploded")
}

func TestHandler_EvaluatorPanic_BecomesToolError(t *testing.T) {
	dir := t.TempDir()
	data, err := os.ReadFile("../../../protocol-tests/fixture-v1/valid/001-command-capture-expect-fail.attest.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f.attest.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	root, err := pathguard.NewRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	reg := assertion.NewRegistry()
	reg.Register(types.TypeTrace, panicEvaluator{})
	srv := newServer(newEnv(root, assertion.NewPipeline(reg)), "test", slog.New(slog.NewTextHandler(io.Discard, nil)))

	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(context.Background(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	call := func() *mcp.CallToolResult {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "evaluate", Arguments: map[string]any{
			"fixture_path": "f.attest.json",
			"assertions": []map[string]any{{"assertion_id": "a", "type": "trace",
				"spec": map[string]any{"check": "forbidden_tools", "tools": []string{"x"}}}},
		}})
		if err != nil {
			t.Fatalf("protocol error, want a tool error: %v", err)
		}
		return res
	}
	for i := 0; i < maxConcurrentCalls+2; i++ { // a panic must also release its slot
		res := call()
		if !res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "internal error") {
			t.Fatalf("call %d: %+v", i, res)
		}
	}
	// The server still answers.
	if _, err := cs.ListTools(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	var _ = json.Marshal
}

func TestCheckSpec_Limits(t *testing.T) {
	big := strings.Repeat("a", maxPatternBytes+1)
	cases := map[string]string{
		"regex_match value": `{"check":"regex_match","target":"output.message","value":"` + big + `"}`,
		"schema pattern":    `{"target":"output.message","schema":{"type":"string","properties":{"x":{"pattern":"` + big + `"}}}}`,
		"oversized spec":    `{"k":"` + strings.Repeat("b", maxSpecBytes) + `"}`,
		"not json":          `{`,
	}
	for name, spec := range cases {
		if err := checkSpec(json.RawMessage(spec)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := checkSpec(json.RawMessage(`{"check":"regex_match","value":"^a+$"}`)); err != nil {
		t.Errorf("small regex rejected: %v", err)
	}
	if err := checkSpec(json.RawMessage(`{"check":"contains","value":"` + big + `"}`)); err != nil {
		t.Errorf("a long non-regex value was rejected: %v", err)
	}
}
