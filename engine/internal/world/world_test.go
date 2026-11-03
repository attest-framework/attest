package world

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	protoSchema = "../../../proto/attest/v1/world-v1.schema.json"
	fixtureDir  = "../../../protocol-tests/world-v1"
)

func loadPayments(t *testing.T) *Definition {
	t.Helper()
	d, err := LoadFile("testdata/payments.world.yaml")
	if err != nil {
		t.Fatalf("load payments world: %v", err)
	}
	return d
}

func start(t *testing.T, d *Definition) (*World, *httptest.Server) {
	t.Helper()
	w, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(w.Handler(""))
	t.Cleanup(func() { w.Release(); ts.Close() })
	return w, ts
}

func post(t *testing.T, c *http.Client, url, key, body string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestSchemaCopyMatchesProto(t *testing.T) {
	want, err := os.ReadFile(protoSchema)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, schemaJSON) {
		t.Fatal("internal/world/world-v1.schema.json differs from proto/attest/v1/world-v1.schema.json; copy the proto file over it")
	}
}

func TestDefinitionFixtures(t *testing.T) {
	for _, tc := range []struct {
		dir   string
		valid bool
	}{{"valid", true}, {"invalid", false}} {
		files, _ := filepath.Glob(filepath.Join(fixtureDir, tc.dir, "*.json"))
		if len(files) == 0 {
			t.Fatalf("no %s fixtures", tc.dir)
		}
		for _, f := range files {
			t.Run(tc.dir+"/"+filepath.Base(f), func(t *testing.T) {
				data, _ := os.ReadFile(f)
				_, err := Parse(data)
				if tc.valid && err != nil {
					t.Fatalf("rejected: %v", err)
				}
				if !tc.valid && err == nil {
					t.Fatal("accepted an invalid definition")
				}
			})
		}
	}
}

func TestSnapshotFixtures(t *testing.T) {
	for _, tc := range []struct {
		dir   string
		valid bool
	}{{"snapshot-valid", true}, {"snapshot-invalid", false}} {
		files, _ := filepath.Glob(filepath.Join(fixtureDir, tc.dir, "*.json"))
		if len(files) == 0 {
			t.Fatalf("no %s fixtures", tc.dir)
		}
		for _, f := range files {
			t.Run(tc.dir+"/"+filepath.Base(f), func(t *testing.T) {
				data, _ := os.ReadFile(f)
				err := ValidateSnapshot(data)
				if tc.valid && err != nil {
					t.Fatalf("rejected: %v", err)
				}
				if !tc.valid && err == nil {
					t.Fatal("accepted an invalid snapshot")
				}
			})
		}
	}
}

func TestParseMalformedNamesThePath(t *testing.T) {
	cases := map[string]struct{ yaml, wantPath string }{
		"unknown route": {"world_version: 1\nbase_url_env: X_URL\nresources:\n  things: {path: /things, routes: [create, delete]}\n", "/resources/things/routes/1"},
		"missing env":   {"world_version: 1\nresources:\n  things: {path: /things, routes: [create]}\n", ""},
		"bad fault":     {"world_version: 1\nbase_url_env: X_URL\nresources:\n  things: {path: /things, routes: [create]}\nfaults:\n  - {resource: things, route: create, fault: 503, on_requests: [1]}\n", "/faults/0/fault"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), "world definition:") || !strings.Contains(err.Error(), tc.wantPath) {
				t.Fatalf("error %q does not name %q", err, tc.wantPath)
			}
		})
	}
}

