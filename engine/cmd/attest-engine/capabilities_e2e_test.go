package main_test

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCapabilities_JSON_ListsSixLayersAndPlugins(t *testing.T) {
	// Arrange
	bin := buildEngineBinary(t)

	// Act
	stdout, stderr, code := runEngine(t, bin, t.TempDir(), "capabilities", "--json")

	// Assert
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	var doc struct {
		License      string   `json:"license"`
		SDKLanguages []string `json:"sdk_languages"`
		Layers       []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"layers"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	if doc.License != "Apache-2.0" || strings.Join(doc.SDKLanguages, ",") != "python,typescript" {
		t.Errorf("license %q, sdk_languages %v", doc.License, doc.SDKLanguages)
	}
	if len(doc.Layers) != 7 || doc.Layers[5].ID != 6 || doc.Layers[6].ID != 8 || doc.Layers[6].Name != "Plugins" {
		t.Errorf("layers = %+v, want L1-L6 plus plugins (id 8)", doc.Layers)
	}
}

func TestCapabilities_Text_PrintsPluginsNotLayerEight(t *testing.T) {
	bin := buildEngineBinary(t)

	stdout, _, code := runEngine(t, bin, t.TempDir(), "capabilities")

	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stdout, "plugins  Plugins") || strings.Contains(stdout, "Layer 8") {
		t.Errorf("text output:\n%s", stdout)
	}
}

func TestCapabilities_ExtraArgument_ExitsTwo(t *testing.T) {
	bin := buildEngineBinary(t)

	stdout, stderr, code := runEngine(t, bin, t.TempDir(), "capabilities", "extra")

	if code != 2 || stdout != "" || !strings.Contains(stderr, "usage: attest-engine capabilities") {
		t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
	}
}
