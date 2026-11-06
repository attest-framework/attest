package capture

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/attest-framework/attest/engine/internal/assertion/judge"
	"github.com/attest-framework/attest/engine/internal/redact"
)

// Replay matching. Fixtures are templated with document-wide positional
// numbering, so a live request templated on its own does not hash equal to
// its recording. Both sides are therefore normalized per request: secrets
// are redacted, then every volatile value (raw ids, timestamps and the
// {{kind_N}} placeholders a replayed response hands back to the agent) is
// renumbered by first occurrence within that request. Email identities are
// the exception: they are numbered across the whole session (identityBinder),
// so a request whose identities swap roles misses. The match key is computed
// from the normalized request; the stored prompt_hash stays informational.

// storedPlaceholder matches a {{kind_N}} placeholder written by Template.
var storedPlaceholder = regexp.MustCompile(`\{\{(id|email|timestamp)_(\d+)\}\}`)

// HasPlaceholder reports whether s contains a templated {{kind_N}} value.
func HasPlaceholder(s string) bool { return storedPlaceholder.MatchString(s) }

// emailIdentity matches an email address, raw or as a stored {{email_N}}
// placeholder (group 1 holds the placeholder's number).
var emailIdentity = regexp.MustCompile(`\{\{email_(\d+)\}\}|` + emailPattern.String())

// unknownLabelBase offsets the label of a live placeholder the recording never
// sent in a request, so it cannot collide with a recorded identity.
const unknownLabelBase = 1000

// identityBinder numbers email identities across every request of a replay
// session in first-seen order, so a request matches only when its identities
// play the same roles as in the recording: the customer looked up and the one
// notified are the same identity or they are not. Raw addresses never reach a
// match key; each becomes a label. A run that uses one different address
// throughout still binds the same labels and replays.
//
// The recorded side numbers the fixture's {{email_N}} placeholders in the
// order its requests first mention them. The live side numbers raw addresses
// in the order the run first sends them, and maps an echoed {{email_N}} to
// the label the recording gave it. A raw address never takes a label already
// held by another address or by an echoed placeholder. Absolute identity (the
// run's address equals the recorded one) is not part of the match.
type identityBinder struct {
	recorded bool
	known    map[string]int // recorded placeholder number -> label; read-only on the live side
	raw      map[string]int
	taken    map[int]bool
	next     int
}

// newRecordedBinder numbers the fixture's placeholders and any raw address it
// still holds, in request order.
func newRecordedBinder() *identityBinder {
	return &identityBinder{recorded: true, known: map[string]int{}, raw: map[string]int{}, taken: map[int]bool{}}
}

// liveBinder starts the numbering of one replay session over the recorded
// placeholder labels.
func (b *identityBinder) liveBinder() *identityBinder {
	return &identityBinder{known: b.known, raw: map[string]int{}, taken: map[int]bool{}}
}

// label returns the identity label of one email match.
func (b *identityBinder) label(match string) int {
	if sub := emailIdentity.FindStringSubmatch(match); sub[1] != "" {
		return b.placeholderLabel(sub[1])
	}
	return b.rawLabel(match)
}

func (b *identityBinder) placeholderLabel(num string) int {
	if l, ok := b.known[num]; ok {
		b.taken[l] = true
		return l
	}
	if b.recorded {
		b.next++
		b.known[num] = b.next
		return b.next
	}
	n, err := strconv.Atoi(num)
	if err != nil {
		return unknownLabelBase
	}
	return unknownLabelBase + n
}

func (b *identityBinder) rawLabel(addr string) int {
	if l, ok := b.raw[addr]; ok {
		return l
	}
	var l int
	if b.recorded {
		b.next++
		l = b.next
	} else {
		for l = 1; b.taken[l]; l++ {
		}
	}
	b.raw[addr] = l
	b.taken[l] = true
	return l
}

// identityToken is the text that stands for identity label in a match key. It
// is letters only, so Template leaves it alone.
func identityToken(label int) string {
	var digits []byte
	for n := label; ; n /= 26 {
		digits = append([]byte{byte('a' + n%26)}, digits...)
		if n < 26 {
			break
		}
	}
	return "attestidentity" + string(digits)
}

// unplaceholder rewrites every stored placeholder to a synthetic raw value of
// the same kind, distinct per number, so Template renumbers placeholders and
// raw values with the same rules. Emails are the exception: they become
// session-wide identity labels, which Template does not renumber.
func unplaceholder(v any, nonce string, ids *identityBinder) any {
	switch x := v.(type) {
	case map[string]any:
		for _, k := range walkKeys(x) {
			x[k] = unplaceholder(x[k], nonce, ids)
		}
		return x
	case []any:
		for i, e := range x {
			x[i] = unplaceholder(e, nonce, ids)
		}
		return x
	case string:
		x = judge.CanonicalizeNonce(x, nonce)
		x = emailIdentity.ReplaceAllStringFunc(x, func(m string) string { return identityToken(ids.label(m)) })
		return storedPlaceholder.ReplaceAllStringFunc(x, func(m string) string {
			sub := storedPlaceholder.FindStringSubmatch(m)
			switch sub[1] {
			case "id":
				return "attestph" + sub[2] + "00000000"
			default:
				return "2000-01-01T00:00:00Z"
			}
		})
	default:
		return v
	}
}

// frameNonce returns the judge delimiter nonce a request declares, found from
// the frame structure of its string values, or "" when it declares none. Only
// this nonce is canonicalized; any other delimiter-shaped text stays.
func frameNonce(v any) string {
	switch x := v.(type) {
	case map[string]any:
		for _, e := range x {
			if n := frameNonce(e); n != "" {
				return n
			}
		}
	case []any:
		for _, e := range x {
			if n := frameNonce(e); n != "" {
				return n
			}
		}
	case string:
		return judge.FrameNonce(x)
	}
	return ""
}

