// Package capabilities is the single source of the public claims about what
// attest ships: its evaluated layers, assertion types, SDK languages, adapters
// and their maturity. `attest-engine capabilities --json` prints it, and the
// generated tables in the README and the guides are rendered from that output.
//
// Layers and assertion types are derived from pkg/types. Adapters and features
// live in data.json; tests in the engine and in both SDKs fail when its
// adapter lists differ from the adapter directories.
package capabilities

import (
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/attest-framework/attest/engine/internal/buildinfo"
	"github.com/attest-framework/attest/engine/internal/server"
	"github.com/attest-framework/attest/engine/pkg/types"
)

// SchemaVersion is the version of the capabilities document.
const SchemaVersion = 1

// License is the SPDX identifier of the project's licence.
const License = "Apache-2.0"

//go:embed data.json
var dataJSON []byte

// Maturity tiers.
const (
	MaturityStable       = "stable"
	MaturityBeta         = "beta"
	MaturityExperimental = "experimental"
)

// Layer is one evaluated layer of the assertion pipeline. The plugin layer
// keeps the internal id 8 and is described as plugins, not as a layer.
type Layer struct {
	ID             int      `json:"id"`
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	AssertionTypes []string `json:"assertion_types"`
	Cost           string   `json:"cost"`
	Deterministic  bool     `json:"deterministic"`
}

// Adapter is one trace adapter shipped by an SDK.
type Adapter struct {
	Name     string `json:"name"`
	Module   string `json:"module"`
	Maturity string `json:"maturity"`
}

// Feature is one user-facing capability and its maturity.
type Feature struct {
	Name     string `json:"name"`
	Maturity string `json:"maturity"`
}

// Adapters lists the adapters of each SDK.
type Adapters struct {
	Python     []Adapter `json:"python"`
	TypeScript []Adapter `json:"typescript"`
}

// Document is the `capabilities --json` output.
type Document struct {
	SchemaVersion       int               `json:"schema_version"`
	EngineVersion       string            `json:"engine_version"`
	ProtocolVersion     int               `json:"protocol_version"`
	License             string            `json:"license"`
	SDKLanguages        []string          `json:"sdk_languages"`
	Layers              []Layer           `json:"layers"`
	Adapters            Adapters          `json:"adapters"`
	Features            []Feature         `json:"features"`
	MaturityDefinitions map[string]string `json:"maturity_definitions"`
}

// layerSpec is the human-facing description of one layer; the assertion types
// of a layer come from types.LayerForType.
type layerSpec struct {
	id            int
	name          string
	description   string
	cost          string
	deterministic bool
}

var layerSpecs = []layerSpec{
	{1, "Schema validation", "Checks the output against a JSON Schema.", "free", true},
	{2, "Constraint checks", "Checks cost, token, latency and step-count limits.", "free", true},
	{3, "Trace and behavioral checks", "Checks which tools ran, in which order, with which arguments, how often per business key, across the agent tree and against the world's final state.", "free", true},
	{4, "Content pattern matching", "Checks the output text against substrings, patterns and keywords.", "free", true},
	{5, "Embedding similarity", "Scores semantic similarity against a reference with an embedding model.", "paid", false},
	{6, "LLM-as-judge", "Scores the output against a rubric with a judge model.", "paid", false},
	{types.LayerForType(types.TypePlugin), "Plugins", "Custom assertions that run in the SDK process; cost and determinism are the plugin's.", "free", false},
}

// assertionTypes lists every assertion type constant in pkg/types. A test
// compares it with the constants declared there.
var assertionTypes = []string{
	types.TypeSchema,
	types.TypeConstraint,
	types.TypeTrace,
	types.TypeContent,
	types.TypeEmbedding,
	types.TypeLLMJudge,
	types.TypeTraceTree,
	types.TypePlugin,
	types.TypeEffect,
}

type data struct {
	MaturityDefinitions map[string]string `json:"maturity_definitions"`
	Adapters            Adapters          `json:"adapters"`
	Features            []Feature         `json:"features"`
}

// Build assembles the capabilities document.
func Build() (Document, error) {
	var d data
	if err := json.Unmarshal(dataJSON, &d); err != nil {
		return Document{}, fmt.Errorf("capabilities data: %w", err)
	}
	layers := make([]Layer, 0, len(layerSpecs))
	for _, spec := range layerSpecs {
		members := []string{}
		for _, t := range assertionTypes {
			if types.LayerForType(t) == spec.id {
				members = append(members, t)
			}
		}
		layers = append(layers, Layer{
			ID:             spec.id,
			Name:           spec.name,
			Description:    spec.description,
			AssertionTypes: members,
			Cost:           spec.cost,
			Deterministic:  spec.deterministic,
		})
	}
	return Document{
		SchemaVersion:       SchemaVersion,
		EngineVersion:       buildinfo.Version,
		ProtocolVersion:     server.ProtocolVersion,
		License:             License,
		SDKLanguages:        []string{"python", "typescript"},
		Layers:              layers,
		Adapters:            d.Adapters,
		Features:            d.Features,
		MaturityDefinitions: d.MaturityDefinitions,
	}, nil
}
