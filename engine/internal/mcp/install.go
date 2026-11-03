package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/attest-ai/attest/engine/internal/pathguard"
)

// maxConfigBytes caps an existing client config the installer reads.
const maxConfigBytes = 4 << 20

// ServerName is the key the attest entry is written under in every client config.
const ServerName = "attest"

// InstallResult lists the client configs by what the install did. Paths are
// relative to the project directory, with forward slashes.
type InstallResult struct {
	Dir       string   `json:"dir"`
	Created   []string `json:"created"`
	Updated   []string `json:"updated"`
	Unchanged []string `json:"unchanged"`
	Conflict  []string `json:"conflict"`
	// Manual lists the conflicts --force cannot resolve: an attest entry that is
	// not a [mcp_servers.attest] table. It is not part of the JSON output.
	Manual []string `json:"-"`
}

const (
	maxCommandArgs = 32
	maxCommandArg  = 1024
)

// ValidateCommand checks the launch argv written into client configs.
func ValidateCommand(argv []string) error {
	if len(argv) == 0 || len(argv) > maxCommandArgs {
		return fmt.Errorf("command must hold between 1 and %d elements", maxCommandArgs)
	}
	for i, a := range argv {
		if a == "" || len(a) > maxCommandArg || hasControl(a) {
			return fmt.Errorf("command[%d] must be 1 to %d characters with no control characters", i, maxCommandArg)
		}
	}
	return nil
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

type configTarget struct {
	rel   string
	apply func(existing []byte, argv []string, force bool) (out []byte, state string, err error)
}

// state values returned by apply.
const (
	stateCreated   = "created"
	stateUpdated   = "updated"
	stateUnchanged = "unchanged"
	stateConflict  = "conflict"
	// stateManual is a conflict --force does not replace.
	stateManual = "conflict-manual"
)

var configTargets = []configTarget{
	{".mcp.json", func(b []byte, argv []string, force bool) ([]byte, string, error) {
		return mergeJSONConfig(b, argv, false, force)
	}},
	{".cursor/mcp.json", func(b []byte, argv []string, force bool) ([]byte, string, error) {
		return mergeJSONConfig(b, argv, true, force)
	}},
	{".codex/config.toml", mergeCodexConfig},
}

// Install writes or merges the attest server entry into the Claude Code,
// Cursor and Codex project configs under dir. Other servers and keys are left
// as they are. A different existing attest entry is a conflict and is left
// alone unless force. Every destination is validated and every merge computed
// before the first write, so a refusal writes nothing.
func Install(dir string, argv []string, force bool) (InstallResult, error) {
	res := InstallResult{Created: []string{}, Updated: []string{}, Unchanged: []string{}, Conflict: []string{}}
	if err := ValidateCommand(argv); err != nil {
		return res, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return res, fmt.Errorf("project directory: %w", err)
	}
	root, err := pathguard.NewRoot(dir)
	if err != nil {
		return res, err
	}
	defer root.Close()
	res.Dir = root.Path()

	type plan struct {
		rel, state string
		out        []byte
		mode       os.FileMode
		exists     bool
	}
	var plans []plan
	for _, t := range configTargets {
		if err := pathguard.RefuseAbsoluteSymlinks(root.Path(), t.rel); err != nil {
			return res, err
		}
		if _, err := pathguard.ResolveDestination(root.Path(), t.rel); err != nil {
			return res, err
		}
		existing, mode, exists, err := root.ReadExisting(t.rel, maxConfigBytes)
		if err != nil {
			return res, err
		}
		out, state, err := t.apply(existing, argv, force)
		if err != nil {
			return res, fmt.Errorf("%s: %w", t.rel, err)
		}
		plans = append(plans, plan{t.rel, state, out, mode, exists})
	}
	for _, p := range plans {
		if p.state == stateCreated || p.state == stateUpdated {
			if err := root.WriteAtomic(p.rel, p.out, p.mode, p.exists); err != nil {
				return res, fmt.Errorf("%s: %w", p.rel, err)
			}
		}
		switch p.state {
		case stateCreated:
			res.Created = append(res.Created, p.rel)
		case stateUpdated:
			res.Updated = append(res.Updated, p.rel)
		case stateUnchanged:
			res.Unchanged = append(res.Unchanged, p.rel)
		case stateManual:
			res.Conflict = append(res.Conflict, p.rel)
			res.Manual = append(res.Manual, p.rel)
		default:
			res.Conflict = append(res.Conflict, p.rel)
		}
	}
	return res, nil
}

// ── JSON clients (Claude Code .mcp.json, Cursor .cursor/mcp.json) ──

type member struct {
	key string
	raw json.RawMessage
}

// readObject decodes a JSON object keeping member order.
func readObject(data []byte) ([]member, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, errors.New("not valid JSON")
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("not a JSON object")
	}
	var out []member
	seen := map[string]bool{}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, errors.New("not valid JSON")
		}
		key := kt.(string)
		if seen[key] {
			return nil, fmt.Errorf("duplicate key %q", key)
		}
		seen[key] = true
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, errors.New("not valid JSON")
		}
		out = append(out, member{key, raw})
	}
	if _, err := dec.Token(); err != nil {
		return nil, errors.New("not valid JSON")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing content after the JSON object")
	}
	return out, nil
}

