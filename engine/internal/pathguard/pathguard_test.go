package pathguard_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/attest-ai/attest/engine/internal/pathguard"
)

func tree(t *testing.T) (*pathguard.Root, string, string) {
	t.Helper()
	base := t.TempDir()
	root, outside := filepath.Join(base, "root"), filepath.Join(base, "outside")
	for _, d := range []string{filepath.Join(root, "sub"), outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{filepath.Join(root, "sub", "a.json"), filepath.Join(outside, "b.json")} {
		if err := os.WriteFile(f, []byte(`{"v":1}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r, err := pathguard.NewRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	return r, root, outside
}

func TestResolveExisting_InsideRoot_ReturnsRealPath(t *testing.T) {
	r, root, _ := tree(t)
	real, _ := filepath.EvalSymlinks(root)
	for _, p := range []string{"sub/a.json", "./sub/../sub/a.json", filepath.Join(real, "sub", "a.json")} {
		got, err := r.ResolveExisting(p)
		if err != nil {
			t.Errorf("%q: %v", p, err)
			continue
		}
		if want := filepath.Join(real, "sub", "a.json"); got != want {
			t.Errorf("%q resolved to %q, want %q", p, got, want)
		}
	}
}

func TestResolveExisting_Escapes_Rejected(t *testing.T) {
	r, root, outside := tree(t)
	if err := os.Symlink(outside, filepath.Join(root, "dirlink")); err != nil {
		t.Skip(err)
	}
	if err := os.Symlink(filepath.Join(outside, "b.json"), filepath.Join(root, "filelink")); err != nil {
		t.Skip(err)
	}
	if err := os.Symlink(filepath.Join(root, "dirlink"), filepath.Join(root, "chain")); err != nil {
		t.Skip(err)
	}
	for _, p := range []string{
		"../outside/b.json", "sub/../../outside/b.json", filepath.Join(outside, "b.json"),
		"dirlink/b.json", "filelink", "chain/b.json", filepath.Dir(root),
	} {
		if got, err := r.ResolveExisting(p); err == nil {
			t.Errorf("%q resolved to %q, want an error", p, got)
		}
	}
}

func TestResolveExisting_SiblingWithSharedPrefix_Rejected(t *testing.T) {
	r, root, _ := tree(t)
	sibling := root + "-evil"
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sibling, "x.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ResolveExisting(filepath.Join(sibling, "x.json")); err == nil {
		t.Error("a sibling directory sharing the root's name prefix was accepted")
	}
}

func TestResolveExisting_NotRegular_Rejected(t *testing.T) {
	r, _, _ := tree(t)
	if _, err := r.ResolveExisting("sub"); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("directory: %v", err)
	}
}

func TestReadFile_OverCap_Rejected(t *testing.T) {
	r, root, _ := tree(t)
	if err := os.WriteFile(filepath.Join(root, "big"), make([]byte, 11), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadFile("big", 10); err == nil {
		t.Error("11 bytes read under a 10 byte cap")
	}
	if data, err := r.ReadFile("big", 11); err != nil || len(data) != 11 {
		t.Errorf("11 bytes under an 11 byte cap: %d bytes, %v", len(data), err)
	}
}

func TestReadFile_ErrorNeverEchoesContents(t *testing.T) {
	r, root, _ := tree(t)
	if err := os.WriteFile(filepath.Join(root, "s.json"), []byte("SECRET-VALUE"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := r.ReadFile("s.json", 3)
	if err == nil || strings.Contains(err.Error(), "SECRET-VALUE") {
		t.Errorf("error %v", err)
	}
}

func TestNewRoot_NotADirectory_Rejected(t *testing.T) {
	r, root, _ := tree(t)
	_ = r
	if _, err := pathguard.NewRoot(filepath.Join(root, "sub", "a.json")); err == nil {
		t.Error("a file was accepted as a root")
	}
	if _, err := pathguard.NewRoot(filepath.Join(root, "missing")); err == nil {
		t.Error("a missing directory was accepted as a root")
	}
}

func TestResolveDestination_SymlinkOutside_Rejected(t *testing.T) {
	r, root, outside := tree(t)
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skip(err)
	}
	if _, err := pathguard.ResolveDestination(r.Path(), "link/new.json"); err == nil {
		t.Error("a destination through an outside symlink was accepted")
	}
	got, err := pathguard.ResolveDestination(r.Path(), "sub/new/file.json")
	if err != nil || !strings.HasSuffix(got, filepath.Join("sub", "new", "file.json")) {
		t.Errorf("new path: %q, %v", got, err)
	}
}
