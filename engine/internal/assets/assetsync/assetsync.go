// Package assetsync stages the repository's guides and skills into the engine
// module for go:embed and generates the plugin manifests, and reports drift
// between those generated files and their sources.
//
// Every function takes the repository root, so tests can run them on a
// fixture tree. All paths in results are repository-relative with forward
// slashes.
package assetsync

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Repository-relative locations of the sources and the staged copies.
const (
	guidesSource = "docs/guides"
	skillsSource = "skills"
	versionFile  = "VERSION"
	stageDir     = "engine/internal/assets"
	guidesStage  = stageDir + "/guides"
	skillsStage  = stageDir + "/skills"
)

// FixCommand regenerates every generated file; drift reports name it.
const FixCommand = "cd engine && go generate ./internal/assets/"

// semverPattern accepts major.minor.patch with an optional pre-release or
// build suffix, such as 1.0.0-rc.3.
var semverPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[+-][0-9A-Za-z.\-]+)?$`)

// file is one generated file: its expected content and what it derives from.
type file struct {
	content []byte
	source  string
}

// managedDirs are the staged directories whose every file must have a source;
// any other file in them is extra.
var managedDirs = []string{guidesStage, skillsStage}

// expected computes every generated file, keyed by repository-relative path.
func expected(repoRoot string) (map[string]file, error) {
	out := map[string]file{}
	if err := addGuides(repoRoot, out); err != nil {
		return nil, err
	}
	if err := addSkills(repoRoot, out); err != nil {
		return nil, err
	}
	version, err := readVersion(repoRoot)
	if err != nil {
		return nil, err
	}
	manifests, err := renderManifests(version)
	if err != nil {
		return nil, err
	}
	for rel, content := range manifests {
		out[rel] = file{content: content, source: versionFile + " and the plugin metadata in assetsync"}
	}
	return out, nil
}

// addGuides stages docs/guides/*.md (top level only) as guides/<name>.md.
func addGuides(repoRoot string, out map[string]file) error {
	entries, err := os.ReadDir(filepath.Join(repoRoot, filepath.FromSlash(guidesSource)))
	if err != nil {
		return fmt.Errorf("read guides: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		src := guidesSource + "/" + e.Name()
		if !e.Type().IsRegular() {
			return fmt.Errorf("%s: not a regular file", src)
		}
		data, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(src)))
		if err != nil {
			return err
		}
		out[guidesStage+"/"+e.Name()] = file{content: data, source: src}
	}
	return nil
}

// addSkills stages every file under skills/, dotfiles included, except
// .gitkeep placeholders.
func addSkills(repoRoot string, out map[string]file) error {
	root := filepath.Join(repoRoot, skillsSource)
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("read skills: %w", err)
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		src := skillsSource + "/" + rel
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s: not a regular file (symlinks cannot be embedded)", src)
		}
		base := path.Base(rel)
		if base == ".gitkeep" || base == ".DS_Store" {
			return nil
		}
		// A staged .go or go.mod file would change the engine module's build.
		if strings.HasSuffix(base, ".go") || base == "go.mod" {
			return fmt.Errorf("%s: Go source files cannot be staged into the engine module", src)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[skillsStage+"/"+rel] = file{content: data, source: src}
		return nil
	})
}

// readVersion returns the canonical version from the repository's VERSION.
func readVersion(repoRoot string) (string, error) {
	data, err := os.ReadFile(filepath.Join(repoRoot, versionFile))
	if err != nil {
		return "", fmt.Errorf("read VERSION: %w", err)
	}
	v := strings.TrimSpace(string(data))
	if !semverPattern.MatchString(v) {
		return "", fmt.Errorf("VERSION %q is not a semantic version", v)
	}
	return v, nil
}

// stagedFiles lists every file currently under the managed directories.
func stagedFiles(repoRoot string) ([]string, error) {
	var out []string
	for _, dir := range managedDirs {
		root := filepath.Join(repoRoot, filepath.FromSlash(dir))
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) && p == root {
					return nil
				}
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(repoRoot, p)
			if err != nil {
				return err
			}
			out = append(out, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("list staged files: %w", err)
		}
	}
	sort.Strings(out)
	return out, nil
}

// Drift compares the staged guides and skills and the plugin manifests under
// repoRoot with their sources. It returns one message per differing file,
// sorted, each naming the file; an empty result means no drift.
func Drift(repoRoot string) ([]string, error) {
	want, err := expected(repoRoot)
	if err != nil {
		return nil, err
	}
	var msgs []string
	for rel, f := range want {
		got, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(rel)))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			msgs = append(msgs, fmt.Sprintf("%s: missing (generated from %s)", rel, f.source))
		case err != nil:
			return nil, err
		case !bytes.Equal(got, f.content):
			msgs = append(msgs, fmt.Sprintf("%s: stale (differs from %s)", rel, f.source))
		}
	}
	staged, err := stagedFiles(repoRoot)
	if err != nil {
		return nil, err
	}
	for _, rel := range staged {
		if _, ok := want[rel]; !ok {
			msgs = append(msgs, fmt.Sprintf("%s: extra (no source)", rel))
		}
	}
	sort.Strings(msgs)
	return msgs, nil
}

// Generate writes every generated file under repoRoot whose content differs,
// and removes staged files (and then empty directories) that have no source.
func Generate(repoRoot string) error {
	want, err := expected(repoRoot)
	if err != nil {
		return err
	}
	for rel, f := range want {
		p := filepath.Join(repoRoot, filepath.FromSlash(rel))
		got, err := os.ReadFile(p)
		if err == nil && bytes.Equal(got, f.content) {
			continue
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, f.content, 0o644); err != nil {
			return err
		}
	}
	staged, err := stagedFiles(repoRoot)
	if err != nil {
		return err
	}
	for _, rel := range staged {
		if _, ok := want[rel]; !ok {
			if err := os.Remove(filepath.Join(repoRoot, filepath.FromSlash(rel))); err != nil {
				return err
			}
		}
	}
	for _, dir := range managedDirs {
		if err := removeEmptyDirs(filepath.Join(repoRoot, filepath.FromSlash(dir)), false); err != nil {
			return err
		}
	}
	return nil
}

// removeEmptyDirs removes every empty directory below dir, and dir itself
// when removeSelf is set and it ends up empty.
func removeEmptyDirs(dir string, removeSelf bool) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	remaining := len(entries)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sub := filepath.Join(dir, e.Name())
		if err := removeEmptyDirs(sub, true); err != nil {
			return err
		}
		if _, err := os.Stat(sub); errors.Is(err, fs.ErrNotExist) {
			remaining--
		}
	}
	if removeSelf && remaining == 0 {
		return os.Remove(dir)
	}
	return nil
}
