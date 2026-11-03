package assets_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/attest-ai/attest/engine/internal/assets"
	"github.com/attest-ai/attest/engine/internal/assets/assetsync"
)

const (
	repoRoot    = "../../.."
	fixtureRoot = "testdata/drift"
)

// TestStagedAssetsAndManifests_MatchSources fails when a staged guide or
// skill, or a plugin manifest, differs from what go generate would write.
func TestStagedAssetsAndManifests_MatchSources(t *testing.T) {
	msgs, err := assetsync.Drift(repoRoot)
	if err != nil {
		t.Fatalf("drift: %v", err)
	}
	if len(msgs) > 0 {
		t.Fatalf("generated files drifted from their sources:\n  %s\nfix: %s",
			strings.Join(msgs, "\n  "), assetsync.FixCommand)
	}
}

func TestDrift_FixtureTree_ReportsEveryMismatchKind(t *testing.T) {
	msgs, err := assetsync.Drift(fixtureRoot)
	if err != nil {
		t.Fatalf("drift: %v", err)
	}

	want := []string{
		".claude-plugin/marketplace.json: missing (generated from VERSION and the plugin metadata in assetsync)",
		".claude-plugin/plugin.json: stale (differs from VERSION and the plugin metadata in assetsync)",
		".codex-plugin/plugin.json: missing (generated from VERSION and the plugin metadata in assetsync)",
		".cursor-plugin/plugin.json: missing (generated from VERSION and the plugin metadata in assetsync)",
		"engine/internal/assets/guides/beta.md: stale (differs from docs/guides/beta.md)",
		"engine/internal/assets/guides/gamma.md: missing (generated from docs/guides/gamma.md)",
		"engine/internal/assets/guides/stray.md: extra (no source)",
		"engine/internal/assets/skills/demo/.notes: missing (generated from skills/demo/.notes)",
		"engine/internal/assets/skills/old/SKILL.md: extra (no source)",
	}
	if !slices.Equal(msgs, want) {
		t.Fatalf("drift messages:\n  %s\nwant:\n  %s", strings.Join(msgs, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestGenerate_FixtureTree_ClearsDriftAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS(fixtureRoot)); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := assetsync.Generate(root); err != nil {
			t.Fatalf("generate: %v", err)
		}
		msgs, err := assetsync.Drift(root)
		if err != nil || len(msgs) > 0 {
			t.Fatalf("after generate: err=%v drift=%v", err, msgs)
		}
	}

	stage := filepath.Join(root, "engine", "internal", "assets")
	for _, gone := range []string{"guides/stray.md", "skills/old", "skills/.gitkeep"} {
		if _, err := os.Stat(filepath.Join(stage, gone)); !os.IsNotExist(err) {
			t.Errorf("%s: still present after generate (err=%v)", gone, err)
		}
	}
	manifest, err := os.ReadFile(filepath.Join(root, ".codex-plugin", "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifest), `"version": "1.2.3-rc.1"`) {
		t.Errorf("codex manifest lacks the fixture VERSION:\n%s", manifest)
	}
}

func TestTopics_EveryGuideHasTitle(t *testing.T) {
	topics, err := assets.Topics()
	if err != nil {
		t.Fatalf("topics: %v", err)
	}
	if len(topics) == 0 {
		t.Fatal("no embedded guides")
	}
	for _, tp := range topics {
		if tp.Title == "" {
			t.Errorf("guide %q has an empty title", tp.Name)
		}
	}
}

func TestGuide_TraversalName_NotFound(t *testing.T) {
	for _, name := range []string{"", "../assets", ".hidden", "a/b"} {
		if _, _, err := assets.Guide(name); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Guide(%q): err=%v, want not-exist", name, err)
		}
	}
}

func TestSkills_EverySkillHasNameAndDescription(t *testing.T) {
	skills, err := assets.Skills()
	if err != nil {
		t.Fatalf("skills: %v", err)
	}
	if len(skills) == 0 {
		t.Fatal("no embedded skills")
	}
}
