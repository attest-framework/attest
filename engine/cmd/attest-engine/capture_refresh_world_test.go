package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/attest-framework/attest/engine/internal/capture"
	"github.com/attest-framework/attest/engine/internal/world"
)

const refreshWorldYAML = `world_version: 1
base_url_env: PAYMENTS_BASE_URL
resources:
  orders:
    path: /orders
    id_field: order_id
    routes: [get]
    seed:
      - {order_id: O-1, amount_cents: 100}
`

func worldYAMLSha(t *testing.T, yaml string) string {
	t.Helper()
	def, err := world.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	w, err := world.New(def)
	if err != nil {
		t.Fatal(err)
	}
	return w.Snapshot().DefinitionSHA256
}

func TestResolveRefreshWorld_HostsTheVerifiedWorldNotAReload(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repo, "w.yaml")
	if err := os.WriteFile(path, []byte(refreshWorldYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	recorded, err := json.Marshal(map[string]string{"definition_sha256": worldYAMLSha(t, refreshWorldYAML)})
	if err != nil {
		t.Fatal(err)
	}
	f := &capture.Fixture{World: recorded}
	f.Source.World = &capture.SourceWorld{File: "w.yaml"}
	o := &captureOptions{refresh: "x", outDir: filepath.Join(repo, "fixtures")}

	if err := resolveRefreshWorld(o, f); err != nil {
		t.Fatal(err)
	}
	// The file changes after verification, before the prompt returns.
	changed := strings.Replace(refreshWorldYAML, "amount_cents: 100", "amount_cents: 999", 1)
	if err := os.WriteFile(path, []byte(changed), 0o644); err != nil {
		t.Fatal(err)
	}

	if o.refreshWorld == nil {
		t.Fatal("the verified world was not kept for hosting")
	}
	if got, want := o.refreshWorld.Snapshot().DefinitionSHA256, worldYAMLSha(t, refreshWorldYAML); got != want {
		t.Errorf("hosted world sha %s, want the verified %s", got, want)
	}
}

func TestRepoRelative_RepoReachedThroughASymlinkedParent_RecordsTheRelativePath(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(filepath.Join(real, "repo", "world"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(real, "repo", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	worldPath := filepath.Join(real, "repo", "world", "w.yaml")
	if err := os.WriteFile(worldPath, []byte(refreshWorldYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	// The fixtures dir is named through the link, the world through the
	// physical path: the same place by two lexical spellings.
	got, err := repoRelative(filepath.Join(link, "repo", "fixtures"), worldPath)

	if err != nil || got != "world/w.yaml" {
		t.Errorf("repoRelative = %q, %v; want world/w.yaml", got, err)
	}
}
