package capture

import (
	"fmt"
	"strings"

	"github.com/attest-ai/attest/engine/internal/redact"
)

// A streamed response carries its text in many small deltas, so a secret the
// model streams arrives split across events and no single string holds it.
// Stream redaction rebuilds each delta stream's text, finds secrets in it, and
// rewrites the deltas they span: the redaction marker goes in the first
// affected delta and the rest lose their part. Events stay valid JSON, so the
// stream still replays.

// sseEvent is one server-sent event.
type sseEvent struct {
	lines   []string       // the event's lines, without line endings
	obj     map[string]any // decoded data payload; nil for [DONE] or no data
	changed bool
}

// sseStream is a parsed event stream.
type sseStream struct {
	events []*sseEvent
	eol    string
}

// parseSSE splits an event stream into events and decodes each data payload,
// which must be a JSON object or [DONE].
func parseSSE(body string) (*sseStream, error) {
	s := &sseStream{eol: "\n"}
	if strings.Contains(body, "\r\n") {
		s.eol = "\r\n"
	}
	for i, block := range strings.Split(body, s.eol+s.eol) {
		if strings.TrimSpace(block) == "" {
			continue
		}
		ev := &sseEvent{lines: strings.Split(block, s.eol)}
		var data []string
		for _, line := range ev.lines {
			if v, ok := strings.CutPrefix(line, "data:"); ok {
				data = append(data, strings.TrimPrefix(v, " "))
			}
		}
		payload := strings.Join(data, "\n")
		if len(data) > 0 && payload != "[DONE]" {
			v, err := decodeJSON([]byte(payload))
			if err != nil {
				return nil, fmt.Errorf("event %d: data is not JSON: %w", i, err)
			}
			obj, ok := v.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("event %d: data is not a JSON object", i)
			}
			ev.obj = obj
		}
		s.events = append(s.events, ev)
	}
	return s, nil
}

// String re-encodes the stream; changed events get one canonical data line.
func (s *sseStream) String() (string, error) {
	blocks := make([]string, 0, len(s.events))
	for _, ev := range s.events {
		if !ev.changed {
			blocks = append(blocks, strings.Join(ev.lines, s.eol))
			continue
		}
		data, err := canonical(ev.obj)
		if err != nil {
			return "", err
		}
		var lines []string
		wrote := false
		for _, line := range ev.lines {
			if !strings.HasPrefix(line, "data:") {
				lines = append(lines, line)
				continue
			}
			if !wrote {
				lines = append(lines, "data: "+string(data))
				wrote = true
			}
		}
		blocks = append(blocks, strings.Join(lines, s.eol))
	}
	return strings.Join(blocks, s.eol+s.eol) + s.eol + s.eol, nil
}

// deltaPiece is one delta string inside an event: holder[key].
type deltaPiece struct {
	ev     *sseEvent
	holder map[string]any
	key    string
}

func (p deltaPiece) text() string { return p.holder[p.key].(string) }

// deltaStreams groups the delta strings of a stream by the text they build,
// in order: Chat Completions content, refusal and tool-call arguments per
// choice; Responses *.delta events per item and part; Anthropic
// content_block_delta text, partial_json and thinking per block.
func (s *sseStream) deltaStreams() ([]string, map[string][]deltaPiece) {
	var order []string
	streams := map[string][]deltaPiece{}
	add := func(id string, ev *sseEvent, holder map[string]any, key string) {
		if _, ok := holder[key].(string); !ok {
			return
		}
		if _, seen := streams[id]; !seen {
			order = append(order, id)
		}
		streams[id] = append(streams[id], deltaPiece{ev: ev, holder: holder, key: key})
	}
	for _, ev := range s.events {
		o := ev.obj
		if o == nil {
			continue
		}
		for _, c := range asSlice(o["choices"]) {
			choice := asMap(c)
			delta := asMap(choice["delta"])
			if delta == nil {
				continue
			}
			ci := fmt.Sprint(choice["index"])
			add("choice "+ci+" content", ev, delta, "content")
			add("choice "+ci+" refusal", ev, delta, "refusal")
			for _, tc := range asSlice(delta["tool_calls"]) {
				call := asMap(tc)
				add(fmt.Sprintf("choice %s tool %v arguments", ci, call["index"]), ev, asMap(call["function"]), "arguments")
			}
		}
		typ, _ := o["type"].(string)
		switch {
		case typ == "content_block_delta":
			delta := asMap(o["delta"])
			for _, k := range []string{"text", "partial_json", "thinking"} {
				add(fmt.Sprintf("block %v %s", o["index"], k), ev, delta, k)
			}
		case strings.HasSuffix(typ, ".delta"):
			add(fmt.Sprintf("%s item %v output %v content %v", typ, o["item_id"], o["output_index"], o["content_index"]), ev, o, "delta")
		}
	}
	return order, streams
}

// streamTexts returns each delta stream's rebuilt text, by stream id.
func streamTexts(body string) (map[string]string, error) {
	s, err := parseSSE(body)
	if err != nil {
		return nil, err
	}
	order, streams := s.deltaStreams()
	out := make(map[string]string, len(order))
	for _, id := range order {
		var b strings.Builder
		for _, p := range streams[id] {
			b.WriteString(p.text())
		}
		out[id] = b.String()
	}
	return out, nil
}

// redactSSE redacts secrets that span deltas and returns the rewritten
// stream. A stream that does not parse is an error: it is never written
// unredacted.
func redactSSE(body string, r *redact.Redactor) (string, error) {
	s, err := parseSSE(body)
	if err != nil {
		return "", err
	}
	order, streams := s.deltaStreams()
	for _, id := range order {
		redactPieces(streams[id], r)
	}
	return s.String()
}

// redactPieces finds secrets in the concatenated text of pieces and rewrites
// each piece the secrets touch.
func redactPieces(pieces []deltaPiece, r *redact.Redactor) {
	var b strings.Builder
	starts := make([]int, len(pieces))
	for i, p := range pieces {
		starts[i] = b.Len()
		b.WriteString(p.text())
	}
	text := b.String()
	ranges := r.Ranges(text)
	if len(ranges) == 0 {
		return
	}
	for i, p := range pieces {
		ps, pe := starts[i], starts[i]+len(p.text())
		var out strings.Builder
		pos, touched := ps, false
		for _, x := range ranges {
			if x.End <= ps || x.Start >= pe {
				continue
			}
			touched = true
			out.WriteString(text[pos:max(x.Start, ps)])
			if x.Start >= ps {
				out.WriteString(redact.Marker)
			}
			pos = min(x.End, pe)
		}
		if !touched {
			continue
		}
		out.WriteString(text[pos:pe])
		p.holder[p.key] = out.String()
		p.ev.changed = true
	}
}
