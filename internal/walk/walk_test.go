package walk

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// tree writes files under dir. Keys are slash paths, values are contents.
func tree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func paths(files []File) []string {
	var out []string
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

func rels(files []File) []string {
	var out []string
	for _, f := range files {
		out = append(out, f.Rel)
	}
	return out
}

func TestGitRepoFollowsGitignore(t *testing.T) {
	dir := t.TempDir()
	tree(t, dir, map[string]string{
		".gitignore":         "node_modules/\n*.log\n",
		"src/app.go":         "package app\n",
		"src/untracked.go":   "package app\n",
		"node_modules/x.js":  "x\n",
		"debug.log":          "log\n",
		"src/sub/.gitignore": "secret.txt\n",
		"src/sub/secret.txt": "s\n",
		"src/sub/ok.txt":     "ok\n",
	})
	git(t, dir, "init", "-q")
	git(t, dir, "add", ".gitignore", "src/app.go")
	t.Chdir(dir)

	res, err := Walk([]string{"."}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".gitignore", "src/app.go", "src/sub/.gitignore", "src/sub/ok.txt", "src/untracked.go"}
	if got := paths(res.Files); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	if resolved, _ := filepath.EvalSymlinks(dir); res.Root != resolved {
		t.Fatalf("root %q, want %q", res.Root, resolved)
	}

	res, err = Walk([]string{"."}, Options{NoIgnore: true})
	if err != nil {
		t.Fatal(err)
	}
	got := paths(res.Files)
	for _, p := range []string{"node_modules/x.js", "debug.log", "src/sub/secret.txt"} {
		if !contains(got, p) {
			t.Errorf("--no-ignore should include %s, got %v", p, got)
		}
	}
	for _, p := range got {
		if strings.HasPrefix(p, ".git/") {
			t.Errorf("never list .git internals, got %s", p)
		}
	}
}

func TestSubdirectoryPathsAreShownAsGivenAndKeyedFromRepoRoot(t *testing.T) {
	dir := t.TempDir()
	tree(t, dir, map[string]string{"src/billing/retry.ts": "retry\n", "README.md": "hi\n"})
	git(t, dir, "init", "-q")
	t.Chdir(filepath.Join(dir, "src"))

	res, err := Walk([]string{"billing"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := paths(res.Files); !reflect.DeepEqual(got, []string{"billing/retry.ts"}) {
		t.Fatalf("display paths %v", got)
	}
	if got := rels(res.Files); !reflect.DeepEqual(got, []string{"src/billing/retry.ts"}) {
		t.Fatalf("repo paths %v", got)
	}
}

func TestOutsideGitParsesGitignoreFiles(t *testing.T) {
	dir := t.TempDir()
	tree(t, dir, map[string]string{
		".gitignore": strings.Join([]string{
			"# build output",
			"/dist",
			"*.min.js",
			"!keep.min.js",
			"logs/",
			"docs/**/draft.md",
		}, "\n"),
		"dist/app.js":          "x",
		"lib/dist/real.js":     "x", // /dist is anchored to the root
		"lib/a.min.js":         "x",
		"lib/keep.min.js":      "x",
		"lib/logs/today.txt":   "x",
		"lib/logs.txt":         "x", // logs/ only matches directories
		"docs/a/b/draft.md":    "x",
		"docs/draft.md":        "x", // **/ also matches zero directories
		"docs/final.md":        "x",
		"pkg/.gitignore":       "*.gen.go\n",
		"pkg/model.gen.go":     "x",
		"pkg/model.go":         "x",
		"other/model.gen.go":   "x", // nested rules apply only below their folder
		".git-not-a-repo.txt":  "x",
		"node_modules/dep.js":  "x",
		"node_modules/.hidden": "x",
	})
	t.Chdir(dir)

	res, err := Walk([]string{"."}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		".git-not-a-repo.txt", ".gitignore", "docs/final.md", "lib/dist/real.js",
		"lib/keep.min.js", "lib/logs.txt", "node_modules/.hidden", "node_modules/dep.js",
		"other/model.gen.go", "pkg/.gitignore", "pkg/model.go",
	}
	if got := paths(res.Files); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	if resolved, _ := filepath.EvalSymlinks(dir); res.Root != resolved {
		t.Fatalf("root %q, want the search path %q", res.Root, resolved)
	}
}

func TestSkipsBinariesSilentlyAndReportsOversizeFiles(t *testing.T) {
	dir := t.TempDir()
	tree(t, dir, map[string]string{
		"image.png":   "\x89PNG\x00\x00binary",
		"big.js":      strings.Repeat("a", 2000),
		"big.bin":     strings.Repeat("\x00", 2000),
		"small.go":    "package small\n",
		"exactly.txt": strings.Repeat("b", 1000),
	})
	t.Chdir(dir)

	res, err := Walk([]string{"."}, Options{MaxFileSize: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if got := paths(res.Files); !reflect.DeepEqual(got, []string{"exactly.txt", "small.go"}) {
		t.Fatalf("files %v", got)
	}
	if got := paths(res.Oversize); !reflect.DeepEqual(got, []string{"big.js"}) {
		t.Fatalf("oversize %v", got)
	}

	res, err = Walk([]string{"."}, Options{MaxFileSize: 0})
	if err != nil {
		t.Fatal(err)
	}
	if got := paths(res.Files); !reflect.DeepEqual(got, []string{"big.js", "exactly.txt", "small.go"}) {
		t.Fatalf("0 means no limit, got %v", got)
	}
}

func TestExplicitFileAndMissingPath(t *testing.T) {
	dir := t.TempDir()
	tree(t, dir, map[string]string{".gitignore": "gen.go\n", "gen.go": "package gen\n", "a.go": "package a\n"})
	t.Chdir(dir)

	res, err := Walk([]string{"gen.go", "a.go", "gen.go"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := paths(res.Files); !reflect.DeepEqual(got, []string{"a.go", "gen.go"}) {
		t.Fatalf("named files are searched once each, even if ignored: %v", got)
	}

	if _, err := Walk([]string{"nope"}, Options{}); err == nil {
		t.Fatal("expected an error for a missing path")
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
