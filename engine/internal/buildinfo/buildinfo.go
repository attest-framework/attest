// Package buildinfo exposes the engine's build-time identity.
//
// The version string is the single source of truth shared across every
// surface of the Attest project (Go engine, Python SDK, TypeScript SDK,
// docs). It is loaded from the repository's VERSION file at compile time
// via go:embed, then mirrored to consumers by importing buildinfo.Version.
//
// To change the version, edit the top-level VERSION file and run
// `make verify-versions` to confirm every language artefact agrees.
package buildinfo

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var versionRaw string

// Version is the engine's semantic version, embedded from VERSION at the
// engine module root. The repository-level scripts/verify-versions.sh
// guarantees that file matches the project-root VERSION on every commit.
var Version = strings.TrimSpace(versionRaw)
