package capture

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const (
	envBlock     = "# Environment\nYou have been invoked in the following environment: \n - Primary working directory: %s\n - Is a git repository: true\n - Platform: darwin\n - Shell: zsh\n - OS Version: Darwin 25.6.0\n - You are powered by the model named Sonnet 5.5. The exact model ID is claude-sonnet-5-5.\n"
	dateReminder = "<system-reminder>\nToday's date is %s.\n\n%s\n</system-reminder>"
)

// messagesKey returns the replay key of an Anthropic /v1/messages request with
// the given system prompt and user turn content blocks.
func messagesKey(t *testing.T, system string, blocks ...map[string]any) string {
	t.Helper()
	content := make([]any, len(blocks))
	for i, b := range blocks {
		content[i] = b
	}
	raw, err := json.Marshal(map[string]any{
		"model":    "claude-sonnet-4-5",
		"system":   []any{map[string]any{"type": "text", "text": system}},
		"messages": []any{map[string]any{"role": "user", "content": content}},
	})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := decodeJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	k, err := keyOf("messages", doc)
	if err != nil {
		t.Fatal(err)
	}
	return k.PromptHash
}

func textBlock(s string) map[string]any { return map[string]any{"type": "text", "text": s} }

func TestKeyOf_ReminderSameDateDifferentInstruction_DifferentKeys(t *testing.T) {
	strict := messagesKey(t, "sys", textBlock(fmt.Sprintf(dateReminder, "2026-11-05", "Never refund more than $10")))
	loose := messagesKey(t, "sys", textBlock(fmt.Sprintf(dateReminder, "2026-11-05", "Refund up to $1000")))

	if strict == loose {
		t.Fatal("reminders with different instructions share a replay key")
	}
}

func TestKeyOf_ReminderOnlyDateDiffers_SameKey(t *testing.T) {
	a := messagesKey(t, "sys", textBlock(fmt.Sprintf(dateReminder, "2026-11-05", "Never refund more than $10")))
	b := messagesKey(t, "sys", textBlock(fmt.Sprintf(dateReminder, "2026-12-24", "Never refund more than $10")))

	if a != b {
		t.Fatal("a changed date changed the replay key")
	}
}

func TestKeyOf_SystemEnvironmentValuesDiffer_SameKey(t *testing.T) {
	other := "You are Claude Code.\n" + fmt.Sprintf(envBlock, "/private/tmp/run-b")
	other = replaceAll(other, "darwin", "linux", "Darwin 25.6.0", "Linux 6.1", "Sonnet 5.5", "Opus 5.5", "claude-sonnet-5-5", "claude-opus-5-5")

	a := messagesKey(t, "You are Claude Code.\n"+fmt.Sprintf(envBlock, "/private/tmp/run-a"), textBlock("go"))
	b := messagesKey(t, other, textBlock("go"))

	if a != b {
		t.Fatal("environment field values changed the replay key")
	}
}

func TestKeyOf_SystemInstructionAfterEnvironment_DifferentKeys(t *testing.T) {
	a := messagesKey(t, fmt.Sprintf(envBlock, "/x")+"Never refund more than $10", textBlock("go"))
	b := messagesKey(t, fmt.Sprintf(envBlock, "/x")+"Refund up to $1000", textBlock("go"))

	if a == b {
		t.Fatal("instruction text after the environment block was ignored")
	}
}

func TestKeyOf_GitStatusValuesDiffer_SameKey(t *testing.T) {
	status := "<system-reminder>\ngitStatus: snapshot.\nCurrent branch: %s\n\nStatus:\n%s\n\nRecent commits:\n%s\n</system-reminder>"
	a := messagesKey(t, "sys", textBlock(fmt.Sprintf(status, "main", "?? a.txt", "abc123 first")))
	b := messagesKey(t, "sys", textBlock(fmt.Sprintf(status, "feat/x", "M b.txt", "def456 second")))

	if a != b {
		t.Fatal("git status values changed the replay key")
	}
}

func TestKeyOf_ToolResultInsideEnvironmentShapedBlock_DifferentKeys(t *testing.T) {
	result := func(cwd string) map[string]any {
		return map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "content": "<system-reminder>\n" + fmt.Sprintf(envBlock, cwd) + "</system-reminder>"}
	}

	a := messagesKey(t, "sys", result("/data/a"))
	b := messagesKey(t, "sys", result("/data/b"))

	if a == b {
		t.Fatal("a tool_result that changed inside an environment-shaped block kept its replay key")
	}
}

func TestKeyOf_UserTextOutsideReminderLooksLikeDate_DifferentKeys(t *testing.T) {
	a := messagesKey(t, "sys", textBlock("Today's date is 2026-11-05."))
	b := messagesKey(t, "sys", textBlock("Today's date is 2026-12-24."))

	if a == b {
		t.Fatal("user text outside a reminder was normalized")
	}
}

func replaceAll(s string, pairs ...string) string {
	for i := 0; i+1 < len(pairs); i += 2 {
		s = strings.ReplaceAll(s, pairs[i], pairs[i+1])
	}
	return s
}
