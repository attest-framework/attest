package report

import (
	"encoding/json"
	"fmt"
)

// CheckComplete rejects a report that recorded no tests although the run
// collected some: a worker or reporter that dropped its results would
// otherwise read as a clean run. A report without tests_collected has unknown
// collection and is accepted.
func CheckComplete(data []byte) error {
	var doc struct {
		Tests          []json.RawMessage `json:"tests"`
		TestsCollected *int              `json:"tests_collected"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("decode report completeness: %w", err)
	}
	if doc.TestsCollected != nil && *doc.TestsCollected > 0 && len(doc.Tests) == 0 {
		return fmt.Errorf("the report has 0 tests but tests_collected is %d: the run's results were lost", *doc.TestsCollected)
	}
	return nil
}
