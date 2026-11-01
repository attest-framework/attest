package main

import (
	"fmt"

	"github.com/attest-ai/attest/engine/internal/buildinfo"
)

func init() {
	register(command{name: "version", summary: "print the engine version", run: runVersion})
}

// runVersion prints `attest-engine <version>`.
func runVersion(_ []string) {
	fmt.Printf("attest-engine %s\n", buildinfo.Version)
}
