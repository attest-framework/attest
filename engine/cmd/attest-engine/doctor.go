package main

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/attest-ai/attest/engine/internal/server"
)

func init() {
	register(command{name: "doctor", summary: "report engine version, capabilities and provider configuration (--json)", run: runDoctor})
}

// runDoctor reports the configuration the JSON-RPC server would start with
// under the current environment. Exits 1 on a configuration error the server
// would refuse to start with, 2 on a usage error.
func runDoctor(args []string) {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "emit JSON matching proto/attest/v1/doctor.schema.json")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "usage: attest-engine doctor [--json]")
		os.Exit(2)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	d, err := server.Diagnose(logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "doctor: configuration error: %v\n", err)
		os.Exit(1)
	}
	if *asJSON {
		emitJSON(d)
		return
	}
	printDiagnosis(os.Stdout, d)
}

// printDiagnosis writes the human-readable doctor report.
func printDiagnosis(w io.Writer, d *server.Diagnosis) {
	fmt.Fprintf(w, "engine:          attest-engine %s (protocol %d)\n", d.EngineVersion, d.ProtocolVersion)
	sim := "off"
	if d.SimulationMode {
		sim = "on (layers 5-6 return not_evaluated, no provider calls)"
	}
	fmt.Fprintf(w, "simulation mode: %s\n", sim)

	if d.Judge.Configured {
		fmt.Fprintf(w, "judge (L6):      %s, model %s, base URL %s, key from %s\n",
			*d.Judge.Provider, *d.Judge.Model, *d.Judge.BaseURL, *d.Judge.APIKeySource)
	} else {
		fmt.Fprintln(w, "judge (L6):      not configured (set ATTEST_JUDGE_API_KEY or OPENAI_API_KEY)")
	}

	if d.Embedding.Configured {
		fmt.Fprintf(w, "embedding (L5):  %s (requested %s)\n", *d.Embedding.Provider, d.Embedding.Requested)
	} else {
		fmt.Fprintf(w, "embedding (L5):  not configured (requested %s)\n", d.Embedding.Requested)
	}

	fmt.Fprintf(w, "cache dir:       %s\n", d.CacheDir)
	fmt.Fprintf(w, "capabilities:    %s\n", strings.Join(d.Capabilities, ", "))
}