// writeObject encodes members in order, indented two spaces per level below
// level (the indentation of the object's own line).
func writeObject(members []member, level string) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("{")
	for i, m := range members {
		if i > 0 {
			b.WriteString(",")
		}
		k, _ := json.Marshal(m.key)
		b.WriteString("\n" + level + "  " + string(k) + ": ")
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, m.raw, level+"  ", "  "); err != nil {
			return nil, err
		}
		b.Write(pretty.Bytes())
	}
	if len(members) > 0 {
		b.WriteString("\n" + level)
	}
	b.WriteString("}")
	return b.Bytes(), nil
}

func setMember(ms []member, key string, raw json.RawMessage) []member {
	for i := range ms {
		if ms[i].key == key {
			ms[i].raw = raw
			return ms
		}
	}
	return append(ms, member{key, raw})
}

func getMember(ms []member, key string) (json.RawMessage, bool) {
	for _, m := range ms {
		if m.key == key {
			return m.raw, true
		}
	}
	return nil, false
}

// serverEntry is the attest entry of a JSON client config. Field order is the
// order written.
type serverEntry struct {
	Type    string   `json:"type,omitempty"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

func jsonEntry(argv []string, typed bool) serverEntry {
	e := serverEntry{Command: argv[0], Args: append([]string{}, argv[1:]...)}
	if typed {
		e.Type = "stdio"
	}
	return e
}

func mergeJSONConfig(existing []byte, argv []string, typed, force bool) ([]byte, string, error) {
	if len(bytes.TrimSpace(existing)) == 0 {
		out, err := jsonConfig(nil, argv, typed)
		return out, stateCreated, err
	}
	top, err := readObject(existing)
	if err != nil {
		return nil, "", err
	}
	if rawServers, ok := getMember(top, "mcpServers"); ok {
		servers, err := readObject(rawServers)
		if err != nil {
			return nil, "", fmt.Errorf("mcpServers: %w", err)
		}
		if cur, ok := getMember(servers, ServerName); ok {
			var have, want any
			if json.Unmarshal(cur, &have) != nil {
				return nil, "", errors.New("mcpServers.attest is not valid JSON")
			}
			wantRaw, _ := json.Marshal(jsonEntry(argv, typed))
			_ = json.Unmarshal(wantRaw, &want)
			if reflect.DeepEqual(have, want) {
				return existing, stateUnchanged, nil
			}
			if !force {
				return existing, stateConflict, nil
			}
			out, err := jsonConfig(existing, argv, typed)
			return out, stateUpdated, err
		}
	}
	out, err := jsonConfig(existing, argv, typed)
	return out, stateUpdated, err
}

// jsonConfig returns existing with the attest entry set, replacing any entry.
func jsonConfig(existing []byte, argv []string, typed bool) ([]byte, error) {
	var top []member
	if len(bytes.TrimSpace(existing)) > 0 {
		var err error
		if top, err = readObject(existing); err != nil {
			return nil, err
		}
	}
	var servers []member
	if raw, ok := getMember(top, "mcpServers"); ok {
		var err error
		if servers, err = readObject(raw); err != nil {
			return nil, fmt.Errorf("mcpServers: %w", err)
		}
	}
	entry, err := json.Marshal(jsonEntry(argv, typed))
	if err != nil {
		return nil, err
	}
	servers = setMember(servers, ServerName, entry)
	inner, err := writeObject(servers, "")
	if err != nil {
		return nil, err
	}
	top = setMember(top, "mcpServers", inner)
	out, err := writeObject(top, "")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// ── Codex (.codex/config.toml) ──

var (
	tomlHeader = regexp.MustCompile(`^\s*\[\[?\s*([^\]]+?)\s*\]\]?\s*(#.*)?$`)
	attestKey  = regexp.MustCompile(`^mcp_servers\s*\.\s*("attest"|'attest'|attest)(\s*\..*)?$`)
)

