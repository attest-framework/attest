package capture

import (
	"encoding/json"
	"strings"

	"github.com/attest-ai/attest/engine/internal/llm"
)

// liveCost prices the exchanges a replay session forwarded live. A call is
// unpriced when its model has no built-in price or its response carries no
// token usage.
func (p *Proxy) liveCost() (usd float64, unpriced int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, ex := range p.log {
		if !ex.done {
			unpriced++
			continue
		}
		in, out, ok := tokenUsage(ex.response)
		var req struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(ex.request, &req)
		price, priced := llm.LookupPrice(req.Model)
		if !ok || !priced {
			unpriced++
			continue
		}
		usd += price.Cost(in, out)
	}
	return usd, unpriced
}

// tokenUsage reads input and output token counts from a JSON response or the
// events of a stream: the largest count seen for each, which is the final
// total for OpenAI usage chunks and Anthropic's message_start/message_delta.
func tokenUsage(body []byte) (in, out int, ok bool) {
	docs := [][]byte{body}
	if text := string(body); strings.HasPrefix(strings.TrimSpace(text), "event:") || strings.HasPrefix(strings.TrimSpace(text), "data:") {
		docs = docs[:0]
		for _, line := range strings.Split(text, "\n") {
			if data, found := strings.CutPrefix(line, "data:"); found {
				docs = append(docs, []byte(strings.TrimSpace(data)))
			}
		}
	}
	for _, d := range docs {
		var v struct {
			Usage    *usageDoc                  `json:"usage"`
			Message  *struct{ Usage *usageDoc } `json:"message"`
			Response *struct{ Usage *usageDoc } `json:"response"`
		}
		if json.Unmarshal(d, &v) != nil {
			continue
		}
		for _, u := range []*usageDoc{v.Usage, usageOf(v.Message), usageOf(v.Response)} {
			if u == nil {
				continue
			}
			ok = true
			in, out = max(in, u.in()), max(out, u.out())
		}
	}
	return in, out, ok
}

type usageDoc struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
}

func (u *usageDoc) in() int  { return max(u.PromptTokens, u.InputTokens) }
func (u *usageDoc) out() int { return max(u.CompletionTokens, u.OutputTokens) }

func usageOf(m *struct{ Usage *usageDoc }) *usageDoc {
	if m == nil {
		return nil
	}
	return m.Usage
}
