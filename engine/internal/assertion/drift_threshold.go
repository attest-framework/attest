package assertion

import (
	"bytes"
	"fmt"

	"github.com/segmentio/encoding/json"
)

// dynamicThreshold is the spec value that selects a history-derived pass bar.
const dynamicThreshold = "dynamic"

// thresholdSpec is a spec's "threshold": a number (static bar) or the string
// "dynamic". For "dynamic" Value stays 0, so each evaluator applies its default
// static threshold, which the dynamic bar never falls below.
type thresholdSpec struct {
	Value   float64
	Dynamic bool
}

// UnmarshalJSON accepts a JSON number, the string "dynamic", or null.
func (t *thresholdSpec) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("null")) {
		return nil
	}
	if len(data) > 0 && data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		if s != dynamicThreshold {
			return fmt.Errorf("threshold %q is not a number or %q", s, dynamicThreshold)
		}
		t.Value, t.Dynamic = 0, true
		return nil
	}
	var v float64
	if err := json.Unmarshal(data, &v); err != nil {
		return fmt.Errorf("threshold must be a number or %q: %w", dynamicThreshold, err)
	}
	t.Value, t.Dynamic = v, false
	return nil
}