// codexEntry renders the attest table for argv.
func codexEntry(argv []string) string {
	args := make([]string, 0, len(argv)-1)
	for _, a := range argv[1:] {
		args = append(args, strconv.Quote(a))
	}
	return "[mcp_servers." + ServerName + "]\ncommand = " + strconv.Quote(argv[0]) + "\nargs = [" + strings.Join(args, ", ") + "]\n"
}

func codexWant(argv []string) map[string]any {
	args := make([]any, 0, len(argv)-1)
	for _, a := range argv[1:] {
		args = append(args, a)
	}
	return map[string]any{"command": argv[0], "args": args}
}

// attestSpan finds the lines of the [mcp_servers.attest] table and its
// sub-tables. Other forms (an inline table, dotted keys) have no span.
func attestSpan(lines []string) (start, end int, found bool) {
	start = -1
	for i, l := range lines {
		m := tomlHeader.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		isAttest := attestKey.MatchString(strings.TrimSpace(m[1]))
		switch {
		case isAttest && start < 0:
			start = i
		case !isAttest && start >= 0:
			return start, i, true
		}
	}
	if start >= 0 {
		return start, len(lines), true
	}
	return 0, 0, false
}

// parseCodex decodes a Codex config and returns the document and its attest
// server entry, if any. The error never quotes the file.
func parseCodex(data []byte) (doc map[string]any, entry any, has bool, err error) {
	if err := toml.Unmarshal(data, &doc); err != nil {
		var de *toml.DecodeError
		if errors.As(err, &de) {
			row, col := de.Position()
			return nil, nil, false, fmt.Errorf("not valid TOML (line %d, column %d)", row, col)
		}
		return nil, nil, false, errors.New("not valid TOML")
	}
	raw, present := doc["mcp_servers"]
	if !present {
		return doc, nil, false, nil
	}
	servers, ok := raw.(map[string]any)
	if !ok {
		return nil, nil, false, errors.New("mcp_servers is not a table")
	}
	entry, has = servers[ServerName]
	return doc, entry, has, nil
}

// withoutAttest returns doc with mcp_servers.attest removed.
func withoutAttest(doc map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range doc {
		if k != "mcp_servers" {
			out[k] = v
			continue
		}
		servers := map[string]any{}
		for sk, sv := range v.(map[string]any) {
			if sk != ServerName {
				servers[sk] = sv
			}
		}
		if len(servers) > 0 {
			out[k] = servers
		}
	}
	return out
}

// checkCodexOutput refuses to write a config that does not parse, or that
// differs from the original in anything but the attest entry.
func checkCodexOutput(out []byte, original map[string]any, argv []string) error {
	doc, entry, has, err := parseCodex(out)
	if err != nil {
		return fmt.Errorf("refusing to write: the result is %w", err)
	}
	if !has || !reflect.DeepEqual(entry, any(codexWant(argv))) {
		return errors.New("refusing to write: the result does not hold the attest entry")
	}
	if !reflect.DeepEqual(withoutAttest(doc), withoutAttest(original)) {
		return errors.New("refusing to write: the result changes more than the attest entry")
	}
	return nil
}

// mergeCodexConfig adds the attest entry to a Codex config. An existing
// different entry is a conflict. With force, an entry written as a
// [mcp_servers.attest] table is replaced; an entry in any other form (an inline
// table, dotted keys) is never replaced, because it has no safe text span.
func mergeCodexConfig(existing []byte, argv []string, force bool) ([]byte, string, error) {
	if len(bytes.TrimSpace(existing)) == 0 {
		out := []byte(codexEntry(argv))
		return out, stateCreated, checkCodexOutput(out, map[string]any{}, argv)
	}
	doc, entry, has, err := parseCodex(existing)
	if err != nil {
		return nil, "", err
	}
	text := string(existing)
	state := stateUpdated
	if has {
		if reflect.DeepEqual(entry, any(codexWant(argv))) {
			return existing, stateUnchanged, nil
		}
		lines := strings.SplitAfter(text, "\n")
		start, end, found := attestSpan(lines)
		if !found {
			return existing, stateManual, nil
		}
		if !force {
			return existing, stateConflict, nil
		}
		text = strings.Join(lines[:start], "") + strings.Join(lines[end:], "")
	}
	text = strings.TrimRight(text, "\n")
	if text != "" {
		text += "\n\n"
	}
	out := []byte(text + codexEntry(argv))
	if err := checkCodexOutput(out, doc, argv); err != nil {
		if has {
			return existing, stateManual, nil
		}
		return nil, "", err
	}
	return out, state, nil
}
