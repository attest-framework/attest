package main_test

import (
	"encoding/json"
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

func TestSkillsInstall_SymlinkedSkillsDir_RefusesAndWritesNothingThroughIt(t *testing.T) {
	bin := buildEngineBinary(t)
	dir, elsewhere := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(dir, ".claude", "skills")); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := runEngine(t, bin, dir, "skills", "install", "--dir", dir, "--json")

	if code == 0 || !strings.Contains(stderr, "symlink") {
		t.Errorf("exit %d, stderr %q; want non-zero naming the symlink", code, stderr)
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Errorf("wrote %d entries through the symlink", len(entries))
	}
}
