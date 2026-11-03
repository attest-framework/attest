package report

import (
	"encoding/xml"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// JUnitTestSuites is the root element of a JUnit XML document.
type JUnitTestSuites struct {
	XMLName xml.Name         `xml:"testsuites"`
	Suites  []JUnitTestSuite `xml:"testsuite"`
}

// JUnitTestSuite groups the tests of one source file.
type JUnitTestSuite struct {
	Name     string          `xml:"name,attr"`
	Tests    int             `xml:"tests,attr"`
	Failures int             `xml:"failures,attr"`
	Errors   int             `xml:"errors,attr"`
	Skipped  int             `xml:"skipped,attr"`
	Time     string          `xml:"time,attr"`
	Cases    []JUnitTestCase `xml:"testcase"`
}

// JUnitTestCase is one test. file and line are the extended attributes GitHub
// and most CI viewers use to place a failure on its source line.
type JUnitTestCase struct {
	Name      string        `xml:"name,attr"`
	ClassName string        `xml:"classname,attr"`
	File      string        `xml:"file,attr,omitempty"`
	Line      int           `xml:"line,attr,omitempty"`
	Time      string        `xml:"time,attr"`
	Failure   *JUnitFailure `xml:"failure,omitempty"`
	Skipped   *JUnitSkipped `xml:"skipped,omitempty"`
}

// JUnitFailure is a failed test.
type JUnitFailure struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr,omitempty"`
	Content string `xml:",chardata"`
}

// JUnitSkipped is a test with no verdict (not_evaluated).
type JUnitSkipped struct {
	Message string `xml:"message,attr"`
}

// GenerateJUnitXML renders the tests of r as JUnit XML: one testsuite per
// source file in sorted order, one testcase per test in report order. A failed
// test carries a <failure> listing its failing assertions and replay misses; a
// skipped test (not_evaluated) carries a <skipped> naming the reasons.
func GenerateJUnitXML(r *CIReport) ([]byte, error) {
	rowsByTest := map[string][]FindingRow{}
	for _, row := range FindingRows(r) {
		rowsByTest[row.TestID] = append(rowsByTest[row.TestID], row)
	}

	suites := map[string]*JUnitTestSuite{}
	durations := map[string]int64{}
	var files []string
	for _, t := range r.Tests {
		suiteName := suiteKey(t.File)
		suite, ok := suites[suiteName]
		if !ok {
			suite = &JUnitTestSuite{Name: suiteName}
			suites[suiteName] = suite
			files = append(files, suiteName)
		}
		tc := JUnitTestCase{
			Name:      t.Name,
			ClassName: suiteName,
			File:      t.File,
			Line:      t.Line,
			Time:      formatDuration(t.DurationMS),
		}
		switch t.Outcome {
		case OutcomeFailed:
			tc.Failure = failureFor(rowsByTest[t.TestID])
			suite.Failures++
		case OutcomeSkipped:
			tc.Skipped = &JUnitSkipped{Message: skippedMessage(t.NotEvaluatedReasons)}
			suite.Skipped++
		}
		suite.Tests++
		durations[suiteName] += t.DurationMS
		suite.Cases = append(suite.Cases, tc)
	}

	sort.Strings(files)
	doc := JUnitTestSuites{Suites: make([]JUnitTestSuite, 0, len(files))}
	for _, f := range files {
		suite := suites[f]
		suite.Time = formatDuration(durations[f])
		doc.Suites = append(doc.Suites, *suite)
	}

	out, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal JUnit XML: %w", err)
	}
	return append([]byte(xml.Header), out...), nil
}

func suiteKey(file string) string {
	if file == "" {
		return "attest"
	}
	return file
}

func failureFor(rows []FindingRow) *JUnitFailure {
	if len(rows) == 0 {
		return &JUnitFailure{Message: "test failed"}
	}
	var lines []string
	for _, row := range rows {
		lines = append(lines, fmt.Sprintf("%s %s: %s", row.Type, row.AssertionID, row.Explanation))
	}
	return &JUnitFailure{
		Message: rows[0].Explanation,
		Type:    rows[0].Type,
		Content: strings.Join(lines, "\n"),
	}
}

func skippedMessage(reasons []string) string {
	if len(reasons) == 0 {
		return "not evaluated"
	}
	return "not evaluated: " + strings.Join(reasons, ", ")
}

// formatDuration converts milliseconds to seconds as a string for XML.
func formatDuration(ms int64) string {
	return strconv.FormatFloat(float64(ms)/1000.0, 'f', 3, 64)
}