// normalizeRequest redacts doc and returns it twice: normalized as a whole,
// which is the match key's input, and with placeholders made raw but not yet
// renumbered, which is what a miss diagnosis compares part by part. doc is
// consumed.
func normalizeRequest(doc any, r *redact.Redactor, ids *identityBinder) (normalized, raw any, err error) {
	doc, err = redactTree(doc, r)
	if err != nil {
		return nil, nil, err
	}
	raw = unplaceholder(doc, frameNonce(doc), ids)
	return Template(cloneJSON(raw)), raw, nil
}

// normalizePath redacts and renumbers the volatile values of a request path,
// the same way on the stored and the live side. Only requests to an API with
// no known prompt split are keyed on it.
func normalizePath(path string, r *redact.Redactor) string {
	return Template(unplaceholder(r.String(path), "", newRecordedBinder().liveBinder())).(string)
}

// cloneJSON deep-copies a decoded JSON value.
func cloneJSON(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = cloneJSON(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = cloneJSON(e)
		}
		return out
	default:
		return v
	}
}

// requestIdentity is a normalized request reduced to what replay matches on.
type requestIdentity struct {
	provider string
	api      string
	method   string
	path     string // set for api "other" only
	key      Key
	comps    components
}

// components are the parts of a request, hashed separately so a miss can name
// the first one that changed.
type components struct {
	system   any
	messages any
	tools    any
	params   any
	model    string
}

// identityOf computes the matching identity of a request: the key from its
// normalized form, the diagnosis parts from its raw (unnumbered) form.
func identityOf(provider, api, method, path string, normalized, raw any) (requestIdentity, error) {
	key, err := keyOf(api, normalized)
	if err != nil {
		return requestIdentity{}, err
	}
	id := requestIdentity{provider: provider, api: api, method: method, key: key, comps: componentsOf(api, raw)}
	if api == "other" {
		id.path = path
	}
	return id, nil
}

// String is the full match key.
func (id requestIdentity) String() string {
	tools := ""
	if id.key.ToolSchemaHash != nil {
		tools = *id.key.ToolSchemaHash
	}
	return strings.Join([]string{id.provider, id.api, id.method, id.path, id.key.Model, string(id.key.Params), id.key.PromptHash, tools}, "\x00")
}

func isSystemRole(m any) bool {
	role, _ := m.(map[string]any)["role"].(string)
	return role == "system" || role == "developer"
}

// componentsOf splits a request into the parts promptFields and keyOf treat as
// identity.
func componentsOf(api string, req any) components {
	body, _ := req.(map[string]any)
	if body == nil {
		return components{messages: req}
	}
	c := components{model: stringOf(body["model"]), tools: body["tools"]}
	fields, known := promptFields[api]
	rest := map[string]any{}
	for name, v := range body {
		if name == "model" || name == "tools" {
			continue
		}
		if !known || !contains(fields, name) {
			if v, keep := identityParam(name, v); keep {
				rest[name] = v
			}
		}
	}
	// A request to an API without a known prompt split is identified by its
	// whole body (minus model and tools), reported as one part.
	if !known {
		c.messages = rest
		return c
	}
	c.params = rest
	switch api {
	case "chat_completions":
		c.system, c.messages = splitMessages(body["messages"])
	case "responses":
		sys, rest := splitMessages(body["input"])
		c.system = joinSystem(body["instructions"], sys)
		c.messages = rest
	case "messages":
		c.system, c.messages = body["system"], body["messages"]
	}
	c.system, c.messages = normalizeSystem(c.system), normalizeConversation(c.messages)
	return c
}

func stringOf(v any) string {
	s, _ := v.(string)
	return s
}

// splitMessages separates system and developer messages from the rest.
func splitMessages(v any) (system, rest any) {
	list, ok := v.([]any)
	if !ok {
		return nil, v
	}
	var sys, other []any
	for _, m := range list {
		if mm, ok := m.(map[string]any); ok && isSystemRole(mm) {
			sys = append(sys, m)
		} else {
			other = append(other, m)
		}
	}
	if sys != nil {
		system = sys
	}
	return system, other
}

func joinSystem(instructions any, system any) any {
	switch {
	case instructions == nil:
		return system
	case system == nil:
		return instructions
	default:
		return []any{instructions, system}
	}
}

// hash7 is the first seven hex digits of the SHA-256 of v's canonical JSON,
// or "none" for an absent part.
func hash7(v any) string {
	if v == nil {
		return "none"
	}
	b, err := canonical(v)
	if err != nil {
		return "unhashable"
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:7]
}

// hashPart is the hash7 of a part renumbered on its own, so a volatile value
// elsewhere in the request cannot change another part's hash.
func hashPart(v any) string {
	if v == nil {
		return "none"
	}
	return hash7(Template(cloneJSON(v)))
}

// firstDifference names the first part, in the order system prompt, messages,
// tools, params, model, that differs between a recording and a live request.
// It returns "" when every part is equal.
func firstDifference(recorded, live components) string {
	type part struct {
		name string
		a, b any
	}
	for _, p := range []part{
		{"system prompt", recorded.system, live.system},
		{"messages", recorded.messages, live.messages},
		{"tools", recorded.tools, live.tools},
		{"params", recorded.params, live.params},
	} {
		if ha, hb := hashPart(p.a), hashPart(p.b); ha != hb {
			return fmt.Sprintf("%s changed (hash %s→%s)", p.name, ha, hb)
		}
	}
	if recorded.model != live.model {
		return fmt.Sprintf("model changed (%q→%q)", recorded.model, live.model)
	}
	return ""
}
