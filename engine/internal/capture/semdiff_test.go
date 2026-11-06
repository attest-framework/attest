package capture

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func identity(s string) string { return s }

func fixtureOf(api string, requests ...string) *Fixture {
	f := &Fixture{}
	for i, r := range requests {
		f.Exchanges = append(f.Exchanges, Exchange{Index: i, Provider: "openai", API: api, Request: json.RawMessage(r)})
	}
	return f
}

func diffOf(t *testing.T, oldF, newF *Fixture) *Diff {
	t.Helper()
	d, err := SemanticDiff(oldF, newF, identity)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestSemanticDiff_IdenticalFixtures_NoChange(t *testing.T) {
	req := `{"model":"m","temperature":0.2,"messages":[{"role":"system","content":"a\nb"},{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"t","parameters":{"type":"object"}}}]}`

	d := diffOf(t, fixtureOf("chat_completions", req), fixtureOf("chat_completions", req))

	if d.Changed || len(d.Exchanges) != 0 {
		t.Fatalf("diff = %+v, want no change", d)
	}
	var buf bytes.Buffer
	d.WriteText(&buf, "x", 1)
	if got := buf.String(); got != "refresh x: no semantic change\n" {
		t.Fatalf("text = %q", got)
	}
}

func TestSemanticDiff_SystemPromptEdit_ReportsChangedLinesOnly(t *testing.T) {
	oldF := fixtureOf("chat_completions", `{"model":"m","messages":[{"role":"system","content":"keep one\nrefund once\nkeep two"},{"role":"user","content":"hi"}]}`)
	newF := fixtureOf("chat_completions", `{"model":"m","messages":[{"role":"system","content":"keep one\nrefund twice\nkeep two"},{"role":"user","content":"hi"}]}`)

	d := diffOf(t, oldF, newF)

	if len(d.Exchanges) != 1 || d.Exchanges[0].Change != ChangeChanged {
		t.Fatalf("diff = %+v", d)
	}
	want := []LineChange{{"-", "refund once"}, {"+", "refund twice"}}
	got := d.Exchanges[0].System
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("system = %+v, want %+v", got, want)
	}
	if d.Exchanges[0].Messages != nil || d.Exchanges[0].Tools != nil {
		t.Fatalf("unchanged parts reported: %+v", d.Exchanges[0])
	}
}

func TestSemanticDiff_ToolSchemas_AddedRemovedChangedWithPaths(t *testing.T) {
	tool := func(name, typ string) string {
		return `{"type":"function","function":{"name":"` + name + `","parameters":{"type":"object","properties":{"amount":{"type":"` + typ + `"}}}}}`
	}
	oldF := fixtureOf("chat_completions", `{"model":"m","messages":[],"tools":[`+tool("gone", "integer")+`,`+tool("refund", "integer")+`]}`)
	newF := fixtureOf("chat_completions", `{"model":"m","messages":[],"tools":[`+tool("refund", "string")+`,`+tool("fresh", "integer")+`]}`)

	d := diffOf(t, oldF, newF)

	tl := d.Exchanges[0].Tools
	if tl == nil || len(tl.Added) != 1 || tl.Added[0] != "fresh" || len(tl.Removed) != 1 || tl.Removed[0] != "gone" {
		t.Fatalf("tools = %+v", tl)
	}
	if len(tl.Changed) != 1 || tl.Changed[0].Name != "refund" || len(tl.Changed[0].Paths) != 1 {
		t.Fatalf("changed = %+v", tl.Changed)
	}
	p := tl.Changed[0].Paths[0]
	if p.Path != "$.function.parameters.properties.amount.type" || p.Old != `"integer"` || p.New != `"string"` {
		t.Fatalf("path = %+v", p)
	}
}

