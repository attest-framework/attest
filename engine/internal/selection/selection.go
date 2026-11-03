// Package selection decides which tests a change set needs to run.
//
// A change set is every path that differs between the merge base of a ref and
// HEAD, plus the working tree. Touched test files and the tests that reference
// a touched fixture run; markdown is ignored; lockfiles, dependency manifests,
// attest config and every path the classifier does not know select everything.
package selection

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Selection modes.
const (
	ModeAll    = "all"
	ModeSubset = "subset"
)

// Path classes.
const (
	ClassTest    = "test"
	ClassFixture = "fixture"
	ClassIgnored = "ignored"
	// ClassAll marks a path that names a reason to run everything: a lockfile,
	// a dependency manifest, attest config or a world definition.
	ClassAll = "all"
	// ClassUnknown marks a path the classifier does not recognize.
	ClassUnknown = "unknown"
)

// Reason records why a changed path affected the selection.
type Reason struct {
	Path  string `json:"path"`
	Class string `json:"class"`
}

// Result is the selection for one change set. Tests is empty in ModeAll.
type Result struct {
	Mode    string   `json:"mode"`
	Tests   []string `json:"tests"`
	Reasons []Reason `json:"reasons"`
}

var (
	allBasenames = map[string]bool{
		"uv.lock": true, "poetry.lock": true, "package-lock.json": true,
		"pnpm-lock.yaml": true, "yarn.lock": true, "bun.lockb": true, "bun.lock": true,
		"pyproject.toml": true, "package.json": true, "world.yaml": true,
	}
	requirementsRe = regexp.MustCompile(`^requirements.*\.txt$`)
	attestConfigRe = regexp.MustCompile(`^attest(\..+)?\.(ya?ml|json|toml)$`)
	pytestTestRe   = regexp.MustCompile(`^(test_.+|.+_test)\.py$`)
	vitestTestRe   = regexp.MustCompile(`^.+\.test\.(ts|tsx|js|mjs)$`)
)

// Classify returns the class of one repo-relative, slash-separated path.
func Classify(p string) string {
	base := path.Base(p)
	switch {
	case strings.HasSuffix(base, ".attest.json"):
		return ClassFixture
	case strings.HasSuffix(base, ".md"):
		return ClassIgnored
	case allBasenames[base], requirementsRe.MatchString(base),
		attestConfigRe.MatchString(base), strings.HasSuffix(base, ".world.yaml"):
		return ClassAll
	case base == "conftest.py", pytestTestRe.MatchString(base), vitestTestRe.MatchString(base):
		return ClassTest
	default:
		return ClassUnknown
	}
}

func isTestFile(p string) bool {
	base := path.Base(p)
	return pytestTestRe.MatchString(base) || vitestTestRe.MatchString(base)
}

// Select computes the selection for the repository at repo against base. The
// diff covers the whole git repository: a lockfile or source change outside
// repo still selects everything. Paths in the result are relative to repo.
func Select(repo, base string) (*Result, error) {
	top, prefix, err := repoRoot(repo)
	if err != nil {
		return nil, err
	}
	changed, err := changedPaths(top, base)
	if err != nil {
		return nil, err
	}
	var reasons []Reason
	for _, p := range changed {
		class := Classify(p)
		if prefix != "" {
			if rel, ok := strings.CutPrefix(p, prefix+"/"); ok {
				p = rel
			} else if class != ClassAll && class != ClassUnknown {
				continue // a test, fixture or doc outside repo is not repo's concern
			}
		}
		reasons = append(reasons, Reason{Path: p, Class: class})
	}
	return selectFor(repo, reasons)
}

// repoRoot returns the git top-level directory of repo and repo's path
// relative to it ("" when repo is the top level), both slash-separated.
func repoRoot(repo string) (top, prefix string, err error) {
	out, err := git(repo, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", err
	}
	top = strings.TrimSpace(string(out))
	absTop, err := filepath.EvalSymlinks(top)
	if err != nil {
		return "", "", fmt.Errorf("resolve %s: %w", top, err)
	}
	absRepo, err := filepath.Abs(repo)
	if err != nil {
		return "", "", err
	}
	absRepo, err = filepath.EvalSymlinks(absRepo)
	if err != nil {
		return "", "", fmt.Errorf("resolve %s: %w", repo, err)
	}
	rel, err := filepath.Rel(absTop, absRepo)
	if err != nil {
		return "", "", err
	}
	if rel == "." {
		rel = ""
	}
	return top, filepath.ToSlash(rel), nil
}

