package trace

import (
	"strings"

	"github.com/attest-ai/attest/engine/pkg/types"
)

const defaultSchemaVersion = 1

// Normalize trims whitespace from TraceID and defaults SchemaVersion to 1 if 0.
func Normalize(t *types.Trace) {
	t.TraceID = strings.TrimSpace(t.TraceID)
	if t.SchemaVersion == 0 {
		t.SchemaVersion = defaultSchemaVersion
	}
}
