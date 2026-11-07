package main_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/attest-framework/attest/engine/internal/assertion"
	"github.com/attest-framework/attest/engine/internal/report/assertionid"
	"github.com/attest-framework/attest/engine/pkg/types"
)

type gateRow struct {
	ID, Scope, Type, Spec, Status string
}

// writeGateReport writes a v2 report. idScheme 0 omits the field, as a
// report written before id schemes did.
func writeGateReport(t *testing.T, dir, name string, idScheme int, budgetExceeded bool, rows []gateRow) string {
	t.Helper()
	results := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		results = append(results, map[string]any{
			"assertion_id": r.ID, "status": r.Status, "score": 1.0, "explanation": "",
			"cost": 0, "duration_ms": 1, "layer": 4, "type": r.Type,
			"scope_id": r.Scope, "spec_hash": r.Spec,
		})
	}
	env := map[string]any{
		"report_version": 2, "timestamp": "2026-11-05T00:00:00Z", "results": results,
		"tests":      []any{},
		"total_cost": 0, "total_duration_ms": 1, "budget_exceeded": budgetExceeded,
		"effect_world_excluded": true,
	}
	if idScheme != 0 {
		env["id_scheme"] = idScheme
	}
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func gateRows(n int) []gateRow {
	rows := make([]gateRow, n)
	for i := range rows {
		rows[i] = gateRow{ID: fmt.Sprintf("id-%02d", i), Scope: "t.py::test_a", Type: "content", Spec: fmt.Sprintf("spec-%02d", i), Status: "pass"}
	}
	return rows
}

func writeGatePolicy(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "attest.policy.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

type gateRun struct {
	stdout, stderr string
	code           int
	out            struct {
		Violations []struct{ Rule, Detail, Severity string } `json:"violations"`
		Changed    []struct {
			AssertionID    string `json:"assertion_id"`
			NewAssertionID string `json:"new_assertion_id"`
		} `json:"changed"`
	}
}

// gateEvaluate pins baseline on main, then evaluates head against it.
func gateEvaluate(t *testing.T, policyBody string, baseline, head func(dir string) string) gateRun {
	t.Helper()
	bin := buildEngineBinary(t)
	dir := t.TempDir()
	cache := t.TempDir()
	if _, stderr, code := runEngine(t, bin, cache, "baseline", "pin", "--tag", "main", "--report", baseline(dir)); code != 0 {
		t.Fatalf("baseline pin exit %d: %s", code, stderr)
	}
	var r gateRun
	r.stdout, r.stderr, r.code = runEngine(t, bin, cache, "policy", "evaluate",
		"--policy", writeGatePolicy(t, dir, policyBody), "--report", head(dir), "--baseline", "main")
	if r.code <= 2 {
		if err := json.Unmarshal([]byte(r.stdout), &r.out); err != nil {
			t.Fatalf("decode policy output %q: %v", r.stdout, err)
		}
	}
	return r
}

func gateBase(t *testing.T) func(string) string {
	return func(dir string) string { return writeGateReport(t, dir, "base.json", 2, false, gateRows(11)) }
}

func TestPolicyEvaluate_BaselineIDMissingFromHead_BlocksAndNamesIt(t *testing.T) {
	head := gateRows(11)[:10]

	r := gateEvaluate(t, "severity: block\n", gateBase(t), func(dir string) string { return writeGateReport(t, dir, "head.json", 2, false, head) })

	if r.code != 1 {
		t.Fatalf("exit = %d, want 1\nstdout: %s", r.code, r.stdout)
	}
	if len(r.out.Violations) != 1 || r.out.Violations[0].Rule != "block_on_removal" || !strings.Contains(r.out.Violations[0].Detail, "id-10") {
		t.Errorf("violations = %+v, want block_on_removal naming id-10", r.out.Violations)
	}
}

func TestPolicyEvaluate_AllowRemoved_Passes(t *testing.T) {
	head := gateRows(11)[:10]

	r := gateEvaluate(t, "allow_removed: [id-10]\n", gateBase(t), func(dir string) string { return writeGateReport(t, dir, "head.json", 2, false, head) })

	if r.code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s", r.code, r.stdout)
	}
}

