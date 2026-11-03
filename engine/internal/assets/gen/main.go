// Command gen stages docs/guides and skills into engine/internal/assets and
// writes the plugin manifests. Run it through go generate from
// engine/internal/assets: cd engine && go generate ./internal/assets/
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/attest-ai/attest/engine/internal/assets/assetsync"
)

func main() {
	// go generate runs in the package directory, engine/internal/assets.
	repoRoot := filepath.Join("..", "..", "..")
	if err := assetsync.Generate(repoRoot); err != nil {
		fmt.Fprintf(os.Stderr, "generate assets: %v\n", err)
		os.Exit(1)
	}
}
