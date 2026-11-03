package mcp

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/attest-ai/attest/engine/internal/capture"
	"github.com/attest-ai/attest/engine/internal/contractdsl"
	"github.com/attest-ai/attest/engine/internal/trace"
	"github.com/attest-ai/attest/engine/internal/world"
	"github.com/attest-ai/attest/engine/pkg/types"
)

// source is the captured run a tool works on.
type source struct {
	name    string
	trace   *types.Trace
	fixture *capture.Fixture // nil when the source is an OTLP export or inline
	check   capture.TraceCheckResult
}

func methodList() string {
	s := ""
	for i, m := range contractdsl.Methods() {
		if i > 0 {
			s += ", "
		}
		s += m.Name
	}
	return s
}

func oneOf(fixturePath, tracePath string) (string, error) {
	switch {
	case fixturePath != "" && tracePath != "":
		return "", errors.New("give exactly one of fixture_path and trace_path")
	case fixturePath != "":
		return fixturePath, nil
	case tracePath != "":
		return tracePath, nil
	}
	return "", errors.New("give one of fixture_path and trace_path")
}

func checkResult(r *trace.CheckReport) capture.TraceCheckResult {
	errs := len(r.Errors())
	return capture.TraceCheckResult{OK: r.OK, Errors: errs, Warnings: len(r.Issues) - errs}
}

// loadSource reads a fixture or an OTLP export inside the root. A trace_path
// may also name a fixture; a fixture_path must.
func (e *env) loadSource(fixturePath, tracePath string) (*source, error) {
	path, err := oneOf(fixturePath, tracePath)
	if err != nil {
		return nil, err
	}
	data, err := e.root.ReadFile(path, MaxFileBytes)
	if err != nil {
		return nil, err
	}
	if capture.IsFixture(data) {
		fx, err := capture.Unmarshal(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		report, _, err := capture.CheckData(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		return &source{name: path, trace: fx.Trace, fixture: fx, check: checkResult(report)}, nil
	}
	if fixturePath != "" {
		return nil, fmt.Errorf("%s is not a capture fixture", path)
	}
	spans, err := trace.ParseOTLP(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	t, err := trace.FromSpans(spans, trace.ConvertOptions{})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &source{name: path, trace: t, check: checkResult(trace.Check(spans))}, nil
}

// prepare normalizes and validates t for evaluation, as the engine does for
// every evaluate_batch request.
func prepare(t *types.Trace) error {
	trace.Normalize(t)
	data, err := json.Marshal(t)
	if err != nil {
		return errors.New("trace could not be serialized")
	}
	if rpcErr := trace.Validate(t, len(data)); rpcErr != nil {
		return fmt.Errorf("invalid trace: %s", rpcErr.Message)
	}
	return nil
}

// worldSnapshot reads the world a statement's effect checks run against: the
// world_path file (a world-v1 snapshot, or a fixture whose world is used), else
// the fixture's own. The result is nil without a world.
func (e *env) worldSnapshot(worldPath string, src *source) (json.RawMessage, error) {
	if worldPath == "" {
		if src.fixture != nil && len(src.fixture.World) > 0 && string(src.fixture.World) != "null" {
			return src.fixture.World, nil
		}
		return nil, nil
	}
	data, err := e.root.ReadFile(worldPath, MaxFileBytes)
	if err != nil {
		return nil, err
	}
	snap := json.RawMessage(data)
	if capture.IsFixture(data) {
		fx, err := capture.Unmarshal(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", worldPath, err)
		}
		if len(fx.World) == 0 || string(fx.World) == "null" {
			return nil, fmt.Errorf("%s holds no world", worldPath)
		}
		snap = fx.World
	}
	if err := world.ValidateSnapshot(snap); err != nil {
		return nil, fmt.Errorf("%s is not a world-v1 snapshot: %w", worldPath, err)
	}
	return snap, nil
}
