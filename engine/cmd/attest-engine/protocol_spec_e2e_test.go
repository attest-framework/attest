package main_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

// specInitializeBlocks returns the request and response JSON blocks of the
// `initialize` section of protocol-spec.md.
func specInitializeBlocks(t *testing.T) (request, response string) {
	t.Helper()
	raw, err := os.ReadFile("../../../proto/attest/v1/protocol-spec.md")
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(raw), "### 2.1 `initialize`")
	if !ok {
		t.Fatal("protocol-spec.md has no initialize section")
	}
	var blocks []string
	for {
		_, rest, found := strings.Cut(section, "```json\n")
		if !found || len(blocks) == 2 {
			break
		}
		block, after, _ := strings.Cut(rest, "\n```")
		blocks = append(blocks, block)
		section = after
	}
	if len(blocks) != 2 {
		t.Fatalf("found %d json blocks in the initialize section, want request and response", len(blocks))
	}
	return blocks[0], blocks[1]
}

func TestProtocolSpec_InitializeExample_MatchesEngineResponse(t *testing.T) {
	// Arrange
	request, response := specInitializeBlocks(t)
	var compact strings.Builder
	var req any
	if err := json.Unmarshal([]byte(request), &req); err != nil {
		t.Fatalf("documented request is not JSON: %v", err)
	}
	line, _ := json.Marshal(req)
	compact.Write(line)
	compact.WriteString("\n")
	bin := buildEngineBinary(t)
	cmd := exec.Command(bin, "--log-level=error")
	cmd.Env = engineEnv(t.TempDir())
	cmd.Stdin = strings.NewReader(compact.String())
	var stdout strings.Builder
	cmd.Stdout = &stdout

	// Act
	if err := cmd.Run(); err != nil {
		t.Fatalf("engine: %v", err)
	}

	// Assert
	var got, want map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout.String())), &got); err != nil {
		t.Fatalf("engine reply is not one JSON object: %v\n%s", err, stdout.String())
	}
	if err := json.Unmarshal([]byte(response), &want); err != nil {
		t.Fatalf("documented response is not JSON: %v", err)
	}
	for _, m := range []map[string]any{got, want} {
		delete(m["result"].(map[string]any), "engine_version")
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("documented response differs from the engine's\n got: %v\nwant: %v", got, want)
	}
}
