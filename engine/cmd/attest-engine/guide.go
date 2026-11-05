package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/attest-framework/attest/engine/internal/assets"
)

func init() {
	register(command{
		name:    "guide",
		summary: "list the embedded guides, or print one: [<topic>] [--json]",
		run:     func(args []string) { os.Exit(runGuide(args)) },
		schema:  "cli/guide.schema.json",
		exits: []exitCode{
			{Code: 0, Meaning: "guide list or guide printed"},
			{Code: 1, Meaning: "an embedded guide is unreadable or has no title"},
			{Code: 2, Meaning: "unknown topic or usage error"},
		},
	})
}

const guideUsage = "usage: attest-engine guide [<topic>] [--json]"

// runGuide lists the guides or prints one guide's Markdown byte-exactly. It
// reads only embedded files, so it never touches the network.
func runGuide(args []string) int {
	asJSON := false
	var topics []string
	for _, a := range args {
		switch {
		case a == "--json" || a == "-json":
			asJSON = true
		case strings.HasPrefix(a, "-"):
			fmt.Fprintf(os.Stderr, "guide: unknown flag %s\n%s\n", a, guideUsage)
			return 2
		default:
			topics = append(topics, a)
		}
	}
	if len(topics) > 1 {
		fmt.Fprintf(os.Stderr, "guide: one topic at a time\n%s\n", guideUsage)
		return 2
	}
	list, err := assets.Topics()
	if err != nil {
		fmt.Fprintf(os.Stderr, "guide: %v\n", err)
		return 1
	}
	if len(topics) == 0 {
		return printTopics(list, asJSON)
	}
	md, topic, err := assets.Guide(topics[0])
	if errors.Is(err, fs.ErrNotExist) {
		names := make([]string, len(list))
		for i, t := range list {
			names[i] = t.Name
		}
		fmt.Fprintf(os.Stderr, "guide: unknown topic %q; topics: %s\n", topics[0], strings.Join(names, ", "))
		return 2
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "guide: %v\n", err)
		return 1
	}
	if asJSON {
		emitJSON(map[string]string{"topic": topic.Name, "title": topic.Title, "markdown": string(md)})
		return 0
	}
	if _, err := os.Stdout.Write(md); err != nil {
		fmt.Fprintf(os.Stderr, "guide: %v\n", err)
		return 1
	}
	return 0
}

// printTopics writes one `name  title` line per guide, or {"topics": [...]}.
func printTopics(list []assets.Topic, asJSON bool) int {
	if asJSON {
		emitJSON(map[string][]assets.Topic{"topics": list})
		return 0
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, t := range list {
		fmt.Fprintf(tw, "%s\t%s\n", t.Name, t.Title)
	}
	if err := tw.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "guide: %v\n", err)
		return 1
	}
	return 0
}