func selectFor(repo string, reasons []Reason) (*Result, error) {
	res := &Result{Mode: ModeSubset, Tests: []string{}, Reasons: reasons}
	if res.Reasons == nil {
		res.Reasons = []Reason{}
	}
	for _, r := range res.Reasons {
		if r.Class == ClassAll || r.Class == ClassUnknown {
			return allResult(res.Reasons), nil
		}
	}

	candidates, err := listFiles(repo)
	if err != nil {
		return nil, err
	}
	var testFiles, otherSources []string
	for _, f := range candidates {
		switch {
		case isTestFile(f):
			testFiles = append(testFiles, f)
		case isSourceFile(f):
			otherSources = append(otherSources, f)
		}
	}

	selected := map[string]bool{}
	var fixtures []string
	for _, r := range res.Reasons {
		switch r.Class {
		case ClassTest:
			if path.Base(r.Path) == "conftest.py" {
				dir := path.Dir(r.Path)
				for _, f := range testFiles {
					if dir == "." || strings.HasPrefix(f, dir+"/") {
						selected[f] = true
					}
				}
			} else if exists(repo, r.Path) {
				selected[r.Path] = true
			}
		case ClassFixture:
			fixtures = append(fixtures, r.Path)
		}
	}

	for _, fx := range fixtures {
		name, err := fixtureName(repo, fx)
		if err != nil {
			return allResult(res.Reasons), nil
		}
		referenced := false
		for _, f := range testFiles {
			hit, err := fileReferences(repo, f, name)
			if err != nil {
				return nil, err
			}
			if hit {
				selected[f] = true
				referenced = true
			}
		}
		for _, f := range otherSources {
			hit, err := fileReferences(repo, f, name)
			if err != nil {
				return nil, err
			}
			if !hit {
				continue
			}
			referenced = true
			if path.Base(f) != "conftest.py" {
				// A helper module can reach any test; the diff cannot say which.
				return allResult(res.Reasons), nil
			}
			dir := path.Dir(f)
			for _, t := range testFiles {
				if dir == "." || strings.HasPrefix(t, dir+"/") {
					selected[t] = true
				}
			}
		}
		if !referenced {
			return allResult(res.Reasons), nil
		}
	}

	for f := range selected {
		res.Tests = append(res.Tests, f)
	}
	sort.Strings(res.Tests)
	return res, nil
}

func allResult(reasons []Reason) *Result {
	return &Result{Mode: ModeAll, Tests: []string{}, Reasons: reasons}
}

// fixtureName reads the capture name of a fixture file. A fixture that is
// deleted, unreadable or nameless has no name to match, which the caller turns
// into a full run.
func fixtureName(repo, rel string) (string, error) {
	data, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(rel)))
	if err != nil {
		return "", fmt.Errorf("read fixture %s: %w", rel, err)
	}
	var f struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return "", fmt.Errorf("decode fixture %s: %w", rel, err)
	}
	if f.Name == "" {
		return "", fmt.Errorf("fixture %s has no name", rel)
	}
	return f.Name, nil
}

// references reports whether src holds the capture name as a quoted string
// literal, or a string literal that names the fixture file (<name>.attest.json).
func references(src []byte, name string) bool {
	for _, q := range []string{`"`, `'`, "`"} {
		if bytes.Contains(src, []byte(q+name+q)) {
			return true
		}
	}
	return bytes.Contains(src, []byte(name+".attest.json"))
}

func isSourceFile(p string) bool {
	switch path.Ext(p) {
	case ".py", ".ts", ".tsx", ".js", ".mjs":
		return true
	}
	return false
}

func fileReferences(repo, rel, name string) (bool, error) {
	data, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(rel)))
	if err != nil {
		return false, fmt.Errorf("read %s: %w", rel, err)
	}
	return references(data, name), nil
}

func exists(repo, rel string) bool {
	_, err := os.Stat(filepath.Join(repo, filepath.FromSlash(rel)))
	return err == nil
}

// git runs git in repo and returns stdout. A failure carries git's stderr.
func git(repo string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// changedPaths lists, relative to repo and sorted, the paths that differ
// between the merge base of base and HEAD and the working tree, including
// untracked files. Renames list both the old and the new path.
func changedPaths(repo, base string) ([]string, error) {
	if base == "" || strings.HasPrefix(base, "-") {
		return nil, fmt.Errorf("invalid base ref %q", base)
	}
	mb, err := git(repo, "merge-base", base, "HEAD")
	if err != nil {
		return nil, err
	}
	mergeBase := strings.TrimSpace(string(mb))
	tracked, err := git(repo, "diff", "--name-only", "--no-renames", "--relative", "-z", mergeBase)
	if err != nil {
		return nil, err
	}
	untracked, err := git(repo, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, out := range [][]byte{tracked, untracked} {
		for _, p := range strings.Split(string(out), "\x00") {
			if p != "" {
				set[p] = true
			}
		}
	}
	paths := make([]string, 0, len(set))
	for p := range set {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths, nil
}

// listFiles lists the tracked and untracked, non-ignored files under repo.
func listFiles(repo string) ([]string, error) {
	out, err := git(repo, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" && exists(repo, p) {
			files = append(files, p)
		}
	}
	sort.Strings(files)
	return files, nil
}
