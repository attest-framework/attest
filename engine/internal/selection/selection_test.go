package selection_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/attest-ai/attest/engine/internal/selection"
)

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const fixtureJSON = `{"fixture_version": 1, "name": "inc-1042"}`

// newRepo returns a repo on branch main with two tests, one referencing the
// inc-1042 fixture by name, and a feature branch checked out at the same commit.
func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "tests/test_refund.py", "def test_it():\n    attest_replay(\"inc-1042\")\n")
	write(t, dir, "tests/test_other.py", "def test_other():\n    pass\n")
	write(t, dir, "tests/attest/fixtures/inc-1042.attest.json", fixtureJSON)
	write(t, dir, "uv.lock", "v1\n")
	write(t, dir, "README.md", "hi\n")
	write(t, dir, "src/agent.py", "x = 1\n")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "init")
	run(t, dir, "checkout", "-q", "-b", "feature")
	return dir
}

func commit(t *testing.T, dir string) {
	t.Helper()
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "change")
}

func TestSelect_FixtureOnlyDiff_SelectsReferencingTests(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "tests/attest/fixtures/inc-1042.attest.json", `{"fixture_version": 1, "name": "inc-1042", "x": 2}`)
	commit(t, dir)

	res, err := selection.Select(dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != selection.ModeSubset || !reflect.DeepEqual(res.Tests, []string{"tests/test_refund.py"}) {
		t.Errorf("got %+v, want subset [tests/test_refund.py]", res)
	}
	want := []selection.Reason{{Path: "tests/attest/fixtures/inc-1042.attest.json", Class: selection.ClassFixture}}
	if !reflect.DeepEqual(res.Reasons, want) {
		t.Errorf("reasons = %+v, want %+v", res.Reasons, want)
	}
}

func TestSelect_LockfileDiff_SelectsAll(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "uv.lock", "v2\n")
	commit(t, dir)

	res, err := selection.Select(dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != selection.ModeAll {
		t.Errorf("mode = %s, want all", res.Mode)
	}
}

func TestSelect_UnknownFile_SelectsAll(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "src/agent.py", "x = 2\n")
	commit(t, dir)

	res, err := selection.Select(dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != selection.ModeAll || res.Reasons[0].Class != selection.ClassUnknown {
		t.Errorf("got %+v, want all with unknown reason", res)
	}
}

func TestSelect_MarkdownOnlyDiff_SelectsNothing(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "README.md", "changed\n")
	commit(t, dir)

	res, err := selection.Select(dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != selection.ModeSubset || len(res.Tests) != 0 {
		t.Errorf("got %+v, want empty subset", res)
	}
}

func TestSelect_UnreferencedFixture_SelectsAll(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "tests/attest/fixtures/orphan.attest.json", `{"fixture_version": 1, "name": "orphan"}`)
	commit(t, dir)

	res, err := selection.Select(dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != selection.ModeAll {
		t.Errorf("mode = %s, want all", res.Mode)
	}
}

func TestSelect_DeletedFixture_SelectsAll(t *testing.T) {
	dir := newRepo(t)
	run(t, dir, "rm", "-q", "tests/attest/fixtures/inc-1042.attest.json")
	commit(t, dir)

	res, err := selection.Select(dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != selection.ModeAll {
		t.Errorf("mode = %s, want all", res.Mode)
	}
}

func TestSelect_WorkingTreeAndUntrackedTests_AreIncluded(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "tests/test_other.py", "def test_other():\n    assert True\n")
	write(t, dir, "tests/test_new.py", "def test_new():\n    pass\n")

	res, err := selection.Select(dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"tests/test_new.py", "tests/test_other.py"}
	if res.Mode != selection.ModeSubset || !reflect.DeepEqual(res.Tests, want) {
		t.Errorf("got %+v, want subset %v", res, want)
	}
}

func TestSelect_DeletedTestFile_IsNotSelected(t *testing.T) {
	dir := newRepo(t)
	run(t, dir, "rm", "-q", "tests/test_other.py")
	commit(t, dir)

	res, err := selection.Select(dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != selection.ModeSubset || len(res.Tests) != 0 {
		t.Errorf("got %+v, want empty subset", res)
	}
}

func TestSelect_ConftestChange_SelectsTestsBelowIt(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "tests/conftest.py", "import pytest\n")
	commit(t, dir)

	res, err := selection.Select(dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"tests/test_other.py", "tests/test_refund.py"}
	if !reflect.DeepEqual(res.Tests, want) {
		t.Errorf("tests = %v, want %v", res.Tests, want)
	}
}

func TestSelect_FixtureReferencedByPath_SelectsTest(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "tests/test_other.py", "FIXTURE = load_fixture('fixtures/inc-1042.attest.json')\n")
	write(t, dir, "tests/test_refund.py", "def test_it():\n    pass\n")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "base moves")
	run(t, dir, "checkout", "-q", "-b", "feature2")
	write(t, dir, "tests/attest/fixtures/inc-1042.attest.json", `{"fixture_version": 1, "name": "inc-1042", "x": 3}`)
	commit(t, dir)

	res, err := selection.Select(dir, "feature")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.Tests, []string{"tests/test_other.py"}) {
		t.Errorf("tests = %v, want [tests/test_other.py]", res.Tests)
	}
}

