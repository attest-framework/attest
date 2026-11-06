package capture

import "regexp"

// Claude Code describes the run to the model in the system prompt
// (# Environment) and in <system-reminder> blocks of the user turn: the
// working directory, platform, shell, OS, git status, the model name and
// today's date. Those values differ between a recording and its replay and do
// not change what the agent is asked to do. Matching neutralizes exactly those
// values, line by line, and keeps every other line, so instruction text in a
// reminder still changes the replay key. The stored request is never changed.

const volatileValue = "[volatile]"

var volatileLines = []struct {
	pattern *regexp.Regexp
	repl    string
}{
	{regexp.MustCompile(`(?m)^([ \t]*Today's date is ).*?\.?[ \t]*$`), "${1}" + volatileValue},
	{regexp.MustCompile(`(?m)^([ \t]*- (?:Primary working directory|Additional working directories|Is a git repository|Platform|Shell|OS Version): ).*$`), "${1}" + volatileValue},
	{regexp.MustCompile(`(?m)^([ \t]*(?:- )?You are powered by the model named ).*$`), "${1}" + volatileValue},
	{regexp.MustCompile(`(?m)^([ \t]*(?:- )?(?:The most recent Claude model|Assistant knowledge cutoff is)).*$`), "${1} " + volatileValue},
	{regexp.MustCompile(`(?m)^(Current branch: |Main branch[^\n:]*: |Git user: ).*$`), "${1}" + volatileValue},
	{regexp.MustCompile(`(?m)^(Status:|Recent commits:)\n(?:[^<\n][^\n]*(?:\n|$))*`), "${1}\n" + volatileValue + "\n"},
	{regexp.MustCompile(`(?m)^([ \t]*<total_tokens>).*$`), "${1}" + volatileValue},
}

var reminderBlock = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)

// normalizeVolatileLines neutralizes the volatile run values in s.
func normalizeVolatileLines(s string) string {
	for _, l := range volatileLines {
		s = l.pattern.ReplaceAllString(s, l.repl)
	}
	return s
}

// normalizeUserText neutralizes the volatile values inside the
// <system-reminder> blocks of a user turn. Text outside a reminder is the
// user's own and stays.
func normalizeUserText(s string) string {
	return reminderBlock.ReplaceAllStringFunc(s, normalizeVolatileLines)
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
		return normalizeVolatileLines(x)
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
