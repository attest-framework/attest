package main_test

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestPolicyEvaluate_MinMutationScore(t *testing.T) {
	bin := buildEngineBinary(t)
	tmp := t.TempDir()
	report := cliReportV2(t, tmp)
	mutation := func(name, score string) string {
		p := filepath.Join(tmp, name+".json")
		cliWriteFile(t, p, `{"score": `+score+`}`)
		return p
	}
	policy := func(name, body string) string {
		p := filepath.Join(tmp, name+".yaml")
		cliWriteFile(t, p, body)
		return p
	}
	block := policy("block", "min_mutation_score:\n  score: 0.8\n")
	warn := policy("warn", "min_mutation_score:\n  score: 0.8\n  severity: warn\n")

	cases := []struct {
		name     string
		policy   string
		mutation string
		wantExit int
		wantErr  string
	}{
		{"below threshold blocks", block, mutation("low", "0.5"), 1, ""},
		{"below threshold warns", warn, mutation("low", "0.5"), 2, ""},
		{"at threshold passes", block, mutation("at", "0.8"), 0, ""},
		{"above threshold passes", block, mutation("high", "0.95"), 0, ""},
		{"null score blocks", block, mutation("null", "null"), 1, ""},
		{"rule without --mutation is a load error", block, "", 3, "min_mutation_score"},
		{"--mutation without the rule has no effect", policy("none", "layers:\n  3:\n    max_hard_fails: 0\n"), mutation("low", "0.1"), 0, ""},
		{"threshold above one is a load error", policy("bad", "min_mutation_score:\n  score: 1.5\n"), mutation("low", "0.5"), 3, "between 0 and 1"},
		{"threshold missing is a load error", policy("noscore", "min_mutation_score:\n  severity: warn\n"), mutation("low", "0.5"), 3, "score is required"},
		{"not a mutate report is a load error", block, policy("junk", "{}"), 3, "no \"score\""},
		{"missing mutation file is a load error", block, filepath.Join(tmp, "nope.json"), 3, "read mutation report"},
	}
	for _, c := range cases {
		args := []string{"policy", "evaluate", "--json", "--policy", c.policy, "--report", report}
		if c.mutation != "" {
			args = append(args, "--mutation", c.mutation)
		}
		stdout, stderr, code := runEngine(t, bin, t.TempDir(), args...)
		if code != c.wantExit {
			t.Errorf("%s: exit %d, want %d\nstdout: %s\nstderr: %s", c.name, code, c.wantExit, stdout, stderr)
			continue
		}
		if c.wantErr != "" && !strings.Contains(stderr, c.wantErr) {
			t.Errorf("%s: stderr %q lacks %q", c.name, stderr, c.wantErr)
		}
		if c.wantExit == 3 && stdout != "" {
			t.Errorf("%s: stdout %q on a load error", c.name, stdout)
		}
		if c.wantExit == 1 && c.name == "below threshold blocks" && !strings.Contains(stdout, `"rule": "min_mutation_score"`) {
			t.Errorf("%s: stdout lacks the violation:\n%s", c.name, stdout)
		}
	}
}
