// Package contractlog records the assertion specs an engine process evaluates
// so `attest mutate` can replay them against mutated fixtures. A suite's
// contracts are test code; the specs are the data that survives the run.
package contractlog

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/attest-ai/attest/engine/pkg/types"
)

// EnvDir is the environment variable that turns recording on. Its value is
// the directory the engine writes to.
const EnvDir = "ATTEST_CONTRACTS_DIR"

// maxLineBytes bounds one recorded line when loading. An effect spec carries
// its world snapshot, so lines are larger than typical NDJSON.
const maxLineBytes = 32 << 20

// Spec is the part of an assertion that is recorded: no trace, no output.
type Spec struct {
	AssertionID string          `json:"assertion_id"`
	Type        string          `json:"type"`
	Spec        json.RawMessage `json:"spec"`
}

// Entry is one recorded line.
type Entry struct {
	TestID    string `json:"test_id,omitempty"`
	File      string `json:"file,omitempty"`
	Line      int    `json:"line,omitempty"`
	Assertion Spec   `json:"assertion"`
}

// Recorder appends entries to one NDJSON file owned by this process.
type Recorder struct {
	mu   sync.Mutex
	file *os.File
}

// NewFromEnv returns a Recorder writing under $ATTEST_CONTRACTS_DIR, or nil
// when the variable is unset or empty. The directory is created 0700 and the
// file 0600; the file name carries the pid and a random suffix so concurrent
// engine processes never share a file.
func NewFromEnv() (*Recorder, error) {
	dir := os.Getenv(EnvDir)
	if dir == "" {
		return nil, nil
	}
	return New(dir)
}

// New creates the directory and this process's file under dir.
func New(dir string) (*Recorder, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("%s: create %s: %w", EnvDir, dir, err)
	}
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return nil, fmt.Errorf("%s: random file suffix: %w", EnvDir, err)
	}
	name := fmt.Sprintf("contracts-%d-%s.ndjson", os.Getpid(), hex.EncodeToString(suffix[:]))
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("%s: create record file: %w", EnvDir, err)
	}
	return &Recorder{file: f}, nil
}

// Path is the file this recorder writes.
func (r *Recorder) Path() string { return r.file.Name() }

// Record appends one line per assertion. ctx may be nil.
func (r *Recorder) Record(ctx *types.BatchContext, assertions []types.Assertion) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for i := range assertions {
		a := &assertions[i]
		e := Entry{Assertion: Spec{AssertionID: a.AssertionID, Type: a.Type, Spec: a.Spec}}
		if ctx != nil {
			e.TestID, e.File, e.Line = ctx.TestID, ctx.File, ctx.Line
		}
		if len(e.Assertion.Spec) == 0 {
			e.Assertion.Spec = json.RawMessage("null")
		}
		if err := enc.Encode(&e); err != nil {
			return fmt.Errorf("record assertion %s: %w", a.AssertionID, err)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.file.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("write %s: %w", r.file.Name(), err)
	}
	return nil
}

// Close closes the file.
func (r *Recorder) Close() error { return r.file.Close() }

// Load reads every regular *.ndjson file directly under dir. Entries are
// de-duplicated on (test, type, spec) and sorted on every field, so the
// result does not depend on file names, process ids or evaluation order.
func Load(dir string) ([]Entry, error) {
	infos, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("contracts directory: %w", err)
	}
	var all []Entry
	for _, info := range infos {
		if !info.Type().IsRegular() || filepath.Ext(info.Name()) != ".ndjson" {
			continue
		}
		entries, err := loadFile(filepath.Join(dir, info.Name()))
		if err != nil {
			return nil, err
		}
		all = append(all, entries...)
	}
	sort.SliceStable(all, func(i, j int) bool { return less(all[i], all[j]) })
	seen := map[string]bool{}
	var out []Entry
	for _, e := range all {
		key := e.TestID + "\x00" + e.Assertion.Type + "\x00" + canonical(e.Assertion.Spec)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, e)
	}
	return out, nil
}

// less orders entries by test, type and spec, then by the fields that tell
// two recordings of the same spec apart, so the entry kept by Load is the
// same whichever process recorded it first.
func less(a, b Entry) bool {
	if a.TestID != b.TestID {
		return a.TestID < b.TestID
	}
	if a.Assertion.Type != b.Assertion.Type {
		return a.Assertion.Type < b.Assertion.Type
	}
	if as, bs := canonical(a.Assertion.Spec), canonical(b.Assertion.Spec); as != bs {
		return as < bs
	}
	if a.Assertion.AssertionID != b.Assertion.AssertionID {
		return a.Assertion.AssertionID < b.Assertion.AssertionID
	}
	if a.File != b.File {
		return a.File < b.File
	}
	return a.Line < b.Line
}

func loadFile(name string) ([]Entry, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	var out []Entry
	for n := 1; ; n++ {
		line, err := readLine(r)
		if errors.Is(err, io.EOF) && len(line) == 0 {
			return out, nil
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s:%d: %w", name, n, err)
		}
		if len(bytes.TrimSpace(line)) > 0 {
			var e Entry
			if jerr := json.Unmarshal(line, &e); jerr != nil {
				return nil, fmt.Errorf("%s:%d: %w", name, n, jerr)
			}
			if e.Assertion.Type == "" {
				return nil, fmt.Errorf("%s:%d: assertion.type is required", name, n)
			}
			out = append(out, e)
		}
		if errors.Is(err, io.EOF) {
			return out, nil
		}
	}
}

// readLine returns one line without its newline, refusing lines over maxLineBytes.
func readLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, isPrefix, err := r.ReadLine()
		line = append(line, chunk...)
		if len(line) > maxLineBytes {
			return nil, fmt.Errorf("line exceeds %d bytes", maxLineBytes)
		}
		if err != nil || !isPrefix {
			return line, err
		}
	}
}

// canonical returns a key-order-independent form of raw JSON.
func canonical(raw json.RawMessage) string {
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return strings.TrimSpace(string(raw))
	}
	out, err := json.Marshal(v)
	if err != nil {
		return strings.TrimSpace(string(raw))
	}
	return string(out)
}
