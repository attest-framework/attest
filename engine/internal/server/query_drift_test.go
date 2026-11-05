package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/attest-framework/attest/engine/internal/cache"
	"github.com/attest-framework/attest/engine/pkg/types"
)

func queryDriftFor(t *testing.T, store *cache.HistoryStore, id string) types.DriftReport {
	t.Helper()
	session := NewSession()
	session.SetState(StateInitialized)
	params, err := json.Marshal(types.QueryDriftParams{AssertionID: id, WindowSize: 3})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	res, rpcErr := handleQueryDrift(store)(context.Background(), session, params)
	if rpcErr != nil {
		t.Fatalf("query_drift: %+v", rpcErr)
	}
	return res.(*types.QueryDriftResult).Report
}

func newQueryDriftStore(t *testing.T) *cache.HistoryStore {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := cache.NewHistoryStore(db)
	if err != nil {
		t.Fatalf("NewHistoryStore: %v", err)
	}
	return store
}

func recordRuns(t *testing.T, store *cache.HistoryStore, id string, n int, score float64, status string) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := store.Record(fmt.Sprintf("trc-%s-%v-%d", id, score, i), id, types.TypeLLMJudge, score, status); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
}

func TestQueryDrift_FailuresStayOutOfTheBaseline(t *testing.T) {
	// Arrange: ten passing runs at 0.9, then a run of failures at 0.5.
	store := newQueryDriftStore(t)
	recordRuns(t, store, "judge-a", 10, 0.9, types.StatusPass)
	recordRuns(t, store, "judge-a", 5, 0.5, types.StatusHardFail)

	// Act
	report := queryDriftFor(t, store, "judge-a")

	// Assert
	if report.Status != "drift_detected" || report.Count != 10 || report.Mean < 0.899 || report.Mean > 0.901 || report.LatestScore != 0.5 {
		t.Errorf("report = %+v, want drift_detected over a 10-run baseline of mean 0.9 and latest 0.5", report)
	}
}

func TestQueryDrift_LatestRunIsNotPartOfItsOwnBaseline(t *testing.T) {
	// Arrange: ten runs at 1.0 then 0.99, which is within the sigma floor.
	store := newQueryDriftStore(t)
	recordRuns(t, store, "judge-b", 10, 1.0, types.StatusPass)
	recordRuns(t, store, "judge-b", 1, 0.99, types.StatusPass)

	// Act
	report := queryDriftFor(t, store, "judge-b")

	// Assert
	if report.Status != "ok" || report.Count != 10 {
		t.Errorf("report = %+v, want ok over a 10-run baseline", report)
	}
}

func TestQueryDrift_AllZeroHistory_LatestZeroDriftsAgainstStaticBar(t *testing.T) {
	// Arrange
	store := newQueryDriftStore(t)
	recordRuns(t, store, "judge-c", 12, 0.0, types.StatusPass)

	// Act
	report := queryDriftFor(t, store, "judge-c")

	// Assert
	if report.Status != "drift_detected" {
		t.Errorf("status = %q, want drift_detected: 0.0 is below the static 0.8 bar", report.Status)
	}
}

func TestQueryDrift_TooFewBaselineRuns_ReportsOK(t *testing.T) {
	// Arrange
	store := newQueryDriftStore(t)
	recordRuns(t, store, "judge-d", 3, 0.9, types.StatusPass)
	recordRuns(t, store, "judge-d", 1, 0.1, types.StatusHardFail)

	// Act
	report := queryDriftFor(t, store, "judge-d")

	// Assert
	if report.Status != "ok" || report.Count != 3 {
		t.Errorf("report = %+v, want ok with 3 baseline runs", report)
	}
}

func queryDriftRaw(t *testing.T, store *cache.HistoryStore, params string) (*types.QueryDriftResult, *types.RPCError) {
	t.Helper()
	session := NewSession()
	session.SetState(StateInitialized)
	res, rpcErr := handleQueryDrift(store)(context.Background(), session, json.RawMessage(params))
	if rpcErr != nil {
		return nil, rpcErr
	}
	return res.(*types.QueryDriftResult), nil
}