func TestPolicyEvaluate_BlockOnRemovalFalse_Passes(t *testing.T) {
	head := gateRows(11)[:10]

	r := gateEvaluate(t, "block_on_removal: false\n", gateBase(t), func(dir string) string { return writeGateReport(t, dir, "head.json", 2, false, head) })

	if r.code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s", r.code, r.stdout)
	}
}

func TestPolicyEvaluate_ReorderedAssertions_Passes(t *testing.T) {
	head := gateRows(11)
	for i, j := 0, len(head)-1; i < j; i, j = i+1, j-1 {
		head[i], head[j] = head[j], head[i]
	}

	r := gateEvaluate(t, "severity: block\n", gateBase(t), func(dir string) string { return writeGateReport(t, dir, "head.json", 2, false, head) })

	if r.code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s", r.code, r.stdout)
	}
}

func TestPolicyEvaluate_SpecEdit_ReportedAsChangedNotRemoved(t *testing.T) {
	head := gateRows(11)
	head[3].ID, head[3].Spec = "id-03-edited", "spec-03-edited"

	r := gateEvaluate(t, "severity: block\n", gateBase(t), func(dir string) string { return writeGateReport(t, dir, "head.json", 2, false, head) })

	if r.code != 0 {
		t.Fatalf("exit = %d, want 0 (an edit is not a removal)\nstdout: %s", r.code, r.stdout)
	}
	if len(r.out.Changed) != 1 || r.out.Changed[0].AssertionID != "id-03" || r.out.Changed[0].NewAssertionID != "id-03-edited" {
		t.Errorf("changed = %+v, want id-03 -> id-03-edited", r.out.Changed)
	}
}

func TestPolicyEvaluate_SpecEditPlusDeletion_StillBlocksTheDeletion(t *testing.T) {
	head := gateRows(11)[:10]
	head[3].ID, head[3].Spec = "id-03-edited", "spec-03-edited"

	r := gateEvaluate(t, "severity: block\n", gateBase(t), func(dir string) string { return writeGateReport(t, dir, "head.json", 2, false, head) })

	if r.code != 1 || len(r.out.Violations) != 1 || !strings.Contains(r.out.Violations[0].Detail, "id-10") || strings.Contains(r.out.Violations[0].Detail, "id-03") {
		t.Fatalf("exit %d violations %+v, want a block naming only id-10", r.code, r.out.Violations)
	}
}

func TestPolicyEvaluate_SchemeOneBaseline_ExitsThreeWithRepinMessage(t *testing.T) {
	r := gateEvaluate(t, "severity: block\n",
		func(dir string) string { return writeGateReport(t, dir, "base.json", 0, false, gateRows(11)) },
		func(dir string) string { return writeGateReport(t, dir, "head.json", 2, false, gateRows(11)) })

	if r.code != 3 {
		t.Fatalf("exit = %d, want 3\nstderr: %s", r.code, r.stderr)
	}
	if !strings.Contains(r.stderr, "baseline uses id scheme 1; re-pin it on 1.1") {
		t.Errorf("stderr = %q, want the re-pin message", r.stderr)
	}
}

func TestPolicyEvaluate_BudgetExceededReport_BlocksAlongsideHardFail(t *testing.T) {
	head := gateRows(11)
	head[0].Status = "hard_fail"

	r := gateEvaluate(t, "severity: block\n", gateBase(t), func(dir string) string { return writeGateReport(t, dir, "head.json", 2, true, head) })

	if r.code != 1 {
		t.Fatalf("exit = %d, want 1\nstdout: %s", r.code, r.stdout)
	}
	found := false
	for _, v := range r.out.Violations {
		found = found || v.Rule == "budget_exceeded"
	}
	if !found {
		t.Errorf("violations = %+v, want budget_exceeded", r.out.Violations)
	}
}

