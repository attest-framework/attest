package main_test

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const skillsSourceDir = "../../../skills"

type skillsInstallJSON struct {
	Dir       string   `json:"dir"`
	Created   []string `json:"created"`
	Updated   []string `json:"updated"`
	Unchanged []string `json:"unchanged"`
}

// sourceSkillFiles lists the files install writes for each target: every
// file under skills/<name>/ for each skill directory, as <name>/<rel>.
func sourceSkillFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(skillsSourceDir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() == ".gitkeep" {
			return err
		}
		rel, err := filepath.Rel(skillsSourceDir, p)
		if err != nil {
			return err
		}
		if strings.Contains(filepath.ToSlash(rel), "/") {
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil || len(out) == 0 {
		t.Fatalf("no source skill files (err=%v)", err)
	}
	return out
}

func installSkillsJSON(t *testing.T, bin, dir string) skillsInstallJSON {
	t.Helper()
	stdout, stderr, code := runAssetEngine(t, bin, nil, "skills", "install", "--dir", dir, "--json")
	if code != 0 {
		t.Fatalf("install: exit %d\n%s", code, stderr)
	}
	if err := validateAssetCLIJSON(compileAssetCLISchema(t, "skills-install"), stdout); err != nil {
		t.Fatalf("install output does not match schema: %v\n%s", err, stdout)
	}
	var res skillsInstallJSON
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatal(err)
	}
	return res
}

