package profile

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadAppliesDefaults(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "myrepo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	path := write(t, dir, "thing.toml", `
repo = "`+repo+`"
[ports]
PORT = { base = 3100 }
[services]
api = "go run ./cmd/api"
`)

	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "thing" {
		t.Errorf("Name = %q, want thing (derived from the file name)", p.Name)
	}
	if want := repo + "-worktrees"; p.Worktrees != want {
		t.Errorf("Worktrees = %q, want %q", p.Worktrees, want)
	}
	if p.Ports["PORT"].Stride != 1 {
		t.Errorf("Stride = %d, want default 1", p.Ports["PORT"].Stride)
	}
	if p.Network() != "branchbox-thing" {
		t.Errorf("Network = %q", p.Network())
	}
}

func TestLoadRequiresRepo(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "bad.toml", "name = \"bad\"\n")
	if _, err := Load(path); err == nil {
		t.Fatal("expected an error when repo is missing")
	}
}

func TestComposePathResolvesRelativeToProfile(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "p.toml", "repo = \""+dir+"\"\n[infra]\ncompose = \"infra/x.yaml\"\n")
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "infra/x.yaml"); p.ComposePath() != want {
		t.Errorf("ComposePath = %q, want %q", p.ComposePath(), want)
	}
}

func TestResolveByName(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "profiles"), "alpha.toml", "repo = \""+dir+"\"\n")
	dirs := []string{filepath.Join(dir, "profiles")}

	p, err := Resolve("alpha", dir, dirs)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "alpha" {
		t.Errorf("Name = %q", p.Name)
	}
	if _, err := Resolve("missing", dir, dirs); err == nil {
		t.Fatal("expected an error for an unknown profile")
	}
}

func TestResolveByWorkingDirectoryPrefersNestedRepo(t *testing.T) {
	dir := t.TempDir()
	outer := filepath.Join(dir, "outer")
	inner := filepath.Join(outer, "inner")
	if err := os.MkdirAll(filepath.Join(inner, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	pdir := filepath.Join(dir, "profiles")
	write(t, pdir, "outer.toml", "repo = \""+outer+"\"\n")
	write(t, pdir, "inner.toml", "repo = \""+inner+"\"\n")

	p, err := Resolve("", filepath.Join(inner, "sub"), []string{pdir})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "inner" {
		t.Errorf("Name = %q, want inner (the nearest enclosing repo)", p.Name)
	}

	if _, err := Resolve("", dir, []string{pdir}); err == nil {
		t.Fatal("expected an error when no profile matches the working directory")
	}
}

func TestResolveMatchesWorktreesDirectory(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	trees := filepath.Join(dir, "trees", "feature-x")
	if err := os.MkdirAll(trees, 0o755); err != nil {
		t.Fatal(err)
	}
	pdir := filepath.Join(dir, "profiles")
	write(t, pdir, "p.toml", "repo = \""+repo+"\"\nworktrees = \""+filepath.Join(dir, "trees")+"\"\n")

	// Running branchbox from inside a branch checkout must resolve the same
	// profile as running it from the main repo.
	p, err := Resolve("", trees, []string{pdir})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "p" {
		t.Errorf("Name = %q", p.Name)
	}
}

func TestDiscoverPrefersNearestDirectory(t *testing.T) {
	dir := t.TempDir()
	user := filepath.Join(dir, "user")
	tool := filepath.Join(dir, "tool")
	write(t, user, "same.toml", "repo = \""+dir+"/user-repo\"\n")
	write(t, tool, "same.toml", "repo = \""+dir+"/tool-repo\"\n")

	all, err := Discover([]string{user, tool})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("len = %d, want 1 (the user profile shadows the shipped one)", len(all))
	}
	if filepath.Base(all[0].Repo) != "user-repo" {
		t.Errorf("Repo = %q, want the user copy", all[0].Repo)
	}
}