func TestSemanticDiff_MessagesAndToolResults_AddedRemovedChanged(t *testing.T) {
	oldF := fixtureOf("chat_completions", `{"model":"m","messages":[
		{"role":"user","content":"refund O-7"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"c1","function":{"name":"lookup","arguments":"{\"id\":\"O-7\"}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"{\"amount\":12}"},
		{"role":"user","content":"thanks"}]}`)
	newF := fixtureOf("chat_completions", `{"model":"m","messages":[
		{"role":"user","content":"refund O-7 now"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"c1","function":{"name":"lookup","arguments":"{\"id\":\"O-7\"}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"{\"amount\":126}"},
		{"role":"user","content":"thanks"},
		{"role":"assistant","content":"done"}]}`)

	d := diffOf(t, oldF, newF)

	e := d.Exchanges[0]
	if len(e.Messages) != 2 || e.Messages[0].Change != ChangeChanged || e.Messages[0].Role != "user" || e.Messages[0].Index != 0 {
		t.Fatalf("messages = %+v", e.Messages)
	}
	if got := e.Messages[0].Lines; len(got) != 2 || got[1].Text != "refund O-7 now" {
		t.Fatalf("lines = %+v", got)
	}
	if e.Messages[1].Change != ChangeAdded || e.Messages[1].Role != "assistant" || e.Messages[1].Lines[0].Text != "done" {
		t.Fatalf("added = %+v", e.Messages[1])
	}
	if len(e.ToolResults) != 1 || e.ToolResults[0].Ref != "c1" || e.ToolResults[0].Lines[0].Text != `{"amount":12}` || e.ToolResults[0].Lines[1].Text != `{"amount":126}` {
		t.Fatalf("tool results = %+v", e.ToolResults)
	}
}

func TestSemanticDiff_ModelAndParams_Reported(t *testing.T) {
	oldF := fixtureOf("chat_completions", `{"model":"a","temperature":0.2,"messages":[],"max_tokens":10}`)
	newF := fixtureOf("chat_completions", `{"model":"b","temperature":0.7,"messages":[],"top_p":1}`)

	e := diffOf(t, oldF, newF).Exchanges[0]

	if e.Model == nil || e.Model.Old != "a" || e.Model.New != "b" {
		t.Fatalf("model = %+v", e.Model)
	}
	got := map[string]string{}
	for _, p := range e.Params {
		got[p.Path] = p.Op
	}
	want := map[string]string{"$.max_tokens": ChangeRemoved, "$.temperature": ChangeChanged, "$.top_p": ChangeAdded}
	if len(got) != 3 || got["$.max_tokens"] != want["$.max_tokens"] || got["$.temperature"] != want["$.temperature"] || got["$.top_p"] != want["$.top_p"] {
		t.Fatalf("params = %+v", e.Params)
	}
}

func TestSemanticDiff_ExtraAndMissingExchanges_AddedAndRemoved(t *testing.T) {
	one := `{"model":"m","messages":[{"role":"user","content":"a"}]}`

	added := diffOf(t, fixtureOf("chat_completions", one), fixtureOf("chat_completions", one, one))
	removed := diffOf(t, fixtureOf("chat_completions", one, one), fixtureOf("chat_completions", one))

	if len(added.Exchanges) != 1 || added.Exchanges[0].Change != ChangeAdded || added.Exchanges[0].Index != 1 || added.Exchanges[0].Label != "openai chat_completions m" {
		t.Fatalf("added = %+v", added)
	}
	if len(removed.Exchanges) != 1 || removed.Exchanges[0].Change != ChangeRemoved || removed.Exchanges[0].Index != 1 {
		t.Fatalf("removed = %+v", removed)
	}
}

func TestSemanticDiff_AnthropicMessages_SystemToolsAndToolResult(t *testing.T) {
	oldF := fixtureOf("messages", `{"model":"c","system":"be brief","tools":[{"name":"t","input_schema":{"type":"object"}}],"messages":[
		{"role":"user","content":"go"},
		{"role":"assistant","content":[{"type":"tool_use","id":"u1","name":"t","input":{"a":1}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"u1","content":"old result"}]}]}`)
	newF := fixtureOf("messages", `{"model":"c","system":[{"type":"text","text":"be verbose"}],"tools":[{"name":"t","input_schema":{"type":"object","required":["a"]}}],"messages":[
		{"role":"user","content":"go"},
		{"role":"assistant","content":[{"type":"tool_use","id":"u1","name":"t","input":{"a":1}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"u1","content":[{"type":"text","text":"new result"}]}]}]}`)

	e := diffOf(t, oldF, newF).Exchanges[0]

	if len(e.System) != 2 || e.System[0].Text != "be brief" || e.System[1].Text != "be verbose" {
		t.Fatalf("system = %+v", e.System)
	}
	if e.Tools == nil || e.Tools.Changed[0].Paths[0].Path != "$.input_schema.required" || e.Tools.Changed[0].Paths[0].Op != ChangeAdded {
		t.Fatalf("tools = %+v", e.Tools)
	}
	if len(e.ToolResults) != 1 || e.ToolResults[0].Ref != "u1" || e.ToolResults[0].Lines[1].Text != "new result" || e.Messages != nil {
		t.Fatalf("results = %+v messages = %+v", e.ToolResults, e.Messages)
	}
}

