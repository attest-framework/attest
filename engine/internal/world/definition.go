// Package world serves a declared external system as a local stateful HTTP
// fake: JSON resources with create, list and get routes, idempotency-key
// handling, initial state and a seeded fault script. world.yaml is the
// definition (world-v1); a Snapshot is the state it produced.
package world

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
	"gopkg.in/yaml.v3"
)

// Version is the world-v1 schema version.
const Version = 1

// idPattern bounds ids so every record stays addressable as one path segment.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,128}$`)

// MaxDefinitionBytes bounds a world.yaml.
const MaxDefinitionBytes = 256 << 10

// schemaJSON is a copy of proto/attest/v1/world-v1.schema.json; a test keeps
// the two byte-identical.
//
//go:embed world-v1.schema.json
var schemaJSON []byte

// Route names a request shape on a resource.
type Route string

// Routes a resource can declare.
const (
	RouteCreate Route = "create"
	RouteList   Route = "list"
	RouteGet    Route = "get"
)

// FaultKind is a seeded fault. It marshals as the string timeout_after_commit
// or the integer status code, the forms world.yaml uses.
type FaultKind struct{ name string }

// The supported faults.
var (
	FaultTimeoutAfterCommit = FaultKind{"timeout_after_commit"}
	Fault429                = FaultKind{"429"}
	Fault500                = FaultKind{"500"}
)

// Status is the HTTP status a 429 or 500 fault answers with, 0 otherwise.
func (k FaultKind) Status() int {
	switch k {
	case Fault429:
		return 429
	case Fault500:
		return 500
	}
	return 0
}

// String is the fault as world.yaml writes it.
func (k FaultKind) String() string { return k.name }

// MarshalJSON writes the world.yaml form.
func (k FaultKind) MarshalJSON() ([]byte, error) {
	if st := k.Status(); st != 0 {
		return json.Marshal(st)
	}
	return json.Marshal(k.name)
}

// UnmarshalJSON reads the world.yaml form.
func (k *FaultKind) UnmarshalJSON(b []byte) error {
	switch string(b) {
	case `"timeout_after_commit"`:
		*k = FaultTimeoutAfterCommit
	case "429":
		*k = Fault429
	case "500":
		*k = Fault500
	default:
		return fmt.Errorf("unsupported fault %s", b)
	}
	return nil
}

// Idempotency names the header a create honors and the record field that
// stores the key.
type Idempotency struct {
	Header string `json:"header"`
	Field  string `json:"field"`
}

// Resource is one collection.
type Resource struct {
	Path        string           `json:"path"`
	IDField     string           `json:"id_field"`
	IDFormat    string           `json:"id_format"`
	Routes      []Route          `json:"routes"`
	Filters     []string         `json:"filters"`
	Idempotency *Idempotency     `json:"idempotency"`
	Seed        []map[string]any `json:"seed"`
}

func (r *Resource) has(route Route) bool {
	for _, x := range r.Routes {
		if x == route {
			return true
		}
	}
	return false
}

// Fault is one entry of the fault script.
type Fault struct {
	ID          string    `json:"id"`
	Resource    string    `json:"resource"`
	Route       Route     `json:"route"`
	Kind        FaultKind `json:"fault"`
	OnRequests  []int     `json:"on_requests"`
	Probability float64   `json:"probability"`
}

// Definition is a validated world.yaml.
type Definition struct {
	WorldVersion int                  `json:"world_version"`
	BaseURLEnv   string               `json:"base_url_env"`
	Seed         uint64               `json:"seed"`
	Resources    map[string]*Resource `json:"resources"`
	Faults       []Fault              `json:"faults"`
	// Hash is the sha256 of the definition's canonical JSON.
	Hash string `json:"-"`
}

// DefinitionError is a rejected world.yaml. Path is the JSON path of the
// offending value ("" for the document as a whole).
type DefinitionError struct {
	Path string
	Msg  string
}

func (e *DefinitionError) Error() string {
	if e.Path == "" {
		return "world definition: " + e.Msg
	}
	return fmt.Sprintf("world definition: %s: %s", e.Path, e.Msg)
}

// DefinitionErrors collects every problem found in one pass.
type DefinitionErrors []*DefinitionError

func (es DefinitionErrors) Error() string {
	parts := make([]string, len(es))
	for i, e := range es {
		parts[i] = e.Error()
	}
	return strings.Join(parts, "\n")
}

// LoadFile reads, validates and returns the world.yaml at path.
func LoadFile(path string) (*Definition, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxDefinitionBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxDefinitionBytes {
		return nil, &DefinitionError{Msg: fmt.Sprintf("%s is larger than %d bytes", path, MaxDefinitionBytes)}
	}
	return Parse(data)
}

// Parse converts YAML (or JSON) to JSON, validates it against world-v1 and
// the cross-field rules a schema cannot express, and returns the definition.
func Parse(data []byte) (*Definition, error) {
	doc, err := yamlToJSONValue(data)
	if err != nil {
		return nil, err
	}
	if err := validateSchema(doc, "#"); err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	d := &Definition{}
	dec := json.NewDecoder(bytes.NewReader(canonical))
	dec.UseNumber()
	if err := dec.Decode(d); err != nil {
		return nil, &DefinitionError{Msg: err.Error()}
	}
	d.applyDefaults()
	if errs := d.check(); len(errs) > 0 {
		return nil, errs
	}
	sum := sha256.Sum256(canonical)
	d.Hash = hex.EncodeToString(sum[:])
	return d, nil
}

func (d *Definition) applyDefaults() {
	for _, r := range d.Resources {
		if r.IDField == "" {
			r.IDField = "id"
		}
		if r.IDFormat == "" {
			r.IDFormat = "%d"
		}
	}
}