func TestPolicyEvaluate_SchemeOneHeadReport_ExitsThreeWithRerunMessage(t *testing.T) {
	r := gateEvaluate(t, "severity: block\n", gateBase(t),
		func(dir string) string { return writeGateReport(t, dir, "head.json", 0, false, gateRows(11)) })

	if r.code != 3 {
		t.Fatalf("exit = %d, want 3\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stderr, "report uses id scheme 1; re-run with an SDK on 1.1") {
		t.Errorf("stderr = %q, want the re-run message", r.stderr)
	}
}

func TestPolicyEvaluate_EffectWorldChangeIsRegressionNotSpecChange(t *testing.T) {
	const scope = "refunds::test_count"
	var ids, hashes []string
	evaluate := func(name, records, wantStatus string) func(string) string {
		return func(dir string) string {
			spec := json.RawMessage(`{"check":"count","resource":"refunds","equals":1,"world":{"world_version":1,"definition_sha256":"` + strings.Repeat("0", 64) + `","state":{"refunds":` + records + `},"faults_fired":[],"faults_fired_count":0}}`)
			id, err := assertionid.ID(scope, types.TypeEffect, spec, 0)
			if err != nil {
				t.Fatal(err)
			}
			pipeline := assertion.NewPipeline(assertion.NewRegistry())
			batch, err := pipeline.EvaluateBatch(context.Background(), &types.Trace{TraceID: name},
				[]types.Assertion{{AssertionID: id, ScopeID: scope, Type: types.TypeEffect, Spec: spec}})
			if err != nil {
				t.Fatal(err)
			}
			if len(batch.Results) != 1 || batch.Results[0].Status != wantStatus {
				t.Fatalf("%s: results = %+v, want %s", name, batch.Results, wantStatus)
			}
			row := batch.Results[0]
			ids = append(ids, row.AssertionID)
			hashes = append(hashes, row.SpecHash)
			return writeGateReport(t, dir, name+".json", 2, false, []gateRow{{
				ID: row.AssertionID, Scope: row.ScopeID, Type: row.Type, Spec: row.SpecHash, Status: row.Status,
			}})
		}
	}
	r := gateEvaluate(t, "block_on_regression: true\n",
		evaluate("baseline", `[{"id":"r1"}]`, types.StatusPass),
		evaluate("head", `[{"id":"r1"},{"id":"r2"}]`, types.StatusHardFail))
	if ids[0] != ids[1] || hashes[0] != hashes[1] {
		t.Errorf("world changed identity: ids=%v spec_hashes=%v", ids, hashes)
	}
	if r.code != 1 || len(r.out.Violations) != 1 ||
		r.out.Violations[0].Rule != "block_on_regression" ||
		!strings.Contains(r.out.Violations[0].Detail, "regressions vs main: "+ids[0]) {
		t.Errorf("exit=%d output=%s, want block_on_regression naming stable id", r.code, r.stdout)
	}
	if len(r.out.Changed) != 0 {
		t.Errorf("runtime world change reported as spec edit: %+v", r.out.Changed)
	}
}

func withoutEffectIdentityMarker(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var report map[string]any
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	delete(report, "effect_world_excluded")
	raw, err = json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPolicyEvaluate_LegacyEffectIdentityFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, typ, hint            string
		legacyBaseline, legacyHead bool
		wantCode                   int
	}{
		{"legacy effect baseline", "effect", "baseline main holds effect assertions pinned before effect ids excluded the world snapshot; re-pin it from a report written by attest 1.1.0-rc.4 or later", true, false, 3},
		{"legacy effect head", "effect", "report holds effect assertions written before effect ids excluded the world snapshot; re-run it with attest 1.1.0-rc.4 or later", false, true, 3},
		{"both legacy effect", "effect", "re-pin it", true, true, 3},
		{"legacy non-effect baseline", "content", "", true, false, 0},
		{"legacy non-effect head", "content", "", false, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			baseRows := []gateRow{{ID: "old-id", Scope: "effect::test", Type: tc.typ, Spec: "old-hash", Status: types.StatusPass}}
			headRows := []gateRow{{ID: "new-id", Scope: "effect::test", Type: tc.typ, Spec: "new-hash", Status: types.StatusHardFail}}
			r := gateEvaluate(t, "block_on_regression: true\n",
				func(dir string) string {
					path := writeGateReport(t, dir, "baseline.json", 2, false, baseRows)
					if tc.legacyBaseline {
						path = withoutEffectIdentityMarker(t, path)
					}
					return path
				}, func(dir string) string {
					path := writeGateReport(t, dir, "head.json", 2, false, headRows)
					if tc.legacyHead {
						path = withoutEffectIdentityMarker(t, path)
					}
					return path
				})
			if r.code != tc.wantCode || !strings.Contains(r.stderr, tc.hint) {
				t.Fatalf("exit %d, want %d; stdout=%s stderr=%s", r.code, tc.wantCode, r.stdout, r.stderr)
			}
		})
	}
}