func TestParseRejectsMalformedYAML(t *testing.T) {
	for name, src := range map[string]string{
		"empty":          "",
		"syntax":         "world_version: [1\n",
		"duplicate key":  "world_version: 1\nworld_version: 1\n",
		"two documents":  "world_version: 1\n---\nworld_version: 1\n",
		"unquoted date":  "world_version: 1\nbase_url_env: X_URL\nresources:\n  t: {path: /t, routes: [create]}\nfaults: []\nseed: 2024-01-01\n",
		"non-string key": "1: a\n",
		"not a mapping":  "- a\n- b\n",
		"wrong type":     "world_version: one\nbase_url_env: X_URL\nresources: {}\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(src)); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestParseCrossFieldRules(t *testing.T) {
	base := "world_version: 1\nbase_url_env: X_URL\n"
	for name, tc := range map[string]struct{ src, want string }{
		"fault on unknown resource":  {base + "resources:\n  a: {path: /a, routes: [create]}\nfaults:\n  - {resource: b, route: create, fault: 500, on_requests: [1]}\n", "/faults/0/resource"},
		"fault on undeclared route":  {base + "resources:\n  a: {path: /a, routes: [create]}\nfaults:\n  - {resource: a, route: list, fault: 500, on_requests: [1]}\n", "/faults/0/route"},
		"duplicate path":             {base + "resources:\n  a: {path: /x, routes: [create]}\n  b: {path: /x, routes: [create]}\n", "/resources/b/path"},
		"seed without id":            {base + "resources:\n  a: {path: /a, routes: [get], seed: [{n: 1}]}\n", "/resources/a/seed/0"},
		"duplicate seed id":          {base + "resources:\n  a: {path: /a, routes: [get], seed: [{id: x}, {id: x}]}\n", "/resources/a/seed/1/id"},
		"idempotency without create": {base + "resources:\n  a: {path: /a, routes: [get], idempotency: {header: K, field: k}}\n", "/resources/a/idempotency"},
		"filters without list":       {base + "resources:\n  a: {path: /a, routes: [create], filters: [q]}\n", "/resources/a/filters"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(tc.src))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error naming %s, got %v", tc.want, err)
			}
		})
	}
}

