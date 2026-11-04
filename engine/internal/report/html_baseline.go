package report

import (
	"fmt"
	"sort"
	"strings"

	"github.com/attest-ai/attest/engine/pkg/types"
)

// baseline compares head with base report to report: pass rate, cost, latency
// and, per test, the outcome and the tool-call sequence read from the
// embedded traces.
func (b *viewBuilder) baseline(base, head *CIReport) *BaselineView {
	bv := &BaselineView{NoTraces: len(base.Traces) == 0 || len(head.Traces) == 0}
	bs, hs := base.Summary, head.Summary
	bv.Rows = []DiffRow{
		{Metric: "assertion pass rate", Base: passRate(bs), Head: passRate(hs), Delta: rateDelta(bs, hs), Class: rateClass(bs, hs)},
		{Metric: "assertions failed", Base: fmt.Sprint(bs.HardFail + bs.SoftFail), Head: fmt.Sprint(hs.HardFail + hs.SoftFail),
			Delta: signedInt(int64(hs.HardFail+hs.SoftFail) - int64(bs.HardFail+bs.SoftFail)), Class: lowerIsBetter(float64(hs.HardFail+hs.SoftFail) - float64(bs.HardFail+bs.SoftFail))},
		{Metric: "not evaluated", Base: fmt.Sprint(bs.NotEvaluated), Head: fmt.Sprint(hs.NotEvaluated),
			Delta: signedInt(int64(hs.NotEvaluated) - int64(bs.NotEvaluated)), Class: lowerIsBetter(float64(hs.NotEvaluated) - float64(bs.NotEvaluated))},
		{Metric: "cost", Base: usd(base.TotalCost), Head: usd(head.TotalCost),
			Delta: fmt.Sprintf("%+.4f USD", head.TotalCost-base.TotalCost), Class: lowerIsBetter(head.TotalCost - base.TotalCost)},
		{Metric: "total latency", Base: msText(base.TotalDurationMS), Head: msText(head.TotalDurationMS),
			Delta: signedInt(head.TotalDurationMS-base.TotalDurationMS) + " ms", Class: lowerIsBetter(float64(head.TotalDurationMS - base.TotalDurationMS))},
	}
	for _, p := range []struct {
		name string
		q    int
	}{{"test latency p50", 50}, {"test latency p95", 95}} {
		bp, hp := percentile(sortedDurations(base), p.q), percentile(sortedDurations(head), p.q)
		bv.Rows = append(bv.Rows, DiffRow{Metric: p.name, Base: msText(bp), Head: msText(hp),
			Delta: signedInt(hp-bp) + " ms", Class: lowerIsBetter(float64(hp - bp))})
	}
	bv.Tests = b.testDiffs(base, head)
	return bv
}

func rateOf(s CISummary) (float64, bool) {
	den := s.Passed + s.SoftFail + s.HardFail
	if den == 0 {
		return 0, false
	}
	return 100 * float64(s.Passed) / float64(den), true
}

func rateDelta(base, head CISummary) string {
	br, bok := rateOf(base)
	hr, hok := rateOf(head)
	if !bok || !hok {
		return "n/a"
	}
	return fmt.Sprintf("%+.1f pp", hr-br)
}

func rateClass(base, head CISummary) string {
	br, bok := rateOf(base)
	hr, hok := rateOf(head)
	switch {
	case !bok || !hok || hr == br:
		return ""
	case hr < br:
		return classFail
	default:
		return classPass
	}
}

// lowerIsBetter classes a delta where an increase is a regression.
func lowerIsBetter(delta float64) string {
	switch {
	case delta > 0:
		return classFail
	case delta < 0:
		return classPass
	default:
		return ""
	}
}

func signedInt(n int64) string { return fmt.Sprintf("%+d", n) }

func msText(ms int64) string { return fmt.Sprintf("%d ms", ms) }

func testDurations(r *CIReport) []int64 {
	out := make([]int64, 0, len(r.Tests))
	for _, t := range r.Tests {
		out = append(out, t.DurationMS)
	}
	return out
}

