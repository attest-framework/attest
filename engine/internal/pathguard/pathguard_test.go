package pathguard_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/attest-framework/attest/engine/internal/pathguard"
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
	t.Cleanup(func() { r.Close() })
	return r, root, outside
}

func TestReadFile_InsideRoot_Reads(t *testing.T) {
	r, root, _ := tree(t)
	real, _ := filepath.EvalSymlinks(root)
	for _, p := range []string{"sub/a.json", "./sub/../sub/a.json", filepath.Join(real, "sub", "a.json"), filepath.Join(root, "sub", "a.json")} {
		data, err := r.ReadFile(p, 100)
		if err != nil || string(data) != `{"v":1}` {
			t.Errorf("%q: %q, %v", p, data, err)
		}
	}
}

func TestReadFile_Escapes_Rejected(t *testing.T) {
	r, root, outside := tree(t)
	links := map[string]string{
		"dirlink":  outside,
		"filelink": filepath.Join(outside, "b.json"),
		"relative": filepath.Join("..", "outside", "b.json"),
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Skip(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "dirlink"), filepath.Join(root, "chain")); err != nil {
		t.Skip(err)
	}
	for _, p := range []string{
		"../outside/b.json", "sub/../../outside/b.json", filepath.Join(outside, "b.json"),
		"dirlink/b.json", "filelink", "chain/b.json", "relative", filepath.Dir(root), root,
	} {
		if data, err := r.ReadFile(p, 100); err == nil {
			t.Errorf("%q read %q, want an error", p, data)
		}
	}
}

func TestReadFile_SymlinkInsideRoot_Followed(t *testing.T) {
	r, root, _ := tree(t)
	if err := os.Symlink(filepath.Join("sub", "a.json"), filepath.Join(root, "alias.json")); err != nil {
		t.Skip(err)
	}
	if _, err := r.ReadFile("alias.json", 100); err != nil {
		t.Errorf("relative symlink inside the root: %v", err)
	}
}

func TestReadFile_SiblingWithSharedPrefix_Rejected(t *testing.T) {
	r, root, _ := tree(t)
	sibling := root + "-evil"
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sibling, "x.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadFile(filepath.Join(sibling, "x.json"), 100); err == nil {
		t.Error("a sibling directory sharing the root's name prefix was read")
	}
}

// The error for a path outside the root must not depend on whether it exists.
func TestReadFile_OutsidePaths_ErrorDoesNotRevealExistence(t *testing.T) {
	r, root, outside := tree(t)
	if err := os.Symlink(outside, filepath.Join(root, "outlink")); err != nil {
		t.Skip(err)
	}
	pairs := [][2]string{
		{"outlink/b.json", "outlink/nonexistent.json"},
		{filepath.Join(outside, "b.json") + "/x", filepath.Join(outside, "missing") + "/x"},
		{"../outside/b.json", "../outside/missing.json"},
		{filepath.Join(outside, "b.json"), filepath.Join(outside, "missing.json")},
	}
	for _, pr := range pairs {
		_, e1 := r.ReadFile(pr[0], 100)
		_, e2 := r.ReadFile(pr[1], 100)
		if e1 == nil || e2 == nil {
			t.Fatalf("%v: expected errors, got %v / %v", pr, e1, e2)
		}
		var u1, u2 *pathguard.ErrUnreadable
		if !errors.As(e1, &u1) || !errors.As(e2, &u2) {
			t.Errorf("%v: errors are %T and %T, want *ErrUnreadable", pr, e1, e2)
			continue
		}
		strip := func(err error, p string) string { return strings.ReplaceAll(err.Error(), p, "<path>") }
		if strip(e1, pr[0]) != strip(e2, pr[1]) {
			t.Errorf("%v: messages differ: %q vs %q", pr, e1, e2)
		}
	}
}

func TestReadFile_NotRegular_Rejected(t *testing.T) {
	r, _, _ := tree(t)
	if _, err := r.ReadFile("sub", 100); err == nil || !strings.Contains(err.Error(), "not a regular file") {
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

func TestNewRoot_Unusable_Rejected(t *testing.T) {
	_, root, _ := tree(t)
	if _, err := pathguard.NewRoot(filepath.Join(root, "sub", "a.json")); err == nil {
		t.Error("a file was accepted as a root")
	}
	if _, err := pathguard.NewRoot(filepath.Join(root, "missing")); err == nil {
		t.Error("a missing directory was accepted as a root")
	}
}

func TestWriteAtomic_KeepsModeOfExistingFile(t *testing.T) {
	r, root, _ := tree(t)
	target := filepath.Join(root, "cfg.json")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, mode, exists, err := r.ReadExisting("cfg.json", 100)
	if err != nil || !exists || string(data) != "old" || mode != 0o600 {
		t.Fatalf("ReadExisting: %q %v %v %v", data, mode, exists, err)
	}
	if err := r.WriteAtomic("cfg.json", []byte("new"), mode, exists); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(target)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", info.Mode().Perm())
	}
	if got, _ := os.ReadFile(target); string(got) != "new" {
		t.Errorf("content %q", got)
	}
}

func TestWriteAtomic_NewFileIs0644AndLeavesNoTemp(t *testing.T) {
	r, root, _ := tree(t)
	if err := r.WriteAtomic("deep/dir/new.json", []byte("x"), 0, false); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(root, "deep", "dir", "new.json"))
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("%v %v", info, err)
	}
	entries, _ := os.ReadDir(filepath.Join(root, "deep", "dir"))
	if len(entries) != 1 {
		t.Errorf("leftover files: %v", entries)
	}
}

func TestWriteAtomic_ThroughSymlinkOutside_Refused(t *testing.T) {
	r, root, outside := tree(t)
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skip(err)
	}
	if err := r.WriteAtomic("link/new.json", []byte("x"), 0, false); err == nil {
		t.Error("write through an outside symlink succeeded")
	}
	if _, err := os.Stat(filepath.Join(outside, "new.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("file created outside the root: %v", err)
	}
}

func TestReadExisting_Missing_ReportsAbsent(t *testing.T) {
	r, _, _ := tree(t)
	if _, _, exists, err := r.ReadExisting("nope.json", 10); exists || err != nil {
		t.Errorf("exists=%v err=%v", exists, err)
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

func TestResolveDestination_AbsoluteSymlinkInsideRoot_Followed(t *testing.T) {
	r, root, _ := tree(t)
	if err := os.Symlink(filepath.Join(root, "sub"), filepath.Join(root, "abs")); err != nil {
		t.Skip(err)
	}
	got, err := pathguard.ResolveDestination(r.Path(), "abs/new.json")
	if err != nil || !strings.HasSuffix(got, filepath.Join("sub", "new.json")) {
		t.Errorf("%q, %v", got, err)
	}
}

func TestRefuseAbsoluteSymlinks_InsideAndOutside_Refused(t *testing.T) {
	r, root, outside := tree(t)
	for name, target := range map[string]string{"inside": filepath.Join(root, "sub"), "outside": outside} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Skip(err)
		}
		err := pathguard.RefuseAbsoluteSymlinks(r.Path(), name+"/new.json")
		if err == nil || !strings.Contains(err.Error(), "absolute symlink; use a relative link") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := os.Symlink("sub", filepath.Join(root, "rel")); err != nil {
		t.Skip(err)
	}
	if err := pathguard.RefuseAbsoluteSymlinks(r.Path(), "rel/new.json"); err != nil {
		t.Errorf("relative symlink: %v", err)
	}
	if err := pathguard.RefuseAbsoluteSymlinks(r.Path(), "missing/new.json"); err != nil {
		t.Errorf("missing path: %v", err)
	}
}
