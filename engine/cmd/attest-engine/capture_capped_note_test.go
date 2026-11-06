package main

import (
	"strings"
	"testing"

	"github.com/attest-framework/attest/engine/internal/capture"
)

func TestRelationalCappedNote_ReportsEachCutAndNothingWhenNoneWas(t *testing.T) {
	if got := relationalCappedNote(capture.BuildReport{}); got != "" {
		t.Errorf("note %q with nothing cut", got)
	}
	trimmed := relationalCappedNote(capture.BuildReport{RelationalTrimmed: true, RelationalKept: 7})
	if !strings.Contains(trimmed, "too large") || !strings.Contains(trimmed, "first 7") {
		t.Errorf("trim note %q lacks the reason or the kept count", trimmed)
	}
	both := relationalCappedNote(capture.BuildReport{RelationalCapped: true, RelationalTrimmed: true, RelationalKept: 7})
	if !strings.Contains(both, "first 50") || !strings.Contains(both, "first 7") {
		t.Errorf("combined note %q lacks a cut", both)
	}
}
