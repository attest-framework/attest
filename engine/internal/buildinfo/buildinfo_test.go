package buildinfo

import (
	"regexp"
	"testing"
)

// semverPattern matches major.minor.patch with an optional pre-release/build
// suffix — strict enough to catch a stray newline or empty embed, loose enough
// not to fight legitimate -rc.N or -alpha tags during release.
var semverPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[+-][0-9A-Za-z.\-]+)?$`)

func TestVersionIsEmbedded(t *testing.T) {
	if Version == "" {
		t.Fatal("Version is empty; the //go:embed directive in buildinfo.go " +
			"could not load engine/internal/buildinfo/VERSION")
	}
	if !semverPattern.MatchString(Version) {
		t.Fatalf("Version = %q is not a valid semver string", Version)
	}
}
