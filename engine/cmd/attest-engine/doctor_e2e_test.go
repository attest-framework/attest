package main_test

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/attest-ai/attest/engine/pkg/types"
)

const (
	doctorSchemaPath  = "../../../proto/attest/v1/doctor.schema.json"
	doctorFixtureRoot = "../../../protocol-tests/doctor"
)

// providerEnv lists every variable that selects or configures an L5/L6
// provider; engineEnv blanks them so the host environment cannot leak in.
var providerEnv = []string{
	"ATTEST_JUDGE_PROVIDER", "ATTEST_JUDGE_MODEL", "ATTEST_JUDGE_BASE_URL", "ATTEST_JUDGE_API_KEY",
	"OPENAI_API_KEY", "ATTEST_EMBEDDING_PROVIDER", "ATTEST_SIMULATION",
}

func engineEnv(cacheDir string, extra ...string) []string {
	env := append(os.Environ(), "ATTEST_CACHE_DIR="+cacheDir)
	for _, k := range providerEnv {
		env = append(env, k+"=")
	}
	return append(env, extra...)
}

func compileDoctorSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	f, err := os.Open(doctorSchemaPath)
	if err != nil {
		t.Fatalf("open schema: %v", err)
	}
	defer f.Close()
	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("doctor.schema.json", doc); err != nil {
		t.Fatalf("add schema: %v", err)
	}
	s, err := c.Compile("doctor.schema.json")
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	return s
}

func validateDoctorJSON(s *jsonschema.Schema, data string) error {
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(data))
	if err != nil {
		return err
	}
	return s.Validate(doc)
}

func runDoctor(t *testing.T, bin string, env []string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(bin, append([]string{"doctor"}, args...)...)
	cmd.Env = env
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if exitErr, ok := err.(*exec.ExitError); ok {
		return stdout.String(), stderr.String(), exitErr.ExitCode()
	}
	if err != nil {
		t.Fatalf("run doctor: %v", err)
	}
	return stdout.String(), stderr.String(), 0
}

// initializeCapabilities starts the JSON-RPC server with env and returns the
// capabilities its initialize response advertises.
func initializeCapabilities(t *testing.T, bin string, env []string) []string {
	t.Helper()
	cmd := exec.Command(bin, "--log-level=error")
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	params, _ := json.Marshal(types.InitializeParams{SDKName: "doctor-test", SDKVersion: "0", ProtocolVersion: 1})
	req, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": json.RawMessage(params)})
	if _, err := stdin.Write(append(req, '\n')); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read initialize response: %v", err)
	}
	stdin.Close()
	_ = cmd.Wait()

	var resp struct {
		Result types.InitializeResult `json:"result"`
		Error  *types.RPCError        `json:"error"`
	}
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatalf("decode initialize response %s: %v", line, err)
	}
	if resp.Error != nil {
		t.Fatalf("initialize returned an error: %+v", resp.Error)
	}
	return resp.Result.Capabilities
}

func TestDoctorFixtures_MatchSchema(t *testing.T) {
	s := compileDoctorSchema(t)
	for _, kind := range []string{"valid", "invalid"} {
		paths, err := filepath.Glob(filepath.Join(doctorFixtureRoot, kind, "*.json"))
		if err != nil || len(paths) == 0 {
			t.Fatalf("no %s doctor fixtures (err=%v)", kind, err)
		}
		for _, p := range paths {
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			err = validateDoctorJSON(s, string(data))
			if kind == "valid" && err != nil {
				t.Errorf("%s: want valid, got %v", filepath.Base(p), err)
			}
			if kind == "invalid" && err == nil {
				t.Errorf("%s: want invalid, schema accepted it", filepath.Base(p))
			}
		}
	}
}