func TestSelect_BadRef_Errors(t *testing.T) {
	dir := newRepo(t)
	for _, ref := range []string{"no-such-ref", "--output=x", ""} {
		if _, err := selection.Select(dir, ref); err == nil {
			t.Errorf("ref %q: want error", ref)
		}
	}
}

func TestSelect_NotAGitRepo_Errors(t *testing.T) {
	if _, err := selection.Select(t.TempDir(), "main"); err == nil {
		t.Error("want error outside a git repo")
	}
}

func TestClassify_Paths(t *testing.T) {
	cases := map[string]string{
		"tests/test_a.py":                 selection.ClassTest,
		"pkg/a_test.py":                   selection.ClassTest,
		"conftest.py":                     selection.ClassTest,
		"src/a.test.ts":                   selection.ClassTest,
		"src/a.test.tsx":                  selection.ClassTest,
		"src/a.test.mjs":                  selection.ClassTest,
		"x/inc.attest.json":               selection.ClassFixture,
		"docs/a.md":                       selection.ClassIgnored,
		"uv.lock":                         selection.ClassAll,
		"sub/poetry.lock":                 selection.ClassAll,
		"requirements-dev.txt":            selection.ClassAll,
		"package-lock.json":               selection.ClassAll,
		"pnpm-lock.yaml":                  selection.ClassAll,
		"yarn.lock":                       selection.ClassAll,
		"bun.lockb":                       selection.ClassAll,
		"pyproject.toml":                  selection.ClassAll,
		"package.json":                    selection.ClassAll,
		"attest.policy.yaml":              selection.ClassAll,
		"world.yaml":                      selection.ClassAll,
		"payments.world.yaml":             selection.ClassAll,
		"src/agent.py":                    selection.ClassUnknown,
		".github/workflows/ci.yml":        selection.ClassUnknown,
		"tests/attest/fixtures/note.json": selection.ClassUnknown,
	}
	for p, want := range cases {
		if got := selection.Classify(p); got != want {
			t.Errorf("Classify(%q) = %s, want %s", p, got, want)
		}
	}
}

func TestSelect_FixtureReferencedFromConftest_SelectsTestsBelowIt(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "tests/conftest.py", "def refund():\n    return attest_replay(\"inc-1042\")\n")
	write(t, dir, "tests/test_a.py", "def test_a(refund):\n    pass\n")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "conftest")
	run(t, dir, "checkout", "-q", "-b", "feature2")
	write(t, dir, "tests/attest/fixtures/inc-1042.attest.json", `{"fixture_version": 1, "name": "inc-1042", "x": 4}`)
	commit(t, dir)

	res, err := selection.Select(dir, "feature")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"tests/test_a.py", "tests/test_other.py", "tests/test_refund.py"}
	if res.Mode != selection.ModeSubset || !reflect.DeepEqual(res.Tests, want) {
		t.Errorf("got %+v, want subset %v", res, want)
	}
}

func TestSelect_FixtureReferencedFromHelperModule_SelectsAll(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "tests/helpers.py", "NAME = 'inc-1042'\n")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "helper")
	run(t, dir, "checkout", "-q", "-b", "feature2")
	write(t, dir, "tests/attest/fixtures/inc-1042.attest.json", `{"fixture_version": 1, "name": "inc-1042", "x": 5}`)
	commit(t, dir)

	res, err := selection.Select(dir, "feature")
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != selection.ModeAll {
		t.Errorf("mode = %s, want all", res.Mode)
	}
}

func TestSelect_RepoSubdirectory_RootLockfileSelectsAll(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "pkg/tests/test_p.py", "def test_p():\n    pass\n")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "pkg")
	run(t, dir, "checkout", "-q", "-b", "feature2")
	write(t, dir, "uv.lock", "v3\n")
	commit(t, dir)

	res, err := selection.Select(filepath.Join(dir, "pkg"), "feature")
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != selection.ModeAll {
		t.Errorf("mode = %s, want all", res.Mode)
	}
}

func TestSelect_RepoSubdirectory_PathsAreRelativeAndOutsideDocsIgnored(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "pkg/tests/test_p.py", "def test_p():\n    pass\n")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "pkg")
	run(t, dir, "checkout", "-q", "-b", "feature2")
	write(t, dir, "pkg/tests/test_p.py", "def test_p():\n    assert True\n")
	write(t, dir, "tests/test_refund.py", "def test_it():\n    assert True\n")
	write(t, dir, "README.md", "changed\n")
	commit(t, dir)

	res, err := selection.Select(filepath.Join(dir, "pkg"), "feature")
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != selection.ModeSubset || !reflect.DeepEqual(res.Tests, []string{"tests/test_p.py"}) {
		t.Errorf("got %+v, want subset [tests/test_p.py]", res)
	}
}