func TestParseHashIsStableAcrossFormatting(t *testing.T) {
	a, err := Parse([]byte("world_version: 1\nbase_url_env: X_URL\nresources:\n  t: {path: /t, routes: [create]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Parse([]byte(`{"resources":{"t":{"routes":["create"],"path":"/t"}},"base_url_env":"X_URL","world_version":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if a.Hash != b.Hash {
		t.Fatalf("hashes differ: %s vs %s", a.Hash, b.Hash)
	}
}

func TestServerRoutesSeedGetListCreate(t *testing.T) {
	d := loadPayments(t)
	d.Faults = nil
	_, ts := start(t, d)
	c := ts.Client()

	resp, err := c.Get(ts.URL + "/orders/O-1042")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("get seeded order: %v %v", err, resp)
	}
	resp.Body.Close()
	if resp, _ = c.Get(ts.URL + "/orders/O-9"); resp.StatusCode != 404 {
		t.Fatalf("missing order: %d", resp.StatusCode)
	}
	if resp, _ = c.Get(ts.URL + "/orders"); resp.StatusCode != 404 {
		t.Fatalf("orders declares get only, list answered %d", resp.StatusCode)
	}

	r, body := post(t, c, ts.URL+"/refunds", "", `{"order_id":"O-1042","amount_cents":4999}`)
	if r.StatusCode != 201 || !strings.Contains(body, `"refund_id":"R-0001"`) {
		t.Fatalf("create: %d %s", r.StatusCode, body)
	}
	post(t, c, ts.URL+"/refunds", "", `{"order_id":"O-2001","amount_cents":1}`)

	resp, _ = c.Get(ts.URL + "/refunds?order_id=O-1042")
	var listed struct{ Refunds []map[string]any }
	json.NewDecoder(resp.Body).Decode(&listed)
	resp.Body.Close()
	if len(listed.Refunds) != 1 || listed.Refunds[0]["refund_id"] != "R-0001" {
		t.Fatalf("filtered list: %v", listed.Refunds)
	}
	if resp, _ = c.Get(ts.URL + "/refunds?bogus=1"); resp.StatusCode != 400 {
		t.Fatalf("unknown filter: %d", resp.StatusCode)
	}
	if resp, _ = c.Get(ts.URL + "/refunds/R-0002"); resp.StatusCode != 200 {
		t.Fatalf("get created: %d", resp.StatusCode)
	}
	if resp, _ = c.Get(ts.URL + "/notifications"); resp.StatusCode != 404 {
		t.Fatalf("create-only resource listed: %d", resp.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/refunds", nil)
	if resp, _ = c.Do(req); resp.StatusCode != 405 {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
}

func TestServerRejectsBadCreates(t *testing.T) {
	d := loadPayments(t)
	d.Faults = nil
	_, ts := start(t, d)
	for name, body := range map[string]string{
		"not json":    `nope`,
		"array":       `[1]`,
		"null":        `null`,
		"trailing":    `{} {}`,
		"id supplied": `{"refund_id":"R-9"}`,
		"key in body": `{"idempotency_key":"k"}`,
	} {
		if r, _ := post(t, ts.Client(), ts.URL+"/refunds", "", body); r.StatusCode != 400 {
			t.Errorf("%s: status %d, want 400", name, r.StatusCode)
		}
	}
	big := `{"x":"` + strings.Repeat("a", MaxBodyBytes) + `"}`
	if r, _ := post(t, ts.Client(), ts.URL+"/refunds", "", big); r.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("oversize body: %d", r.StatusCode)
	}
}

func TestIdempotencyKeyReplaysAndRejectsReuse(t *testing.T) {
	d := loadPayments(t)
	d.Faults = nil
	w, ts := start(t, d)
	c := ts.Client()
	body := `{"order_id":"O-1042","amount_cents":4999}`
	r1, b1 := post(t, c, ts.URL+"/refunds", "K1", body)
	r2, b2 := post(t, c, ts.URL+"/refunds", "K1", body)
	if r1.StatusCode != 201 || r2.StatusCode != 200 || b1 != b2 || r2.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay: %d %q / %d %q", r1.StatusCode, b1, r2.StatusCode, b2)
	}
	if r3, _ := post(t, c, ts.URL+"/refunds", "K1", `{"order_id":"O-2001","amount_cents":1}`); r3.StatusCode != 422 {
		t.Fatalf("key reuse with another body: %d", r3.StatusCode)
	}
	if n := len(w.Snapshot().State["refunds"]); n != 1 {
		t.Fatalf("%d refunds after a replayed key, want 1", n)
	}
}

func TestTimeoutAfterCommitCommitsAndHoldsUntilClientGivesUp(t *testing.T) {
	_, ts := start(t, loadPayments(t))
	c := &http.Client{Timeout: 300 * time.Millisecond}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/refunds", strings.NewReader(`{"order_id":"O-1042","amount_cents":4999}`))
	if _, err := c.Do(req); err == nil {
		t.Fatal("first refund answered; the fault holds its response")
	}
	resp, err := ts.Client().Get(ts.URL + "/refunds?order_id=O-1042")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var listed struct{ Refunds []map[string]any }
	json.NewDecoder(resp.Body).Decode(&listed)
	if len(listed.Refunds) != 1 {
		t.Fatalf("%d refunds after the timed-out create, want 1 committed", len(listed.Refunds))
	}
}

func TestTimeoutAfterCommitGivesUpAtTheCap(t *testing.T) {
	w, ts := start(t, loadPayments(t))
	w.holdCap = 100 * time.Millisecond
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/refunds", strings.NewReader(`{"order_id":"O-1042"}`))
	start := time.Now()
	if _, err := ts.Client().Do(req); err == nil {
		t.Fatal("response arrived; the connection should be dropped at the cap")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("hold ignored its cap")
	}
}

func TestIncidentDoublePostWithoutKeyCommitsTwoRefunds(t *testing.T) {
	w, ts := start(t, loadPayments(t))
	body := `{"order_id":"O-1042","amount_cents":4999}`
	short := &http.Client{Timeout: 200 * time.Millisecond}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/refunds", strings.NewReader(body))
	if _, err := short.Do(req); err == nil {
		t.Fatal("timeout fault did not fire")
	}
	if r, _ := post(t, ts.Client(), ts.URL+"/refunds", "", body); r.StatusCode != 201 {
		t.Fatalf("retry: %d", r.StatusCode)
	}
	snap := w.Snapshot()
	if n := len(snap.State["refunds"]); n != 2 {
		t.Fatalf("%d refunds, want 2 (the incident)", n)
	}
	if len(snap.FaultsFired) != 1 || snap.FaultsFired[0].ID != "inc-1042" || snap.FaultsFired[0].Request != 1 {
		t.Fatalf("faults fired: %+v", snap.FaultsFired)
	}
}

func TestIncidentDoublePostWithKeyCommitsOneRefund(t *testing.T) {
	w, ts := start(t, loadPayments(t))
	body := `{"order_id":"O-1042","amount_cents":4999}`
	short := &http.Client{Timeout: 200 * time.Millisecond}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/refunds", strings.NewReader(body))
	req.Header.Set("Idempotency-Key", "refund-O-1042")
	if _, err := short.Do(req); err == nil {
		t.Fatal("timeout fault did not fire")
	}
	if r, _ := post(t, ts.Client(), ts.URL+"/refunds", "refund-O-1042", body); r.StatusCode != 200 {
		t.Fatalf("keyed retry: %d, want the replayed 200", r.StatusCode)
	}
	if n := len(w.Snapshot().State["refunds"]); n != 1 {
		t.Fatalf("%d refunds, want 1", n)
	}
}

func statusSequence(t *testing.T, seed uint64, n int) []int {
	t.Helper()
	d, err := Parse([]byte(`{"world_version":1,"base_url_env":"X_URL","seed":` + itoa(seed) + `,
	  "resources":{"things":{"path":"/things","routes":["create","list"]}},
	  "faults":[{"resource":"things","route":"create","fault":429,"probability":0.3},
	            {"resource":"things","route":"create","fault":500,"probability":0.3},
	            {"resource":"things","route":"list","fault":500,"on_requests":[2,4]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	_, ts := start(t, d)
	var seq []int
	for i := 0; i < n; i++ {
		r, _ := post(t, ts.Client(), ts.URL+"/things", "", `{}`)
		seq = append(seq, r.StatusCode)
		resp, err := ts.Client().Get(ts.URL + "/things")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		seq = append(seq, resp.StatusCode)
	}
	return seq
}

func itoa(n uint64) string { b, _ := json.Marshal(n); return string(b) }

func TestFaultsAreIdenticalAcrossRerunsForAFixedSeed(t *testing.T) {
	a, b := statusSequence(t, 7, 60), statusSequence(t, 7, 60)
	if !equalInts(a, b) {
		t.Fatalf("same seed, different outcomes:\n%v\n%v", a, b)
	}
	saw := map[int]bool{}
	for _, s := range a {
		saw[s] = true
	}
	if !saw[429] || !saw[500] || !saw[201] {
		t.Fatalf("sequence never mixed outcomes: %v", saw)
	}
	if equalInts(a, statusSequence(t, 8, 60)) {
		t.Fatal("a different seed gave the same sequence")
	}
}

func TestListFaultFiresOnListedOrdinalsOnly(t *testing.T) {
	seq := statusSequence(t, 7, 6)
	for i := 0; i < 6; i++ {
		want := 200
		if i+1 == 2 || i+1 == 4 {
			want = 500
		}
		if got := seq[2*i+1]; got != want {
			t.Errorf("list #%d: %d, want %d", i+1, got, want)
		}
	}
}

func equalInts(a, b []int) bool {
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

func TestStateEndpointServesAValidSnapshot(t *testing.T) {
	_, ts := start(t, loadPayments(t))
	resp, err := ts.Client().Get(ts.URL + StatePath)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if err := ValidateSnapshot(raw); err != nil {
		t.Fatalf("snapshot rejected by world-v1: %v\n%s", err, raw)
	}
	if !strings.Contains(string(raw), `"O-1042"`) {
		t.Fatalf("seed missing from snapshot: %s", raw)
	}
}

func TestSnapshotIsACopy(t *testing.T) {
	w, _ := start(t, loadPayments(t))
	s := w.Snapshot()
	s.State["orders"][0]["status"] = "tampered"
	if w.Snapshot().State["orders"][0]["status"] != "delivered" {
		t.Fatal("snapshot aliases live state")
	}
}

func TestParseRejectsUnaddressableIDsAndUnrepresentableNumbers(t *testing.T) {
	base := "world_version: 1\nbase_url_env: X_URL\n"
	for name, src := range map[string]string{
		"id_format slash": base + "resources:\n  a: {path: /a, id_format: \"x/%d\", routes: [create]}\n",
		"seed id slash":   base + "resources:\n  a: {path: /a, routes: [get], seed: [{id: \"a/b\"}]}\n",
		"inf":             base + "resources:\n  a: {path: /a, routes: [get], seed: [{id: x, n: .inf}]}\n",
		"huge integer":    base + "resources:\n  a: {path: /a, routes: [get], seed: [{id: x, n: 123456789012345678901234567890}]}\n",
		"huge ordinal":    base + "resources:\n  a: {path: /a, routes: [create]}\nfaults:\n  - {resource: a, route: create, fault: 500, on_requests: [100000000000000000000]}\n",
	} {
		t.Run(name, func(t *testing.T) {
			err := func() error { _, err := Parse([]byte(src)); return err }()
			if err == nil || !strings.Contains(err.Error(), "/") {
				t.Fatalf("want an error naming a path, got %v", err)
			}
		})
	}
}

func TestTimeoutFaultOnRejectedCreateFiresNothing(t *testing.T) {
	w, ts := start(t, loadPayments(t))
	if r, _ := post(t, ts.Client(), ts.URL+"/refunds", "", "nope"); r.StatusCode != 400 {
		t.Fatalf("bad body: %d", r.StatusCode)
	}
	if n := len(w.Snapshot().FaultsFired); n != 0 {
		t.Fatalf("%d faults fired on a rejected create", n)
	}
}