func TestSkillsList_JSON_MatchesSchemaAndSources(t *testing.T) {
	bin := buildEngineBinary(t)

	stdout, stderr, code := runAssetEngine(t, bin, nil, "skills", "list", "--json")

	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	if err := validateAssetCLIJSON(compileAssetCLISchema(t, "skills-list"), stdout); err != nil {
		t.Fatalf("output does not match schema: %v\n%s", err, stdout)
	}
	var doc struct {
		Skills []struct{ Name, Description string }
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range doc.Skills {
		names = append(names, s.Name)
	}
	if !slices.Contains(names, "attest") {
		t.Errorf("skills %v lack attest", names)
	}
}

func TestSkillsInstall_FreshThenRerun_CreatesThenUnchanged(t *testing.T) {
	bin := buildEngineBinary(t)
	dir := t.TempDir()
	var want []string
	for _, target := range []string{".agents/skills", ".claude/skills"} {
		for _, f := range sourceSkillFiles(t) {
			want = append(want, target+"/"+f)
		}
	}
	slices.Sort(want)

	first := installSkillsJSON(t, bin, dir)
	second := installSkillsJSON(t, bin, dir)

	if !slices.Equal(first.Created, want) || len(first.Updated)+len(first.Unchanged) != 0 {
		t.Errorf("first install: created %v updated %v unchanged %v, want created %v",
			first.Created, first.Updated, first.Unchanged, want)
	}
	if !slices.Equal(second.Unchanged, want) || len(second.Created)+len(second.Updated) != 0 {
		t.Errorf("rerun: created %v updated %v unchanged %v, want all unchanged",
			second.Created, second.Updated, second.Unchanged)
	}
	abs, _ := filepath.EvalSymlinks(dir)
	got, _ := filepath.EvalSymlinks(first.Dir)
	if got != abs {
		t.Errorf("dir = %q, want %q", first.Dir, dir)
	}
	for _, rel := range want {
		src, err := os.ReadFile(filepath.Join(skillsSourceDir, filepath.FromSlash(strings.SplitN(rel, "/", 3)[2])))
		if err != nil {
			t.Fatal(err)
		}
		dst, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil || string(dst) != string(src) {
			t.Errorf("%s: content differs from source (err=%v)", rel, err)
		}
	}
}

func TestSkillsInstall_EditedFileAndForeignSkill_UpdatesOnlyAttestFiles(t *testing.T) {
	bin := buildEngineBinary(t)
	dir := t.TempDir()
	installSkillsJSON(t, bin, dir)
	edited := ".claude/skills/attest/SKILL.md"
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(edited)), []byte("local edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(dir, ".claude", "skills", "mine", "SKILL.md")
	extra := filepath.Join(dir, ".agents", "skills", "attest", "local-notes.md")
	for _, p := range []string{foreign, extra} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("keep me\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	res := installSkillsJSON(t, bin, dir)

	if !slices.Equal(res.Updated, []string{edited}) || len(res.Created) != 0 {
		t.Errorf("created %v updated %v, want only %s updated", res.Created, res.Updated, edited)
	}
	for _, p := range []string{foreign, extra} {
		if data, err := os.ReadFile(p); err != nil || string(data) != "keep me\n" {
			t.Errorf("%s was touched (err=%v)", p, err)
		}
	}
}

func TestSkills_UsageErrors_ExitTwo(t *testing.T) {
	bin := buildEngineBinary(t)
	for _, args := range [][]string{{"skills"}, {"skills", "frob"}, {"skills", "list", "extra"}, {"skills", "install", "--bogus"}} {
		if _, _, code := runAssetEngine(t, bin, nil, args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
}

func countFiles(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return 0
	}
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSkillsInstall_SymlinkedDirInsideRoot_InstallsOnceAndReportsBothPaths(t *testing.T) {
	bin := buildEngineBinary(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".agents", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", ".agents", "skills"), filepath.Join(dir, ".claude", "skills")); err != nil {
		t.Fatal(err)
	}

	first := installSkillsJSON(t, bin, dir)
	second := installSkillsJSON(t, bin, dir)

	sources := len(sourceSkillFiles(t))
	if got := countFiles(t, filepath.Join(dir, ".agents")); got != sources {
		t.Errorf("real files under .agents = %d, want %d (each written once)", got, sources)
	}
	if len(first.Created) != 2*sources || len(first.Updated)+len(first.Unchanged) != 0 {
		t.Errorf("first install: created %d updated %d unchanged %d, want created %d", len(first.Created), len(first.Updated), len(first.Unchanged), 2*sources)
	}
	if len(second.Unchanged) != 2*sources || len(second.Created)+len(second.Updated) != 0 {
		t.Errorf("rerun: created %d updated %d unchanged %d, want all %d unchanged", len(second.Created), len(second.Updated), len(second.Unchanged), 2*sources)
	}
}

func TestSkillsInstall_SymlinkedDirOutsideRoot_RefusesWithNothingWritten(t *testing.T) {
	bin := buildEngineBinary(t)
	dir, elsewhere := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(dir, ".claude", "skills")); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := runEngine(t, bin, dir, "skills", "install", "--dir", dir, "--json")

	if code != 1 || !strings.Contains(stderr, "outside") {
		t.Errorf("exit %d, stderr %q; want 1 naming the outside target", code, stderr)
	}
	if n := countFiles(t, elsewhere); n != 0 {
		t.Errorf("%d files written through the symlink", n)
	}
	if n := countFiles(t, filepath.Join(dir, ".agents")); n != 0 {
		t.Errorf("%d files written under .agents before the refusal", n)
	}
}

func TestSkillsInstall_SymlinkedSkillFile_RefusesWithNothingWritten(t *testing.T) {
	bin := buildEngineBinary(t)
	dir, elsewhere := t.TempDir(), t.TempDir()
	victim := filepath.Join(elsewhere, "victim.md")
	if err := os.WriteFile(victim, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(dir, ".claude", "skills", "attest")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(skillDir, "SKILL.md")); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := runEngine(t, bin, dir, "skills", "install", "--dir", dir, "--json")

	if code != 1 || !strings.Contains(stderr, "symlink") {
		t.Errorf("exit %d, stderr %q; want 1 naming the symlink", code, stderr)
	}
	if got, _ := os.ReadFile(victim); string(got) != "keep" {
		t.Errorf("symlink target overwritten: %q", got)
	}
	if n := countFiles(t, filepath.Join(dir, ".agents")); n != 0 {
		t.Errorf("%d files written under .agents before the refusal", n)
	}
}

func TestHelpFlag_ExitsZero(t *testing.T) {
	bin := buildEngineBinary(t)
	for _, args := range [][]string{
		{"policy", "evaluate", "-h"},
		{"version", "-h"},
		{"cache", "stats", "-h"},
		{"skills", "install", "-h"},
		{"skills", "list", "-h"},
	} {
		_, _, code := runEngine(t, bin, t.TempDir(), args...)
		if code != 0 {
			t.Errorf("attest-engine %s: exit %d, want 0", strings.Join(args, " "), code)
		}
	}
}
