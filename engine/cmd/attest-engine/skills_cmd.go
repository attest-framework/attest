package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/attest-ai/attest/engine/internal/assets"
)

func init() {
	register(command{
		name:    "skills",
		summary: "list the embedded agent skills, or install them: list|install [--dir D] [--json]",
		run:     func(args []string) { os.Exit(runSkills(args)) },
		subs: []subcommand{
			{name: "list", schema: "cli/skills-list.schema.json"},
			{name: "install", schema: "cli/skills-install.schema.json"},
		},
		exits: []exitCode{
			{Code: 0, Meaning: "skills listed or installed"},
			{Code: 1, Meaning: "an embedded skill is invalid or a destination was refused or a file could not be written"},
			{Code: 2, Meaning: "usage error"},
		},
	})
}

const skillsUsage = "usage: attest-engine skills list [--json]\n       attest-engine skills install [--dir D] [--json]"

// skillTargets are the directories, relative to the install root, that each
// skill is written under: the cross-agent location and Claude Code's.
var skillTargets = []string{".agents/skills", ".claude/skills"}

// skillsInstallResult is the --json output of `skills install`.
type skillsInstallResult struct {
	Dir       string   `json:"dir"`
	Created   []string `json:"created"`
	Updated   []string `json:"updated"`
	Unchanged []string `json:"unchanged"`
}

func runSkills(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, skillsUsage)
		return 2
	}
	switch args[0] {
	case "list":
		return runSkillsList(args[1:])
	case "install":
		return runSkillsInstall(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "skills: unknown subcommand %q\n%s\n", args[0], skillsUsage)
		return 2
	}
}

// parseSkillsFlags parses fs and rejects positional arguments.
func parseSkillsFlags(fs *flag.FlagSet, args []string, help *bool) bool {
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		*help = errors.Is(err, flag.ErrHelp)
		return false
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "skills %s: unexpected argument %q\n%s\n", fs.Name(), fs.Arg(0), skillsUsage)
		return false
	}
	return true
}

func runSkillsList(args []string) int {
	fset := flag.NewFlagSet("list", flag.ContinueOnError)
	asJSON := fset.Bool("json", false, `print {"skills":[{"name","description"}]} as JSON`)
	var help bool
	if !parseSkillsFlags(fset, args, &help) {
		if help {
			return 0
		}
		return 2
	}
	skills, err := assets.Skills()
	if err != nil {
		fmt.Fprintf(os.Stderr, "skills list: %v\n", err)
		return 1
	}
	if *asJSON {
		emitJSON(map[string][]assets.Skill{"skills": skills})
		return 0
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, s := range skills {
		fmt.Fprintf(tw, "%s\t%s\n", s.Name, s.Description)
	}
	if err := tw.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "skills list: %v\n", err)
		return 1
	}
	return 0
}

func runSkillsInstall(args []string) int {
	fset := flag.NewFlagSet("install", flag.ContinueOnError)
	dir := fset.String("dir", ".", "project directory to install into")
	asJSON := fset.Bool("json", false, `print {"dir","created","updated","unchanged"} as JSON`)
	var help bool
	if !parseSkillsFlags(fset, args, &help) {
		if help {
			return 0
		}
		return 2
	}
	root, err := filepath.Abs(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "skills install: %v\n", err)
		return 2
	}
	res, err := installSkills(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "skills install: %v\n", err)
		return 1
	}
	if *asJSON {
		emitJSON(res)
		return 0
	}
	fmt.Printf("installed attest skills into %s: %d created, %d updated, %d unchanged\n",
		root, len(res.Created), len(res.Updated), len(res.Unchanged))
	return 0
}

// skillFile is one embedded skill file and where it lands.
type skillFile struct {
	rel  string // logical path relative to root, forward slashes
	real string // fully resolved destination
	data []byte
}

