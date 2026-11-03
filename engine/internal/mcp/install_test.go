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
	for _, argv := range [][]string{nil, {}, {""}, {"a", "b\nc"}, {"a\x00"}, {"a\x07b"}, {"a\x7fb"}, make([]string, 33)} {
		if err := attestmcp.ValidateCommand(argv); err == nil {
			t.Errorf("%q accepted", argv)
		}
	}
}

func TestInstall_ExistingFileKeepsItsPermissionBits(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, ".mcp.json"), `{"mcpServers": {}}`)
	if err := os.Chmod(filepath.Join(dir, ".mcp.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := attestmcp.Install(dir, launch, false); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(filepath.Join(dir, ".mcp.json")); info.Mode().Perm() != 0o600 {
		t.Errorf("existing file mode %v, want 0600", info.Mode().Perm())
	}
	if info, _ := os.Stat(filepath.Join(dir, ".cursor", "mcp.json")); info.Mode().Perm() != 0o644 {
		t.Errorf("new file mode %v, want 0644", info.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".attest-tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestInstall_TrailingContentAfterJSON_RefusesWithoutWriting(t *testing.T) {
	for name, tail := range map[string]string{
		"conflict marker": "\n<<<<<<< HEAD\n{}\n=======\n",
		"second object":   "\n{}",
		"stray token":     " x",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			body := `{"mcpServers": {"other": {"command": "x"}}}` + tail
			write(t, filepath.Join(dir, ".mcp.json"), body)
			if _, err := attestmcp.Install(dir, launch, false); err == nil || !strings.Contains(err.Error(), "trailing content") {
				t.Fatalf("error %v, want trailing content", err)
			}
			if got := read(t, filepath.Join(dir, ".mcp.json")); got != body {
				t.Errorf("file was modified: %q", got)
			}
			if _, err := os.Stat(filepath.Join(dir, ".codex")); err == nil {
				t.Error("other configs written despite the refusal")
			}
		})
	}
}

func codexInstall(t *testing.T, existing string, force bool) (attestmcp.InstallResult, string) {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, ".codex/config.toml"), existing)
	res, err := attestmcp.Install(dir, launch, force)
	if err != nil {
		t.Fatal(err)
	}
	return res, read(t, filepath.Join(dir, ".codex/config.toml"))
}

func TestInstallCodex_AttestInAnyForm_IsDetected(t *testing.T) {
	same := `command = "uv"` + "\n" + `args = ["run", "attest", "mcp"]`
	forms := map[string]struct{ existing, wantState string }{
		"table, same":             {"[mcp_servers.attest]\n" + same + "\n", "unchanged"},
		"inline table, same":      {"[mcp_servers]\nattest = { command = \"uv\", args = [\"run\", \"attest\", \"mcp\"] }\n", "unchanged"},
		"dotted keys, same":       {"[mcp_servers]\nattest.command = \"uv\"\nattest.args = [\"run\", \"attest\", \"mcp\"]\n", "unchanged"},
		"quoted header, same":     {"[mcp_servers.\"attest\"]\n" + same + "\n", "unchanged"},
		"inline table, other":     {"[mcp_servers]\nattest = { command = \"old\" }\n", "conflict"},
		"dotted keys, other":      {"[mcp_servers]\nattest.command = \"old\"\n", "conflict"},
		"top-level dotted, other": {"mcp_servers.attest.command = \"old\"\n", "conflict"},
		"table, other":            {"[mcp_servers.attest]\ncommand = \"old\"\n", "conflict"},
	}
	for name, f := range forms {
		t.Run(name, func(t *testing.T) {
			res, got := codexInstall(t, f.existing, false)
			var state string
			switch {
			case len(res.Unchanged) > 0 && contains(res.Unchanged, ".codex/config.toml"):
				state = "unchanged"
			case contains(res.Conflict, ".codex/config.toml"):
				state = "conflict"
			default:
				state = "other"
			}
			if state != f.wantState {
				t.Errorf("state %s, want %s (result %+v)", state, f.wantState, res)
			}
			if got != f.existing {
				t.Errorf("file modified:\n%s", got)
			}
		})
	}
}

func contains(l []string, s string) bool {
	for _, e := range l {
		if e == s {
			return true
		}
	}
	return false
}

func TestInstallCodex_Force_ReplacesOnlyTableForm(t *testing.T) {
	res, got := codexInstall(t, "model = \"m\"\n\n[mcp_servers.attest]\ncommand = \"old\"\n\n[other]\nx = 1\n", true)
	if !contains(res.Updated, ".codex/config.toml") {
		t.Fatalf("result %+v", res)
	}
	if strings.Contains(got, "old") || !strings.Contains(got, `command = "uv"`) || !strings.Contains(got, "model = \"m\"") || !strings.Contains(got, "[other]\nx = 1") {
		t.Errorf("forced config:\n%s", got)
	}

	for name, existing := range map[string]string{
		"inline": "[mcp_servers]\nattest = { command = \"old\" }\n",
		"dotted": "[mcp_servers]\nattest.command = \"old\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			res, got := codexInstall(t, existing, true)
			if !contains(res.Conflict, ".codex/config.toml") || got != existing {
				t.Errorf("force overrode a non-table form: %+v\n%s", res, got)
			}
		})
	}
}

func TestInstallCodex_InvalidExisting_Refuses(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, ".codex/config.toml"), "model = \n[broken")
	if _, err := attestmcp.Install(dir, launch, false); err == nil || !strings.Contains(err.Error(), "not valid TOML") {
		t.Errorf("error %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".mcp.json")); err == nil {
		t.Error("other configs written despite the refusal")
	}
}

func TestInstallCodex_OutputAlwaysParses(t *testing.T) {
	for _, argv := range [][]string{{"uv", "run", "attest", "mcp"}, {"a b", `q"uote`, `back\slash`, "tab\there"}} {
		dir := t.TempDir()
		write(t, filepath.Join(dir, ".codex/config.toml"), "[features]\nx = true\n")
		if _, err := attestmcp.Install(dir, argv, false); err == nil {
			continue
		} else if !strings.Contains(err.Error(), "control") {
			t.Errorf("%q: %v", argv, err)
		}
	}
}

func TestInstall_AbsoluteSymlinkedDirectory_RefusedWithClearMessage(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real-cursor")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(dir, ".cursor")); err != nil {
		t.Skip(err)
	}
	_, err := attestmcp.Install(dir, launch, false)
	if err == nil || !strings.Contains(err.Error(), "absolute symlink; use a relative link") {
		t.Fatalf("error %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".mcp.json")); err == nil {
		t.Error("other configs written despite the refusal")
	}
}
