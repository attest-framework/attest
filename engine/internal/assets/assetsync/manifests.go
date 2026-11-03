package assetsync

import (
	"bytes"
	"encoding/json"
)

// pluginMetadata is the single source for every plugin manifest. The version
// comes from the repository's VERSION file.
var pluginMetadata = struct {
	Name            string
	MarketplaceName string
	Description     string
	AuthorName      string
	AuthorURL       string
	Homepage        string
	Repository      string
	License         string
	Keywords        []string
}{
	Name:            "attest",
	MarketplaceName: "attest-framework",
	Description:     "Skills for testing AI agents with Attest: capture runs as fixtures, write deterministic assertions, and gate merges.",
	AuthorName:      "Attest Contributors",
	AuthorURL:       "https://github.com/attest-framework",
	Homepage:        "https://attest-framework.github.io/",
	Repository:      "https://github.com/attest-framework/attest",
	License:         "Apache-2.0",
	Keywords:        []string{"testing", "ai", "agents", "llm", "evaluation"},
}

// skillsPath is the plugin-root-relative skills directory, for manifests
// whose format declares it.
const skillsPath = "./skills/"

// Repository-relative manifest paths.
const (
	claudePluginPath      = ".claude-plugin/plugin.json"
	claudeMarketplacePath = ".claude-plugin/marketplace.json"
	cursorPluginPath      = ".cursor-plugin/plugin.json"
	codexPluginPath       = ".codex-plugin/plugin.json"
)

type authorWithURL struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type authorNameOnly struct {
	Name string `json:"name"`
}

// claudePlugin follows https://code.claude.com/docs/en/plugins-reference.
// Claude Code scans the plugin root's skills/ by default; a skills key would
// add a second scan of the same directory.
type claudePlugin struct {
	Name        string        `json:"name"`
	Version     string        `json:"version"`
	Description string        `json:"description"`
	Author      authorWithURL `json:"author"`
	Homepage    string        `json:"homepage"`
	Repository  string        `json:"repository"`
	License     string        `json:"license"`
	Keywords    []string      `json:"keywords"`
}

// claudeMarketplace follows https://code.claude.com/docs/en/plugin-marketplaces.
// The entry omits version: plugin.json carries it and wins.
type claudeMarketplace struct {
	Name        string                   `json:"name"`
	Owner       authorWithURL            `json:"owner"`
	Description string                   `json:"description"`
	Plugins     []claudeMarketplaceEntry `json:"plugins"`
}

type claudeMarketplaceEntry struct {
	Name        string `json:"name"`
	Source      string `json:"source"`
	Description string `json:"description"`
}

// cursorPlugin follows https://cursor.com/docs/reference/plugins.md; its
// author object has name and email only.
type cursorPlugin struct {
	Name        string         `json:"name"`
	Version     string         `json:"version"`
	Description string         `json:"description"`
	Author      authorNameOnly `json:"author"`
	Homepage    string         `json:"homepage"`
	Repository  string         `json:"repository"`
	License     string         `json:"license"`
	Keywords    []string       `json:"keywords"`
	Skills      string         `json:"skills"`
}

// codexPlugin follows the .codex-plugin/plugin.json compatibility manifest in
// https://developers.openai.com/codex/plugins/build.
type codexPlugin struct {
	Name        string        `json:"name"`
	Version     string        `json:"version"`
	Description string        `json:"description"`
	Author      authorWithURL `json:"author"`
	Homepage    string        `json:"homepage"`
	Repository  string        `json:"repository"`
	License     string        `json:"license"`
	Keywords    []string      `json:"keywords"`
	Skills      string        `json:"skills"`
}

// renderManifests returns every plugin manifest for version, keyed by
// repository-relative path.
func renderManifests(version string) (map[string][]byte, error) {
	m := pluginMetadata
	author := authorWithURL{Name: m.AuthorName, URL: m.AuthorURL}
	docs := map[string]any{
		claudePluginPath: claudePlugin{
			Name: m.Name, Version: version, Description: m.Description, Author: author,
			Homepage: m.Homepage, Repository: m.Repository, License: m.License, Keywords: m.Keywords,
		},
		claudeMarketplacePath: claudeMarketplace{
			Name: m.MarketplaceName, Owner: author, Description: m.Description,
			Plugins: []claudeMarketplaceEntry{{Name: m.Name, Source: "./", Description: m.Description}},
		},
		cursorPluginPath: cursorPlugin{
			Name: m.Name, Version: version, Description: m.Description,
			Author:   authorNameOnly{Name: m.AuthorName},
			Homepage: m.Homepage, Repository: m.Repository, License: m.License, Keywords: m.Keywords,
			Skills: skillsPath,
		},
		codexPluginPath: codexPlugin{
			Name: m.Name, Version: version, Description: m.Description, Author: author,
			Homepage: m.Homepage, Repository: m.Repository, License: m.License, Keywords: m.Keywords,
			Skills: skillsPath,
		},
	}
	out := make(map[string][]byte, len(docs))
	for rel, doc := range docs {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(doc); err != nil {
			return nil, err
		}
		out[rel] = buf.Bytes()
	}
	return out, nil
}
