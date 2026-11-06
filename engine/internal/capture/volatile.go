package capture

import (
	"regexp"
	"strings"
)

// Claude Code describes the run to the model in the system prompt
// (# Environment) and in <system-reminder> blocks of the user turn: the
// working directory, platform, shell, OS, git status, the model name and
// today's date. Those values differ between a recording and its replay and do
// not change what the agent is asked to do. Matching neutralizes exactly those
// values, line by line, and keeps every other line, so instruction text in a
// reminder still changes the replay key. The stored request is never changed.

const volatileValue = "[volatile]"

type lineRule struct {
	pattern *regexp.Regexp
	repl    string
}

// envRules apply inside Claude Code's own environment sections: a
// "# Environment" section or an <env> block.
var envRules = []lineRule{
	{regexp.MustCompile(`^([ \t]*- (?:Primary working directory|Is a git repository|Platform|Shell|OS Version): ).*$`), "${1}" + volatileValue},
}

// modelRules apply inside an environment section and, wherever they stand,
// inside a <system-reminder> of a user turn, where Claude Code states the
// model on its own.
var modelRules = []lineRule{
	{regexp.MustCompile(`^([ \t]*(?:- )?You are powered by the model named ).*$`), "${1}" + volatileValue},
	{regexp.MustCompile(`^([ \t]*(?:- )?(?:The most recent Claude model|Assistant knowledge cutoff is)).*$`), "${1} " + volatileValue},
}

// gitRules apply inside a gitStatus: block.
var gitRules = []lineRule{
	{regexp.MustCompile(`^(Current branch: |Main branch[^:]*: |Git user: ).*$`), "${1}" + volatileValue},
}

var (
	dateRule        = lineRule{regexp.MustCompile(`^([ \t]*Today's date is ).*?\.?[ \t]*$`), "${1}" + volatileValue}
	additionalDirs  = regexp.MustCompile(`^([ \t]*)- Additional working directories:.*$`)
	listEntry       = regexp.MustCompile(`^([ \t]+)- `)
	totalTokensRule = lineRule{regexp.MustCompile(`^([ \t]*<total_tokens>).*$`), "${1}" + volatileValue}
	heading         = regexp.MustCompile(`^#{1,6} `)
)

type volatileScope int

const (
	scopeNone volatileScope = iota
	scopeEnv
	scopeDate
	scopeGit
)

var reminderBlock = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)

func applyRule(r lineRule, line string) string {
	return r.pattern.ReplaceAllString(line, r.repl)
}

func applyRules(rules []lineRule, line string) string {
	for _, r := range rules {
		line = applyRule(r, line)
	}
	return line
}

// enterScope returns the scope a line opens, and whether it opens one. A
// scope ends at the next heading or at the end of the <env> block or
// reminder; other lines never close it.
func enterScope(trimmed string, cur volatileScope) volatileScope {
	switch {
	case trimmed == "# Environment" || trimmed == "<env>":
		return scopeEnv
	case trimmed == "# currentDate":
		return scopeDate
	case strings.HasPrefix(trimmed, "gitStatus:"):
		return scopeGit
	case trimmed == "</env>" || trimmed == "</system-reminder>" || heading.MatchString(trimmed):
		return scopeNone
	}
	return cur
}

// normalizeVolatileLines neutralizes the volatile run values in s, but only
// inside the sections Claude Code writes them in: "# Environment" and <env>
// (working directories, platform, shell, OS, model, cutoff), "# currentDate"
// (today's date) and gitStatus: (branch, status, commits). A line of any other
// prompt that looks like one of them is the application's own and stays. In a
// <system-reminder> of a user turn (reminder), the date and model lines are
// Claude Code's own wherever they stand.
func normalizeVolatileLines(s string, reminder bool) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	scope := scopeNone
	listIndent := -1 // indent of an Additional working directories header whose entries are being dropped
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if listIndent >= 0 {
			if m := listEntry.FindStringSubmatch(line); m != nil && len(m[1]) > listIndent {
				continue
			}
			listIndent = -1
		}
		trimmed := strings.TrimSpace(line)
		scope = enterScope(trimmed, scope)
		line = applyRule(totalTokensRule, line)
		switch scope {
		case scopeEnv:
			if m := additionalDirs.FindStringSubmatch(line); m != nil {
				out = append(out, m[1]+"- Additional working directories: "+volatileValue)
				listIndent = len(m[1])
				continue
			}
			line = applyRules(envRules, line)
			line = applyRules(modelRules, line)
			line = applyRule(dateRule, line)
		case scopeDate:
			line = applyRule(dateRule, line)
		case scopeGit:
			line = applyRules(gitRules, line)
			if trimmed == "Status:" || trimmed == "Recent commits:" {
				out = append(out, line, volatileValue)
				for i+1 < len(lines) && lines[i+1] != "" && lines[i+1][0] != '<' {
					i++
				}
				continue
			}
		default:
			if reminder {
				line = applyRules(modelRules, line)
				line = applyRule(dateRule, line)
			}
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// normalizeUserText neutralizes the volatile values inside the
// <system-reminder> blocks of a user turn. Text outside a reminder is the
// user's own and stays.
func normalizeUserText(s string) string {
	return reminderBlock.ReplaceAllStringFunc(s, func(block string) string {
		return normalizeVolatileLines(block, true)
	})
}

// normalizeSystem neutralizes the volatile values in every string of a system
// prompt (a string, content blocks, or system messages).
func normalizeSystem(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = normalizeSystem(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = normalizeSystem(e)
		}
		return out
	case string:
		return normalizeVolatileLines(x, false)
	default:
		return v
	}
}

// normalizeConversation neutralizes the volatile values in the user-text
// content of a conversation: system and developer messages as a system prompt,
// user messages' text blocks inside reminders. Assistant turns, tool calls and
// tool results are never touched: a tool result that merely looks like an
// environment block is data the agent saw.
func normalizeConversation(v any) any {
	switch x := v.(type) {
	case string:
		return normalizeUserText(x)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = normalizeMessage(e)
		}
		return out
	default:
		return v
	}
}

func normalizeMessage(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	role, _ := m["role"].(string)
	switch role {
	case "system", "developer":
		return normalizeSystem(m)
	case "user":
		out := make(map[string]any, len(m))
		for k, e := range m {
			out[k] = e
		}
		out["content"] = normalizeContent(m["content"])
		return out
	default:
		return v
	}
}

func normalizeContent(v any) any {
	switch x := v.(type) {
	case string:
		return normalizeUserText(x)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = normalizeBlock(e)
		}
		return out
	default:
		return v
	}
}

// normalizeBlock normalizes a text content block; every other block type
// (tool_result, image, document) is returned as is.
func normalizeBlock(v any) any {
	b, ok := v.(map[string]any)
	if !ok {
		return v
	}
	if t, _ := b["type"].(string); t != "text" && t != "input_text" {
		return v
	}
	text, ok := b["text"].(string)
	if !ok {
		return v
	}
	out := make(map[string]any, len(b))
	for k, e := range b {
		out[k] = e
	}
	out["text"] = normalizeUserText(text)
	return out
}

// normalizePromptField normalizes one prompt field of a request body by its
// name: system prompts whole, conversations by role and block type.
func normalizePromptField(name string, v any) any {
	if name == "system" || name == "instructions" {
		return normalizeSystem(v)
	}
	return normalizeConversation(v)
}
