package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/attest-ai/attest/engine/internal/pathguard"
)

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
		if a == "" || len(a) > maxCommandArg || strings.ContainsAny(a, "\x00\n\r") {
			return fmt.Errorf("command[%d] must be 1 to %d characters with no NUL or line break", i, maxCommandArg)
		}
	}
	return nil
}

type configTarget struct {
	rel   string
	apply func(existing []byte, argv []string) (out []byte, state string, err error)
}

// state values returned by apply.
const (
	stateCreated   = "created"
	stateUpdated   = "updated"
	stateUnchanged = "unchanged"
	stateConflict  = "conflict"
)

var configTargets = []configTarget{
	{".mcp.json", func(b []byte, argv []string) ([]byte, string, error) { return mergeJSONConfig(b, argv, false) }},
	{".cursor/mcp.json", func(b []byte, argv []string) ([]byte, string, error) { return mergeJSONConfig(b, argv, true) }},
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
	res.Dir = root.Path()

	type plan struct {
		rel, real, state string
		out              []byte
	}
	var plans []plan
	for _, t := range configTargets {
		real, err := pathguard.ResolveDestination(root.Path(), t.rel)
		if err != nil {
			return res, err
		}
		existing, err := os.ReadFile(real)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return res, fmt.Errorf("%s: %w", t.rel, err)
		}
		out, state, err := t.apply(existing, argv)
		if err != nil {
			return res, fmt.Errorf("%s: %w", t.rel, err)
		}
		if state == stateConflict && force {
			if out, err = replaceEntry(t, existing, argv); err != nil {
				return res, fmt.Errorf("%s: %w", t.rel, err)
			}
			state = stateUpdated
		}
		plans = append(plans, plan{t.rel, real, state, out})
	}
	for _, p := range plans {
		switch p.state {
		case stateCreated, stateUpdated:
			if err := os.MkdirAll(filepath.Dir(p.real), 0o755); err != nil {
				return res, fmt.Errorf("%s: %w", p.rel, err)
			}
			if err := os.WriteFile(p.real, p.out, 0o644); err != nil {
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
		default:
			res.Conflict = append(res.Conflict, p.rel)
		}
	}
	return res, nil
}

// replaceEntry rebuilds the config with the attest entry replaced.
func replaceEntry(t configTarget, existing []byte, argv []string) ([]byte, error) {
	if strings.HasSuffix(t.rel, ".toml") {
		return codexConfig(existing, argv, true)
	}
	return jsonConfig(existing, argv, strings.HasPrefix(t.rel, ".cursor/"), true)
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
	if _, err := dec.Token(); err == nil {
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

func mergeJSONConfig(existing []byte, argv []string, typed bool) ([]byte, string, error) {
	if len(bytes.TrimSpace(existing)) == 0 {
		out, err := jsonConfig(nil, argv, typed, false)
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
			return existing, stateConflict, nil
		}
	}
	out, err := jsonConfig(existing, argv, typed, false)
	return out, stateUpdated, err
}

// jsonConfig returns existing with the attest entry set. force has no effect
// here beyond documenting that an existing entry is overwritten.
func jsonConfig(existing []byte, argv []string, typed, _ bool) ([]byte, error) {
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
	q := func(s string) string { return strconv.Quote(s) }
	args := make([]string, 0, len(argv)-1)
	for _, a := range argv[1:] {
		args = append(args, q(a))
	}
	return "[mcp_servers." + ServerName + "]\ncommand = " + q(argv[0]) + "\nargs = [" + strings.Join(args, ", ") + "]\n"
}

// attestSpan finds the lines of the attest table and its sub-tables.
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

func meaningful(lines []string) []string {
	var out []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		out = append(out, strings.Join(strings.Fields(l), " "))
	}
	return out
}

func mergeCodexConfig(existing []byte, argv []string) ([]byte, string, error) {
	if len(bytes.TrimSpace(existing)) == 0 {
		out, err := codexConfig(nil, argv, false)
		return out, stateCreated, err
	}
	lines := strings.SplitAfter(string(existing), "\n")
	if start, end, found := attestSpan(lines); found {
		if reflect.DeepEqual(meaningful(lines[start:end]), meaningful(strings.SplitAfter(codexEntry(argv), "\n"))) {
			return existing, stateUnchanged, nil
		}
		return existing, stateConflict, nil
	}
	out, err := codexConfig(existing, argv, false)
	return out, stateUpdated, err
}

// codexConfig returns existing with the attest table appended, or replaced when
// replace is set.
func codexConfig(existing []byte, argv []string, replace bool) ([]byte, error) {
	text := string(existing)
	if replace {
		lines := strings.SplitAfter(text, "\n")
		if start, end, found := attestSpan(lines); found {
			text = strings.Join(lines[:start], "") + strings.Join(lines[end:], "")
		}
	}
	text = strings.TrimRight(text, "\n")
	if text != "" {
		text += "\n\n"
	}
	return []byte(text + codexEntry(argv)), nil
}
