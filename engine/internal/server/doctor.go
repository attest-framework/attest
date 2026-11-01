package server

import (
	"log/slog"

	"github.com/attest-ai/attest/engine/internal/assertion"
	"github.com/attest-ai/attest/engine/internal/buildinfo"
)

// DoctorSchemaVersion is the version of the `attest-engine doctor --json`
// shape defined by proto/attest/v1/doctor.schema.json.
const DoctorSchemaVersion = 1

// Diagnosis is the engine's view of its configuration under the current
// environment. It never carries an API key, only the variable it came from.
type Diagnosis struct {
	SchemaVersion   int            `json:"schema_version"`
	EngineVersion   string         `json:"engine_version"`
	ProtocolVersion int            `json:"protocol_version"`
	Capabilities    []string       `json:"capabilities"`
	SimulationMode  bool           `json:"simulation_mode"`
	Judge           JudgeDiagnosis `json:"judge"`
	Embedding       EmbedDiagnosis `json:"embedding"`
	CacheDir        string         `json:"cache_dir"`
}

// JudgeDiagnosis describes the Layer 6 judge. Every field except Configured
// is nil when no judge is configured.
type JudgeDiagnosis struct {
	Configured   bool    `json:"configured"`
	Provider     *string `json:"provider"`
	Model        *string `json:"model"`
	BaseURL      *string `json:"base_url"`
	APIKeySource *string `json:"api_key_source"`
}

// EmbedDiagnosis describes the Layer 5 embedder.
type EmbedDiagnosis struct {
	Configured bool    `json:"configured"`
	Requested  string  `json:"requested"`
	Provider   *string `json:"provider"`
}

// Diagnose resolves providers exactly as the JSON-RPC server does at startup
// and reports the capabilities `initialize` would advertise. It returns the
// same configuration errors the server would fail to start with.
func Diagnose(logger *slog.Logger) (*Diagnosis, error) {
	if err := validateEngineEnv(); err != nil {
		return nil, err
	}
	provs, err := buildProviders(logger)
	if err != nil {
		return nil, err
	}
	simulation := assertion.SimulationModeFromEnv()

	d := &Diagnosis{
		SchemaVersion:   DoctorSchemaVersion,
		EngineVersion:   buildinfo.Version,
		ProtocolVersion: protocolVersion,
		Capabilities:    provs.capabilities(simulation),
		SimulationMode:  simulation,
		Embedding:       EmbedDiagnosis{Requested: provs.embeddingRequested},
		CacheDir:        cacheDirectory(),
	}
	if provs.embedder != nil {
		d.Embedding.Configured = true
		d.Embedding.Provider = &provs.embeddingProvider
	}
	if provs.judge != nil {
		cfg := provs.judgeConfig
		model := provs.judge.DefaultModel()
		d.Judge = JudgeDiagnosis{
			Configured:   true,
			Provider:     &cfg.provider,
			Model:        &model,
			BaseURL:      &cfg.baseURL,
			APIKeySource: &cfg.keySource,
		}
	}
	return d, nil
}