func TestQueryDrift_StaticThresholdBelowDefault_LatestAtStaticBarIsOK(t *testing.T) {
	// Arrange: static 0.6, ten runs at 0.7, then 0.7 again.
	store := newQueryDriftStore(t)
	recordRuns(t, store, "judge-e", 11, 0.7, types.StatusPass)

	// Act
	res, rpcErr := queryDriftRaw(t, store, `{"assertion_id":"judge-e","static_threshold":0.6}`)

	// Assert
	if rpcErr != nil {
		t.Fatalf("query_drift: %+v", rpcErr)
	}
	if res.Report.Status != "ok" || res.StaticThreshold != 0.6 {
		t.Errorf("result = %+v, want ok with static_threshold 0.6", res)
	}
}

func TestQueryDrift_StaticThresholdAboveDefault_DropBelowStaticDrifts(t *testing.T) {
	// Arrange: static 0.95, ten runs at 0.95, then 0.92 (below the static bar).
	store := newQueryDriftStore(t)
	recordRuns(t, store, "judge-f", 10, 0.95, types.StatusPass)
	recordRuns(t, store, "judge-f", 1, 0.92, types.StatusHardFail)

	// Act
	res, rpcErr := queryDriftRaw(t, store, `{"assertion_id":"judge-f","static_threshold":0.95}`)

	// Assert
	if rpcErr != nil {
		t.Fatalf("query_drift: %+v", rpcErr)
	}
	if res.Report.Status != "drift_detected" {
		t.Errorf("status = %q, want drift_detected: 0.92 is under the static 0.95 bar", res.Report.Status)
	}
}

func TestQueryDrift_StaticThresholdOmitted_UsesDefault(t *testing.T) {
	// Arrange: ten runs at 0.9, then 0.75 (below the 0.8 default and the baseline).
	store := newQueryDriftStore(t)
	recordRuns(t, store, "judge-g", 10, 0.9, types.StatusPass)
	recordRuns(t, store, "judge-g", 1, 0.75, types.StatusPass)

	// Act
	res, rpcErr := queryDriftRaw(t, store, `{"assertion_id":"judge-g"}`)

	// Assert
	if rpcErr != nil {
		t.Fatalf("query_drift: %+v", rpcErr)
	}
	if res.StaticThreshold != 0.8 || res.Report.Status != "drift_detected" {
		t.Errorf("result = %+v, want static_threshold 0.8 and drift_detected", res)
	}
}

func TestQueryDrift_StaticThresholdZero_IsDistinctFromOmitted(t *testing.T) {
	// Arrange: ten runs at 0.1, then 0.1; floor 0 must not be replaced by 0.8.
	store := newQueryDriftStore(t)
	recordRuns(t, store, "judge-h", 11, 0.1, types.StatusPass)

	// Act
	res, rpcErr := queryDriftRaw(t, store, `{"assertion_id":"judge-h","static_threshold":0}`)

	// Assert
	if rpcErr != nil {
		t.Fatalf("query_drift: %+v", rpcErr)
	}
	if res.StaticThreshold != 0 || res.Report.Status != "ok" {
		t.Errorf("result = %+v, want static_threshold 0 and ok", res)
	}
}

func TestQueryDrift_InvalidStaticThreshold_IsRejected(t *testing.T) {
	store := newQueryDriftStore(t)
	for _, raw := range []string{"-0.1", "1.5", `"0.8"`, "null", "true"} {
		// Act
		_, rpcErr := queryDriftRaw(t, store, `{"assertion_id":"x","static_threshold":`+raw+`}`)

		// Assert
		if raw == "null" {
			if rpcErr != nil {
				t.Errorf("static_threshold null: unexpected error %+v (null means omitted)", rpcErr)
			}
			continue
		}
		if rpcErr == nil || rpcErr.Code != types.ErrAssertionError {
			t.Errorf("static_threshold %s: rpcErr = %+v, want ASSERTION_ERROR", raw, rpcErr)
		}
	}
}