// TestDoctorJSON_MatchesSchemaAndInitialize checks, per environment, that
// doctor --json matches the schema, never prints the key, and reports the
// exact capability list the server's initialize response advertises.
func TestDoctorJSON_MatchesSchemaAndInitialize(t *testing.T) {
	bin := buildEngineBinary(t)
	s := compileDoctorSchema(t)
	cases := []struct {
		name       string
		extra      []string
		wantJudge  bool
		wantSource string
		wantBase   string
		wantSim    bool
	}{
		{name: "no providers"},
		{name: "simulation", extra: []string{"ATTEST_SIMULATION=1"}, wantSim: true},
		{
			name:       "compatible endpoint",
			extra:      []string{"ATTEST_JUDGE_BASE_URL=http://127.0.0.1:9/v1/", "ATTEST_JUDGE_API_KEY=judge-secret-value", "ATTEST_JUDGE_MODEL=local"},
			wantJudge:  true,
			wantSource: "ATTEST_JUDGE_API_KEY",
			wantBase:   "http://127.0.0.1:9/v1",
		},
		{
			name:       "standard key",
			extra:      []string{"OPENAI_API_KEY=std-secret-value"},
			wantJudge:  true,
			wantSource: "OPENAI_API_KEY",
			wantBase:   "https://api.openai.com/v1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := engineEnv(t.TempDir(), tc.extra...)

			stdout, stderr, code := runDoctor(t, bin, env, "--json")

			if code != 0 {
				t.Fatalf("exit %d, stderr %s", code, stderr)
			}
			if err := validateDoctorJSON(s, stdout); err != nil {
				t.Fatalf("doctor --json does not match schema: %v\n%s", err, stdout)
			}
			if strings.Contains(stdout+stderr, "secret-value") {
				t.Fatalf("doctor output leaks an API key:\n%s%s", stdout, stderr)
			}
			var d struct {
				Capabilities   []string `json:"capabilities"`
				SimulationMode bool     `json:"simulation_mode"`
				Judge          struct {
					Configured   bool    `json:"configured"`
					BaseURL      *string `json:"base_url"`
					APIKeySource *string `json:"api_key_source"`
				} `json:"judge"`
			}
			if err := json.Unmarshal([]byte(stdout), &d); err != nil {
				t.Fatal(err)
			}
			if d.Judge.Configured != tc.wantJudge || d.SimulationMode != tc.wantSim {
				t.Errorf("judge.configured=%v simulation_mode=%v, want %v %v", d.Judge.Configured, d.SimulationMode, tc.wantJudge, tc.wantSim)
			}
			if tc.wantJudge && (*d.Judge.APIKeySource != tc.wantSource || *d.Judge.BaseURL != tc.wantBase) {
				t.Errorf("api_key_source=%q base_url=%q, want %q %q", *d.Judge.APIKeySource, *d.Judge.BaseURL, tc.wantSource, tc.wantBase)
			}
			if got := initializeCapabilities(t, bin, env); !slices.Equal(got, d.Capabilities) {
				t.Errorf("doctor capabilities %v != initialize capabilities %v", d.Capabilities, got)
			}
		})
	}
}

func TestDoctor_TextOutputAndConfigError(t *testing.T) {
	bin := buildEngineBinary(t)

	stdout, _, code := runDoctor(t, bin, engineEnv(t.TempDir()))
	if code != 0 || !strings.Contains(stdout, "judge (L6):      not configured") || !strings.Contains(stdout, "capabilities:") {
		t.Fatalf("text doctor: exit %d\n%s", code, stdout)
	}

	_, stderr, code := runDoctor(t, bin, engineEnv(t.TempDir(), "ATTEST_JUDGE_PROVIDER=gemini"), "--json")
	if code != 1 || !strings.Contains(stderr, "supported: openai") {
		t.Fatalf("config error: exit %d stderr %q, want 1 naming supported providers", code, stderr)
	}

	_, _, code = runDoctor(t, bin, engineEnv(t.TempDir()), "extra")
	if code != 2 {
		t.Fatalf("stray argument: exit %d, want 2", code)
	}
}
