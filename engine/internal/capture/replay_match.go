package capture

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"github.com/attest-framework/attest/engine/internal/assertion/judge"
	"github.com/attest-framework/attest/engine/internal/redact"
)

// Replay matching. Fixtures are templated with document-wide positional
// numbering, so a live request templated on its own does not hash equal to
// its recording. Both sides are therefore normalized per request: secrets
// are redacted, then every volatile value (raw ids, timestamps, emails and
// the {{kind_N}} placeholders a replayed response hands back to the agent) is
// renumbered by first occurrence within that request. The match key is
// computed from the normalized request; the stored prompt_hash stays
// informational.

// storedPlaceholder matches a {{kind_N}} placeholder written by Template.
var storedPlaceholder = regexp.MustCompile(`\{\{(id|email|timestamp)_(\d+)\}\}`)

// HasPlaceholder reports whether s contains a templated {{kind_N}} value.
func HasPlaceholder(s string) bool { return storedPlaceholder.MatchString(s) }

// normalizedEmail stands for every email address, raw or templated. Capture
// numbers emails by value, but a replayed response echoes its placeholder back
// next to the raw address the agent still holds, so for matching a placeholder
// and the address it stands for are the same token.
const normalizedEmail = "attest-email@attest.invalid"

// unplaceholder rewrites every stored placeholder to a synthetic raw value of
// the same kind, distinct per number, so Template renumbers placeholders and
// raw values with the same rules.
func unplaceholder(v any, nonce string) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = unplaceholder(e, nonce)
		}
		return x
	case []any:
		for i, e := range x {
			x[i] = unplaceholder(e, nonce)
		}
		return x
	case string:
		x = judge.CanonicalizeNonce(x, nonce)
		x = emailPattern.ReplaceAllString(x, normalizedEmail)
		return storedPlaceholder.ReplaceAllStringFunc(x, func(m string) string {
			sub := storedPlaceholder.FindStringSubmatch(m)
			switch sub[1] {
			case "id":
				return "attestph" + sub[2] + "00000000"
			case "email":
				return normalizedEmail
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
func normalizeRequest(doc any, r *redact.Redactor) (normalized, raw any, err error) {
	doc, err = redactTree(doc, r)
	if err != nil {
		return nil, nil, err
	}
	raw = unplaceholder(doc, frameNonce(doc))
	return Template(cloneJSON(raw)), raw, nil
}

// normalizePath redacts and renumbers the volatile values of a request path,
// the same way on the stored and the live side. Only requests to an API with
// no known prompt split are keyed on it.
func normalizePath(path string, r *redact.Redactor) string {
	return Template(unplaceholder(r.String(path), "")).(string)
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
		if name != "model" && name != "tools" && (!known || !contains(fields, name)) {
			rest[name] = v
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
