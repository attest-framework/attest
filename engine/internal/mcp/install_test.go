package mcp_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	attestmcp "github.com/attest-ai/attest/engine/internal/mcp"
)

var launch = []string{"uv", "run", "attest", "mcp"}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func write(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInstall_EmptyProject_CreatesThreeConfigs(t *testing.T) {
	dir := t.TempDir()
	res, err := attestmcp.Install(dir, launch, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".mcp.json", ".cursor/mcp.json", ".codex/config.toml"}
	if !reflect.DeepEqual(res.Created, want) || len(res.Updated)+len(res.Unchanged)+len(res.Conflict) != 0 {
		t.Fatalf("result %+v", res)
	}
	var claude, cursor struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(read(t, filepath.Join(dir, ".mcp.json"))), &claude); err != nil {
		t.Fatal(err)
	}
	if e := claude.MCPServers["attest"]; e["command"] != "uv" || !reflect.DeepEqual(e["args"], []any{"run", "attest", "mcp"}) || e["type"] != nil {
		t.Errorf(".mcp.json entry %v", e)
	}
	if err := json.Unmarshal([]byte(read(t, filepath.Join(dir, ".cursor/mcp.json"))), &cursor); err != nil {
		t.Fatal(err)
	}
	if cursor.MCPServers["attest"]["type"] != "stdio" {
		t.Errorf("cursor entry %v", cursor.MCPServers["attest"])
	}
	want2 := "[mcp_servers.attest]\ncommand = \"uv\"\nargs = [\"run\", \"attest\", \"mcp\"]\n"
	if got := read(t, filepath.Join(dir, ".codex/config.toml")); got != want2 {
		t.Errorf("codex config %q", got)
	}
}

func TestInstall_Rerun_Unchanged(t *testing.T) {
	dir := t.TempDir()
	if _, err := attestmcp.Install(dir, launch, false); err != nil {
		t.Fatal(err)
	}
	res, err := attestmcp.Install(dir, launch, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Unchanged) != 3 || len(res.Created)+len(res.Updated)+len(res.Conflict) != 0 {
		t.Errorf("result %+v", res)
	}
}

func TestInstall_KeepsOtherServersAndKeys(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, ".mcp.json"), `{"extra": [1, 2], "mcpServers": {"other": {"command": "x", "args": ["a"]}}}`)
	write(t, filepath.Join(dir, ".codex/config.toml"), "model = \"gpt-5.4\"\n\n[mcp_servers.other]\ncommand = \"y\"\n")
	res, err := attestmcp.Install(dir, launch, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.Updated, []string{".mcp.json", ".codex/config.toml"}) {
		t.Fatalf("result %+v", res)
	}
	var doc struct {
		Extra      []int                     `json:"extra"`
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(read(t, filepath.Join(dir, ".mcp.json"))), &doc); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(doc.Extra, []int{1, 2}) || doc.MCPServers["other"]["command"] != "x" || doc.MCPServers["attest"]["command"] != "uv" {
		t.Errorf("merged .mcp.json %+v", doc)
	}
	toml := read(t, filepath.Join(dir, ".codex/config.toml"))
	for _, s := range []string{`model = "gpt-5.4"`, "[mcp_servers.other]", `command = "y"`, "[mcp_servers.attest]"} {
		if !strings.Contains(toml, s) {
			t.Errorf("codex config lost %q:\n%s", s, toml)
		}
	}
}

func TestInstall_DifferentEntry_ConflictUntilForce(t *testing.T) {
	dir := t.TempDir()
	old := `{"mcpServers": {"attest": {"command": "old"}, "keep": {"command": "k"}}}`
	write(t, filepath.Join(dir, ".mcp.json"), old)
	write(t, filepath.Join(dir, ".codex/config.toml"), "[mcp_servers.attest]\ncommand = \"old\"\n\n[mcp_servers.attest.env]\nK = \"v\"\n\n[other]\nx = 1\n")

	res, err := attestmcp.Install(dir, launch, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.Conflict, []string{".mcp.json", ".codex/config.toml"}) || !reflect.DeepEqual(res.Created, []string{".cursor/mcp.json"}) {
		t.Fatalf("result %+v", res)
	}
	if got := read(t, filepath.Join(dir, ".mcp.json")); got != old {
		t.Errorf("conflicting file was modified: %s", got)
	}

	res, err = attestmcp.Install(dir, launch, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.Updated, []string{".mcp.json", ".codex/config.toml"}) || len(res.Conflict) != 0 {
		t.Fatalf("forced result %+v", res)
	}
	if got := read(t, filepath.Join(dir, ".mcp.json")); !strings.Contains(got, `"keep"`) || strings.Contains(got, `"old"`) {
		t.Errorf("forced .mcp.json %s", got)
	}
	toml := read(t, filepath.Join(dir, ".codex/config.toml"))
	if strings.Contains(toml, "old") || strings.Contains(toml, "attest.env") || !strings.Contains(toml, "[other]\nx = 1") {
		t.Errorf("forced codex config:\n%s", toml)
	}
}

func TestInstall_InvalidExistingConfig_RefusesWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, ".cursor/mcp.json"), `{not json`)
	if _, err := attestmcp.Install(dir, launch, false); err == nil {
		t.Fatal("invalid JSON accepted")
	}
	for _, p := range []string{".mcp.json", ".codex/config.toml"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err == nil {
			t.Errorf("%s written despite the refusal", p)
		}
	}
}

func TestInstall_SymlinkedDestinationOutsideRoot_Refused(t *testing.T) {
	base := t.TempDir()
	dir, outside := filepath.Join(base, "p"), filepath.Join(base, "o")
	for _, d := range []string{dir, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(dir, ".cursor")); err != nil {
		t.Skip(err)
	}
	if _, err := attestmcp.Install(dir, launch, false); err == nil {
		t.Fatal("install through an outside symlink accepted")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("files written outside the project: %v", entries)
	}
	if _, err := os.Stat(filepath.Join(dir, ".mcp.json")); err == nil {
		t.Error(".mcp.json written despite the refusal")
	}
}

func TestValidateCommand_Rejects(t *testing.T) {
	for _, argv := range [][]string{nil, {}, {""}, {"a", "b\nc"}, {"a\x00"}, make([]string, 33)} {
		if err := attestmcp.ValidateCommand(argv); err == nil {
			t.Errorf("%q accepted", argv)
		}
	}
}
