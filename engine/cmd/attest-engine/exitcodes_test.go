package main

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const exitTableGuide = "../../../docs/guides/cli.md"

// exitTableDrift compares the exit-code table under the "## Exit codes" heading
// of the Markdown guide md with the declarations in cmds and returns one message
// per missing, extra, different, duplicate or unparseable row.
func exitTableDrift(md string, cmds map[string]command) []string {
	documented := map[string]string{}
	var problems []string
	inSection := false
	for _, line := range strings.Split(md, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			inSection = trimmed == "## Exit codes"
			continue
		}
		if !inSection || !strings.HasPrefix(trimmed, "|") {
			continue
		}
		cells := strings.Split(trimmed, "|")
		if len(cells) == 5 {
			if h := strings.TrimSpace(cells[1]); h == "Command" || strings.HasPrefix(h, "---") {
				continue // header and separator rows
			}
		}
		first := ""
		if len(cells) == 5 {
			first = strings.TrimSpace(cells[1])
		}
		if len(cells) != 5 || cells[0] != "" || cells[4] != "" ||
			len(first) < 3 || !strings.HasPrefix(first, "`") || !strings.HasSuffix(first, "`") {
			problems = append(problems, fmt.Sprintf("unparseable row: %q", trimmed))
			continue
		}
		name := strings.Trim(first, "`")
		code := strings.TrimSpace(cells[2])
		if _, err := strconv.Atoi(code); err != nil {
			problems = append(problems, fmt.Sprintf("code is not an integer: %s %q", name, code))
			continue
		}
		key := name + " " + code
		if _, dup := documented[key]; dup {
			problems = append(problems, "duplicate row: "+key)
			continue
		}
		documented[key] = strings.TrimSpace(cells[3])
	}
	declared := map[string]string{}
	for _, c := range cmds {
		for _, e := range c.exits {
			declared[fmt.Sprintf("%s %d", c.name, e.Code)] = e.Meaning
		}
	}
	for key, meaning := range declared {
		got, ok := documented[key]
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("missing row: %s (%q)", key, meaning))
		case got != meaning:
			problems = append(problems, fmt.Sprintf("different meaning for %s: documented %q, registry %q", key, got, meaning))
		}
	}
	for key := range documented {
		if _, ok := declared[key]; !ok {
			problems = append(problems, "extra row: "+key)
		}
	}
	sort.Strings(problems)
	return problems
}

func TestExitCodeTable_MatchesRegistry(t *testing.T) {
	md, err := os.ReadFile(exitTableGuide)
	if err != nil {
		t.Fatal(err)
	}
	if problems := exitTableDrift(string(md), commands); len(problems) > 0 {
		t.Fatalf("exit-code table in docs/guides/cli.md differs from the command registry:\n  %s\nupdate the table or the `exits` declaration", strings.Join(problems, "\n  "))
	}
}

func TestExitTableDrift_MismatchFixture_ReportsEveryKind(t *testing.T) {
	md, err := os.ReadFile("testdata/exit-table-mismatch.md")
	if err != nil {
		t.Fatal(err)
	}
	cmds := map[string]command{
		"demo": {name: "demo", exits: []exitCode{{0, "success"}, {1, "check failed"}, {2, "usage error"}}},
	}
	got := strings.Join(exitTableDrift(string(md), cmds), "\n")
	for _, want := range []string{"missing row: demo 2", "different meaning for demo 1", "extra row: demo 9", "duplicate row: demo 0", "code is not an integer: demo \"x\"", "unparseable row: \"| demo | 7 | row without backticks |\""} {
		if !strings.Contains(got, want) {
			t.Errorf("drift output missing %q:\n%s", want, got)
		}
	}
	for _, bad := range []string{"missing row: demo 0", "different meaning for demo 0", "demo 5"} {
		if strings.Contains(got, bad) {
			t.Errorf("matching row demo 0 reported as %q:\n%s", bad, got)
		}
	}
}
