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

// Select computes the selection for the repository at repo against base.
func Select(repo, base string) (*Result, error) {
	changed, err := changedPaths(repo, base)
	if err != nil {
		return nil, err
	}
	return selectFor(repo, changed)
}

func selectFor(repo string, changed []string) (*Result, error) {
	res := &Result{Mode: ModeSubset, Tests: []string{}, Reasons: []Reason{}}
	for _, p := range changed {
		res.Reasons = append(res.Reasons, Reason{Path: p, Class: Classify(p)})
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
	var testFiles []string
	for _, f := range candidates {
		if isTestFile(f) {
			testFiles = append(testFiles, f)
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

	if len(fixtures) > 0 {
		sources := map[string][]byte{}
		for _, f := range testFiles {
			data, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(f)))
			if err != nil {
				return nil, fmt.Errorf("read test file %s: %w", f, err)
			}
			sources[f] = data
		}
		for _, fx := range fixtures {
			name, err := fixtureName(repo, fx)
			if err != nil {
				return allResult(res.Reasons), nil
			}
			referenced := false
			for _, f := range testFiles {
				if references(sources[f], name) {
					selected[f] = true
					referenced = true
				}
			}
			if !referenced {
				return allResult(res.Reasons), nil
			}
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