// sortedDurations returns the test durations of r in ascending order, the
// input percentile expects.
func sortedDurations(r *CIReport) []int64 {
	out := testDurations(r)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// testSnapshot is what the diff keeps of a test.
type testSnapshot struct {
	name, location, label string
	class                 string
	tools                 []string
	hasTools              bool
}

// snapshots indexes the tests of r by test id; the first test with an id wins.
func snapshots(r *CIReport) (map[string]testSnapshot, []string) {
	skips := notEvaluatedSkips(r)
	traceIDs := map[testKey][]string{}
	seen := map[testKey]map[string]bool{}
	for _, res := range r.Results {
		if res.TraceID == "" {
			continue
		}
		k := testKey{res.src, res.TestID}
		if seen[k] == nil {
			seen[k] = map[string]bool{}
		}
		if !seen[k][res.TraceID] {
			seen[k][res.TraceID] = true
			traceIDs[k] = append(traceIDs[k], res.TraceID)
		}
	}
	dec := &traceDecoder{raw: r.Traces, decoded: map[string]*types.Trace{}}
	out := map[string]testSnapshot{}
	var order []string
	for _, t := range r.Tests {
		if _, dup := out[t.TestID]; dup {
			continue
		}
		class, label := classify(t, skips)
		snap := testSnapshot{name: t.Name, location: fmt.Sprintf("%s:%d", t.File, t.Line), class: class, label: label}
		for _, id := range traceIDs[testKey{t.src, t.TestID}] {
			if tr := dec.get(id); tr != nil {
				snap.tools = append(snap.tools, toolSequence(tr, viewMaxSequence)...)
				snap.hasTools = true
			}
		}
		out[t.TestID] = snap
		order = append(order, t.TestID)
	}
	return out, order
}

func (b *viewBuilder) testDiffs(base, head *CIReport) []TestDiff {
	bs, _ := snapshots(base)
	hs, order := snapshots(head)
	var diffs []TestDiff
	for _, id := range order {
		h := hs[id]
		bt, ok := bs[id]
		d := TestDiff{Name: b.text(h.name), Location: b.text(h.location), Head: b.text(h.label)}
		switch {
		case !ok:
			d.Base, d.Note = "absent", "test is new"
		case bt.label != h.label || (bt.hasTools && h.hasTools && !equalStrings(bt.tools, h.tools)):
			d.Base = b.text(bt.label)
			if bt.label != h.label {
				d.Note = "outcome changed"
			}
			if bt.hasTools && h.hasTools && !equalStrings(bt.tools, h.tools) {
				d.Sequence = diffSequence(bt.tools, h.tools)
				for i := range d.Sequence {
					d.Sequence[i].Name = b.text(d.Sequence[i].Name)
				}
				d.Note = strings.TrimSpace(d.Note + " tool sequence changed")
			}
		default:
			continue
		}
		diffs = append(diffs, d)
	}
	var removed []string
	for id := range bs {
		if _, ok := hs[id]; !ok {
			removed = append(removed, id)
		}
	}
	sort.Strings(removed)
	for _, id := range removed {
		t := bs[id]
		diffs = append(diffs, TestDiff{Name: b.text(t.name), Location: b.text(t.location), Base: b.text(t.label), Head: "absent", Note: "test was removed"})
	}
	return diffs
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// diffSequence is the longest-common-subsequence diff of two tool-call
// sequences: calls only in head are "add", calls only in base are "del".
func diffSequence(base, head []string) []SeqOp {
	n, m := len(base), len(head)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if base[i] == head[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var ops []SeqOp
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case base[i] == head[j]:
			ops = append(ops, SeqOp{Kind: "keep", Name: base[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, SeqOp{Kind: "del", Name: base[i]})
			i++
		default:
			ops = append(ops, SeqOp{Kind: "add", Name: head[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, SeqOp{Kind: "del", Name: base[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, SeqOp{Kind: "add", Name: head[j]})
	}
	return ops
}
