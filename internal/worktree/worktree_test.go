package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/jegoldberg8/branchbox/internal/profile"
)

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"feature/oku-7234": "feature-oku-7234",
		"Feature/OKU_123":  "feature-oku-123",
		"main":             "main",
		"///":              "branch",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

// newRepo builds a real git repository, because the whole point of this
// package is its interaction with git's worktree bookkeeping.
func newRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-qm", "init")
	return dir
}

func testProfile(t *testing.T, repo string, secrets ...string) *profile.Profile {
	t.Helper()
	body := "name = \"p\"\nrepo = \"" + repo + "\"\nworktrees = \"" + repo + "-wt\"\n"
	if len(secrets) > 0 {
		body += "secrets = ["
		for i, s := range secrets {
			if i > 0 {
				body += ", "
			}
			body += "\"" + s + "\""
		}
		body += "]\n"
	}
	path := filepath.Join(t.TempDir(), "p.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := profile.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEnsureCreatesWorktreeForNewBranch(t *testing.T) {
	repo := newRepo(t)
	p := testProfile(t, repo)

	// No origin here, so the branch must already exist locally: create it the
	// way a developer would before asking for a stack.
	cmd := exec.Command("git", "branch", "feature/x")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git branch: %v: %s", err, out)
	}

	wt, err := Ensure(p, "feature/x")
	if err != nil {
		t.Fatal(err)
	}
	if wt.Slug != "feature-x" {
		t.Errorf("Slug = %q", wt.Slug)
	}
	if _, err := os.Stat(filepath.Join(wt.Path, "file.txt")); err != nil {
		t.Errorf("worktree is not checked out: %v", err)
	}
	if branch := currentBranch(t, wt.Path); branch != "feature/x" {
		t.Errorf("branch = %q, want feature/x", branch)
	}

	// Idempotent: a second call reuses the same checkout instead of failing on
	// git's one-checkout-per-branch rule.
	again, err := Ensure(p, "feature/x")
	if err != nil {
		t.Fatal(err)
	}
	if again.Path != wt.Path {
		t.Errorf("second Ensure returned %q, want %q", again.Path, wt.Path)
	}
}

func TestEnsureReusesTheMainCheckoutForItsOwnBranch(t *testing.T) {
	repo := newRepo(t)
	p := testProfile(t, repo)

	wt, err := Ensure(p, "main")
	if err != nil {
		t.Fatal(err)
	}
	if wt.Path != repo {
		t.Errorf("Path = %q, want the main checkout %q", wt.Path, repo)
	}
}

func TestEnsureLinksSecretsWithoutClobberingRealFiles(t *testing.T) {
	repo := newRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "config.yaml"), []byte("k: v\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "a-credentials.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := testProfile(t, repo, "config.yaml", "*-credentials.json")

	cmd := exec.Command("git", "branch", "feature/y")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git branch: %v: %s", err, out)
	}
	wt, err := Ensure(p, "feature/y")
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"config.yaml", "a-credentials.json"} {
		link := filepath.Join(wt.Path, name)
		info, err := os.Lstat(link)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("%s is not a symlink", name)
		}
		target, err := os.Readlink(link)
		if err != nil {
			t.Fatal(err)
		}
		if target != filepath.Join(repo, name) {
			t.Errorf("%s points at %q", name, target)
		}
	}

	// A real file in the worktree must survive a re-run untouched.
	real := filepath.Join(wt.Path, "config.yaml")
	if err := os.Remove(real); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real, []byte("local: override\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Ensure(p, "feature/y"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(real)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "local: override\n" {
		t.Errorf("a real file was replaced by a symlink: %q", body)
	}
}

func TestEnsureRecoversFromAManuallyDeletedWorktree(t *testing.T) {
	repo := newRepo(t)
	p := testProfile(t, repo)
	cmd := exec.Command("git", "branch", "feature/z")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git branch: %v: %s", err, out)
	}
	wt, err := Ensure(p, "feature/z")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(wt.Path); err != nil {
		t.Fatal(err)
	}

	// Without the prune in Ensure, git refuses to re-add the branch because a
	// stale administrative entry still claims it.
	again, err := Ensure(p, "feature/z")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(again.Path, "file.txt")); err != nil {
		t.Errorf("worktree was not recreated: %v", err)
	}
}

func TestHeadReportsTheCheckedOutCommit(t *testing.T) {
	repo := newRepo(t)
	head, err := Head(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(head) < 7 {
		t.Errorf("Head = %q, want a short sha", head)
	}
}

func currentBranch(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(trimNewline(out))
}

func trimNewline(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}