// installSkills writes every file of every embedded skill under each skill
// target of root. Only files of embedded skills are written; nothing is
// deleted, and directories of other skills are never touched. Every
// destination is validated before the first write, so a refusal leaves the
// tree untouched. Directory symlinks are followed only when they resolve
// inside root; files that are symlinks or otherwise not regular are refused.
// Destinations sharing a resolved path are written once and reported under
// each logical path with the same state.
func installSkills(root string) (skillsInstallResult, error) {
	res := skillsInstallResult{Dir: root, Created: []string{}, Updated: []string{}, Unchanged: []string{}}
	skills, err := assets.Skills()
	if err != nil {
		return res, err
	}
	realRoot := root
	if r, err := filepath.EvalSymlinks(root); err == nil {
		realRoot = r
	} else if !errors.Is(err, fs.ErrNotExist) {
		return res, err
	}

	tree := assets.SkillsFS()
	var plan []skillFile
	for _, s := range skills {
		err := fs.WalkDir(tree, s.Name, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := fs.ReadFile(tree, p)
			if err != nil {
				return err
			}
			for _, target := range skillTargets {
				rel := path.Join(target, p)
				real, err := resolveSkillDestination(realRoot, rel)
				if err != nil {
					return err
				}
				plan = append(plan, skillFile{rel: rel, real: real, data: data})
			}
			return nil
		})
		if err != nil {
			return res, fmt.Errorf("skill %q: %w", s.Name, err)
		}
	}

	states := map[string]string{}
	for _, f := range plan {
		if _, done := states[f.real]; done {
			continue
		}
		state, err := writeSkillFile(f.real, f.data)
		if err != nil {
			return res, err
		}
		states[f.real] = state
	}
	for _, f := range plan {
		switch states[f.real] {
		case "created":
			res.Created = append(res.Created, f.rel)
		case "updated":
			res.Updated = append(res.Updated, f.rel)
		default:
			res.Unchanged = append(res.Unchanged, f.rel)
		}
	}
	sort.Strings(res.Created)
	sort.Strings(res.Updated)
	sort.Strings(res.Unchanged)
	return res, nil
}

// resolveSkillDestination returns the real path rel (relative to realRoot)
// would be written to. It follows existing directory symlinks, refuses any that
// resolve outside realRoot, refuses an existing non-directory in the path, and
// refuses an existing destination that is not a regular file.
func resolveSkillDestination(realRoot, rel string) (string, error) {
	parts := strings.Split(rel, "/")
	cur := realRoot
	for i, part := range parts {
		next := filepath.Join(cur, part)
		info, err := os.Lstat(next)
		if errors.Is(err, fs.ErrNotExist) {
			return filepath.Join(append([]string{cur}, parts[i:]...)...), nil
		}
		if err != nil {
			return "", err
		}
		last := i == len(parts)-1
		if info.Mode()&os.ModeSymlink != 0 {
			if last {
				return "", fmt.Errorf("%s is a symlink; refusing to overwrite it", next)
			}
			resolved, err := filepath.EvalSymlinks(next)
			if errors.Is(err, fs.ErrNotExist) {
				return "", fmt.Errorf("%s is a symlink whose target does not exist yet; create the target directory or remove the link, then re-run", next)
			}
			if err != nil {
				return "", fmt.Errorf("%s: %w", next, err)
			}
			if !within(realRoot, resolved) {
				return "", fmt.Errorf("%s resolves to %s, outside %s; refusing to install through it", next, resolved, realRoot)
			}
			next = resolved
			if info, err = os.Stat(next); err != nil {
				return "", err
			}
		}
		if last {
			if !info.Mode().IsRegular() {
				return "", fmt.Errorf("%s exists and is not a regular file", next)
			}
		} else if !info.IsDir() {
			return "", fmt.Errorf("%s exists and is not a directory", next)
		}
		cur = next
	}
	return cur, nil
}

// within reports whether p is root or lies below it.
func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// writeSkillFile makes dst hold data and reports "created", "updated", or
// "unchanged". dst was validated by resolveSkillDestination.
func writeSkillFile(dst string, data []byte) (string, error) {
	state := "created"
	cur, err := os.ReadFile(dst)
	switch {
	case err == nil:
		if bytes.Equal(cur, data) {
			return "unchanged", nil
		}
		state = "updated"
	case !errors.Is(err, fs.ErrNotExist):
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return "", err
	}
	return state, nil
}