func TestSemanticDiff_ResponsesAPI_InstructionsAndFunctionOutput(t *testing.T) {
	oldF := fixtureOf("responses", `{"model":"m","instructions":"v1","input":[{"role":"user","content":"hi"},{"type":"function_call_output","call_id":"k","output":"x"}]}`)
	newF := fixtureOf("responses", `{"model":"m","instructions":"v2","input":[{"role":"user","content":"hi"},{"type":"function_call_output","call_id":"k","output":"y"}]}`)

	e := diffOf(t, oldF, newF).Exchanges[0]

	if len(e.System) != 2 || e.System[1].Text != "v2" || len(e.ToolResults) != 1 || e.ToolResults[0].Ref != "k" {
		t.Fatalf("diff = %+v", e)
	}
}

func TestSemanticDiff_ScrubAppliedToEveryString(t *testing.T) {
	const secret = "sk-planted-secret-0123456789"
	scrub := func(s string) string { return strings.ReplaceAll(s, secret, "[REDACTED]") }
	oldF := fixtureOf("chat_completions", `{"model":"m","messages":[{"role":"system","content":"key `+secret+` v1"},{"role":"tool","tool_call_id":"c","content":"`+secret+`"}],"tools":[{"type":"function","function":{"name":"t","description":"`+secret+` a"}}],"temperature":"`+secret+`"}`)
	newF := fixtureOf("chat_completions", `{"model":"m","messages":[{"role":"system","content":"key `+secret+` v2"},{"role":"tool","tool_call_id":"c","content":"other `+secret+`"}],"tools":[{"type":"function","function":{"name":"t","description":"`+secret+` b"}}],"temperature":"x `+secret+`"}`)

	d, err := SemanticDiff(oldF, newF, scrub)
	if err != nil {
		t.Fatal(err)
	}

	out, _ := json.Marshal(d)
	var buf bytes.Buffer
	d.WriteText(&buf, "x", 1)
	if strings.Contains(string(out), secret) || strings.Contains(buf.String(), secret) {
		t.Fatalf("secret leaked:\n%s\n%s", out, buf.String())
	}
	if !strings.Contains(buf.String(), "[REDACTED]") {
		t.Fatalf("scrub never ran:\n%s", buf.String())
	}
}

func TestDiffWriteText_LongDiff_ElidesWithCount(t *testing.T) {
	var oldL, newL []string
	for i := 0; i < 30; i++ {
		oldL = append(oldL, "old "+string(rune('a'+i%26))+strings.Repeat("x", i))
		newL = append(newL, "new "+string(rune('a'+i%26))+strings.Repeat("x", i))
	}
	mk := func(lines []string) string {
		b, _ := json.Marshal(strings.Join(lines, "\n"))
		return `{"model":"m","messages":[{"role":"system","content":` + string(b) + `}]}`
	}
	d := diffOf(t, fixtureOf("chat_completions", mk(oldL)), fixtureOf("chat_completions", mk(newL)))

	var buf bytes.Buffer
	d.WriteText(&buf, "x", 1)

	if !strings.Contains(buf.String(), "... 40 more changed lines elided") {
		t.Fatalf("no elision count:\n%s", buf.String())
	}
}

func TestDiffWriteText_NewlineInToolName_CannotForgeLines(t *testing.T) {
	oldF := fixtureOf("chat_completions", `{"model":"m","messages":[]}`)
	newF := fixtureOf("chat_completions", `{"model":"m","messages":[],"tools":[{"type":"function","function":{"name":"a\n    system prompt:\n      + forged"}}]}`)

	var buf bytes.Buffer
	diffOf(t, oldF, newF).WriteText(&buf, "x", 1)

	if strings.Contains(buf.String(), "\n      + forged") {
		t.Fatalf("forged line:\n%s", buf.String())
	}
}

