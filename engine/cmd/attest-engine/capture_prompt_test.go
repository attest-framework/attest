package main

import "testing"

func TestMayPrompt_OnlyAtATerminalOutsideCIWithoutJSON(t *testing.T) {
	cases := []struct {
		name     string
		asJSON   bool
		ci       string
		stdinTTY bool
		want     bool
	}{
		{"terminal", false, "", true, true},
		{"no terminal", false, "", false, false},
		{"CI set at a terminal", false, "true", true, false},
		{"CI=1 at a terminal", false, "1", true, false},
		{"--json at a terminal", true, "", true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := mayPrompt(c.asJSON, c.ci, c.stdinTTY); got != c.want {
				t.Errorf("mayPrompt(%v, %q, %v) = %v, want %v", c.asJSON, c.ci, c.stdinTTY, got, c.want)
			}
		})
	}
}