// check enforces what the schema cannot: unique paths, seed records that
// carry unique string ids, and faults that name a declared resource and route.
func (d *Definition) check() DefinitionErrors {
	var errs DefinitionErrors
	bad := func(path, format string, a ...any) {
		errs = append(errs, &DefinitionError{Path: path, Msg: fmt.Sprintf(format, a...)})
	}
	names := make([]string, 0, len(d.Resources))
	for n := range d.Resources {
		names = append(names, n)
	}
	sort.Strings(names)
	paths := map[string]string{}
	for _, n := range names {
		r := d.Resources[n]
		at := "/resources/" + n
		if other, dup := paths[r.Path]; dup {
			bad(at+"/path", "%s is already served by %q", r.Path, other)
		}
		paths[r.Path] = n
		if r.Idempotency != nil && !r.has(RouteCreate) {
			bad(at+"/idempotency", "needs the create route")
		}
		if len(r.Filters) > 0 && !r.has(RouteList) {
			bad(at+"/filters", "needs the list route")
		}
		seen := map[string]bool{}
		for i, rec := range r.Seed {
			id, ok := rec[r.IDField].(string)
			switch {
			case !ok || !idPattern.MatchString(id):
				bad(fmt.Sprintf("%s/seed/%d", at, i), "record needs a string %q of letters, digits, '.', '_', '~' or '-'", r.IDField)
			case seen[id]:
				bad(fmt.Sprintf("%s/seed/%d/%s", at, i, r.IDField), "duplicate id %q", id)
			}
			seen[id] = true
		}
	}
	for i, f := range d.Faults {
		at := fmt.Sprintf("/faults/%d", i)
		r, ok := d.Resources[f.Resource]
		switch {
		case !ok:
			bad(at+"/resource", "%q is not a declared resource", f.Resource)
		case !r.has(f.Route):
			bad(at+"/route", "resource %q does not declare the %s route", f.Resource, f.Route)
		}
	}
	return errs
}

func yamlToJSONValue(data []byte) (any, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var raw any
	if err := dec.Decode(&raw); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, &DefinitionError{Msg: "empty document"}
		}
		return nil, &DefinitionError{Msg: "not valid YAML: " + err.Error()}
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, &DefinitionError{Msg: "more than one YAML document"}
	}
	return toJSONValue(raw, "")
}

// toJSONValue converts decoded YAML to values encoding/json accepts,
// rejecting what has no JSON form: non-string keys and unquoted timestamps.
func toJSONValue(v any, path string) (any, error) {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			c, err := toJSONValue(x, path+"/"+k)
			if err != nil {
				return nil, err
			}
			out[k] = c
		}
		return out, nil
	case map[any]any:
		return nil, &DefinitionError{Path: path, Msg: "mapping keys must be strings"}
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			c, err := toJSONValue(x, fmt.Sprintf("%s/%d", path, i))
			if err != nil {
				return nil, err
			}
			out[i] = c
		}
		return out, nil
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return nil, &DefinitionError{Path: path, Msg: "not a finite number"}
		}
		if t == math.Trunc(t) && math.Abs(t) >= 1<<53 {
			return nil, &DefinitionError{Path: path, Msg: "integer too large to keep exactly; quote it to make it a string"}
		}
		return t, nil
	case time.Time:
		return nil, &DefinitionError{Path: path, Msg: "unquoted timestamp; quote it to make it a string"}
	case []byte:
		return nil, &DefinitionError{Path: path, Msg: "binary values are not JSON"}
	}
	return v, nil
}

// validateSchema validates doc against the world-v1 schema ref ("#" for the
// definition, "#/$defs/snapshot" for a snapshot).
func validateSchema(doc any, ref string) error {
	s, err := compiled(ref)
	if err != nil {
		return err
	}
	// jsonschema wants JSON-decoded values; round-trip so ints become numbers.
	b, err := json.Marshal(doc)
	if err != nil {
		return &DefinitionError{Msg: err.Error()}
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		return &DefinitionError{Msg: err.Error()}
	}
	verr := s.Validate(inst)
	if verr == nil {
		return nil
	}
	var ve *jsonschema.ValidationError
	if !errors.As(verr, &ve) {
		return verr
	}
	p := message.NewPrinter(language.English)
	var errs DefinitionErrors
	var walk func(*jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			path := "/"
			if len(e.InstanceLocation) > 0 {
				path = "/" + strings.Join(e.InstanceLocation, "/")
			}
			errs = append(errs, &DefinitionError{Path: path, Msg: e.ErrorKind.LocalizedString(p)})
			return
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(ve)
	return errs
}

var (
	compileMu sync.Mutex
	schemas   = map[string]*jsonschema.Schema{}
)

// compiled returns the world-v1 schema at ref, compiling it once.
func compiled(ref string) (*jsonschema.Schema, error) {
	compileMu.Lock()
	defer compileMu.Unlock()
	if s, ok := schemas[ref]; ok {
		return s, nil
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	schemaDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaJSON))
	if err != nil {
		return nil, fmt.Errorf("world-v1 schema: %w", err)
	}
	const url = "world-v1.schema.json"
	if err := c.AddResource(url, schemaDoc); err != nil {
		return nil, fmt.Errorf("world-v1 schema: %w", err)
	}
	s, err := c.Compile(url + ref)
	if err != nil {
		return nil, fmt.Errorf("world-v1 schema: %w", err)
	}
	schemas[ref] = s
	return s, nil
}

// ValidateSnapshot checks a snapshot document against world-v1.
func ValidateSnapshot(raw []byte) error {
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return &DefinitionError{Msg: "snapshot is not JSON: " + err.Error()}
	}
	return validateSchema(doc, "#/$defs/snapshot")
}