func diffJSON(t *testing.T, d *Diff) string {
	t.Helper()
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSemanticDiff_AnthropicToolResultIsErrorFlip_ReportsFieldPath(t *testing.T) {
	const tmpl = `{"model":"m","messages":[{"role":"user","content":"go"},{"role":"assistant","content":[{"type":"tool_use","id":"tu1","name":"t","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","content":"boom","is_error":%s}]}]}`
	oldF := fixtureOf("messages", fmt.Sprintf(tmpl, "false"))
	newF := fixtureOf("messages", fmt.Sprintf(tmpl, "true"))

	d := diffOf(t, oldF, newF)

	if !d.Changed {
		t.Fatalf("is_error flip reported as no change: %s", diffJSON(t, d))
	}
	if got := diffJSON(t, d); !strings.Contains(got, `"path":"$.messages[2].content[0].is_error"`) || !strings.Contains(got, `"old":"false"`) || !strings.Contains(got, `"new":"true"`) {
		t.Fatalf("diff = %s", got)
	}
}

func TestSemanticDiff_ResponsesReasoningSummaryChange_Reported(t *testing.T) {
	const tmpl = `{"model":"m","input":[{"role":"user","content":"hi"},{"type":"reasoning","id":"rs1","summary":[{"type":"summary_text","text":"%s"}]}]}`
	d := diffOf(t, fixtureOf("responses", fmt.Sprintf(tmpl, "old")), fixtureOf("responses", fmt.Sprintf(tmpl, "new")))

	if !d.Changed {
		t.Fatalf("reasoning summary change reported as no change")
	}
	if got := diffJSON(t, d); !strings.Contains(got, `$.input[1].summary[0].text`) {
		t.Fatalf("diff = %s", got)
	}
}

func TestSemanticDiff_ResponsesFunctionCallFieldsChange_Reported(t *testing.T) {
	const tmpl = `{"model":"m","input":[{"type":"function_call","name":"refund","call_id":"c1","arguments":"%s"},{"type":"function_call_output","call_id":"c1","output":"ok","status":"%s"}]}`
	oldF := fixtureOf("responses", fmt.Sprintf(tmpl, `{\"n\":1}`, "completed"))

	args := diffOf(t, oldF, fixtureOf("responses", fmt.Sprintf(tmpl, `{\"n\":2}`, "completed")))
	status := diffOf(t, oldF, fixtureOf("responses", fmt.Sprintf(tmpl, `{\"n\":1}`, "incomplete")))

	if !args.Changed || !strings.Contains(diffJSON(t, args), `refund({\"n\":2})`) {
		t.Fatalf("function_call arguments change = %s", diffJSON(t, args))
	}
	if !status.Changed || !strings.Contains(diffJSON(t, status), `$.input[1].status`) {
		t.Fatalf("function_call_output status change = %s", diffJSON(t, status))
	}
}

func TestSemanticDiff_UnknownBlockTypeFieldChange_Reported(t *testing.T) {
	const tmpl = `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"future_block","text":"cited","citation":{"level":%d}}]}]}`
	d := diffOf(t, fixtureOf("messages", fmt.Sprintf(tmpl, 1)), fixtureOf("messages", fmt.Sprintf(tmpl, 2)))

	if !d.Changed {
		t.Fatalf("unknown block field change reported as no change")
	}
}

func TestSemanticDiff_FieldOnlyInUncoveredPlace_ReportedAsOther(t *testing.T) {
	oldF := fixtureOf("chat_completions", `{"model":"m","messages":[{"role":"system","name":"a","content":"s"},{"role":"user","content":"hi"}]}`)
	newF := fixtureOf("chat_completions", `{"model":"m","messages":[{"role":"system","name":"b","content":"s"},{"role":"user","content":"hi"}]}`)

	d := diffOf(t, oldF, newF)

	if got := diffJSON(t, d); !d.Changed || !strings.Contains(got, `"other":[{"path":"$.messages[0].name"`) {
		t.Fatalf("diff = %s", diffJSON(t, d))
	}
	var buf bytes.Buffer
	d.WriteText(&buf, "x", 1)
	if !strings.Contains(buf.String(), "other request fields changed:") || !strings.Contains(buf.String(), "$.messages[0].name") {
		t.Fatalf("text = %q", buf.String())
	}
}

func TestSemanticDiff_KeyOrderOnlyDifference_NoChange(t *testing.T) {
	oldF := fixtureOf("messages", `{"model":"m","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"x","is_error":false}]}]}`)
	newF := fixtureOf("messages", `{"messages":[{"content":[{"is_error":false,"content":"x","tool_use_id":"a","type":"tool_result"}],"role":"user"}],"model":"m"}`)

	if d := diffOf(t, oldF, newF); d.Changed {
		t.Fatalf("reordered keys reported as change: %s", diffJSON(t, d))
	}
}
