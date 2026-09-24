// Package worktree provisions a git worktree per branch, so every branch stack
// has a real checkout of its own without disturbing the main working tree.
package worktree

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jegoldberg8/branchbox/internal/profile"
)

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// Slug converts a branch name into a filesystem- and container-safe
// identifier. Branch names routinely contain slashes and mixed case, neither
// of which is usable as a directory or container name.
func Slug(branch string) string {
	s := nonSlug.ReplaceAllString(strings.ToLower(branch), "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "branch"
	}
	return s
}

// Worktree is a provisioned checkout for one branch.
type Worktree struct {
	Branch string
	Slug   string
	// Path is the checkout the container runs in.
	Path string
	// MainRepo is the profile's main checkout. It must stay available to the
	// container: a worktree's .git is a pointer file into the main repo's
	// .git/worktrees directory, so git inside the container breaks without it.
	MainRepo string
}

// Ensure creates the worktree for branch, or reuses an existing checkout of
// that branch. Local branches are used as-is; otherwise the branch is created
// tracking origin/<branch>.
func Ensure(p *profile.Profile, branch string) (*Worktree, error) {
	if _, err := os.Stat(filepath.Join(p.Repo, ".git")); err != nil {
		return nil, fmt.Errorf("profile %s: %s is not a git checkout: %w", p.Name, p.Repo, err)
	}
	// Prune first: a worktree directory deleted by hand leaves a stale
	// administrative entry that blocks re-adding the same branch.
	if _, err := git(p.Repo, "worktree", "prune"); err != nil {
		return nil, err
	}

	slug := Slug(branch)
	wt := &Worktree{Branch: branch, Slug: slug, MainRepo: p.Repo, Path: filepath.Join(p.Worktrees, slug)}

	// Reuse an existing checkout of this branch wherever it lives, including
	// the main working tree, rather than failing on git's one-checkout-per-
	// branch rule.
	if existing, err := checkoutOf(p.Repo, branch); err != nil {
		return nil, err
	} else if existing != "" {
		wt.Path = existing
		if err := linkSecrets(p, wt); err != nil {
			return nil, err
		}
		return wt, nil
	}

	if err := os.MkdirAll(p.Worktrees, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create worktree directory %s: %w", p.Worktrees, err)
	}
	args := []string{"worktree", "add", wt.Path}
	if hasLocalBranch(p.Repo, branch) {
		args = append(args, branch)
	} else {
		// -B moves an existing local branch onto the remote tip; combined with
		// the hasLocalBranch check above it only ever creates a new branch.
		args = append(args, "-b", branch, "--track", "origin/"+branch)
	}
	if _, err := git(p.Repo, args...); err != nil {
		return nil, err
	}
	if err := linkSecrets(p, wt); err != nil {
		return nil, err
	}
	return wt, nil
}

// linkSecrets symlinks the profile's gitignored local files from the main
// checkout into the worktree. Symlinks rather than copies: one place to rotate
// a credential, and the worktree stays clean because these paths are
// gitignored in the target repo.
func linkSecrets(p *profile.Profile, wt *Worktree) error {
	if wt.Path == p.Repo {
		return nil
	}
	for _, pattern := range p.Secrets {
		matches, err := filepath.Glob(filepath.Join(p.Repo, pattern))
		if err != nil {
			return fmt.Errorf("invalid secrets pattern %q: %w", pattern, err)
		}
		for _, src := range matches {
			rel, err := filepath.Rel(p.Repo, src)
			if err != nil {
				return fmt.Errorf("failed to resolve %s: %w", src, err)
			}
			dst := filepath.Join(wt.Path, rel)
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return fmt.Errorf("failed to create %s: %w", filepath.Dir(dst), err)
			}
			// Replace only a symlink we would have made ourselves; never
			// clobber a real file the developer put there.
			if info, err := os.Lstat(dst); err == nil {
				if info.Mode()&os.ModeSymlink == 0 {
					continue
				}
				if err := os.Remove(dst); err != nil {
					return fmt.Errorf("failed to replace symlink %s: %w", dst, err)
				}
			}
			if err := os.Symlink(src, dst); err != nil {
				return fmt.Errorf("failed to link %s: %w", dst, err)
			}
		}
	}
	return nil
}

// Head returns the short commit of the worktree, used to prove which code a
// container is actually running.
func Head(path string) (string, error) {
	out, err := git(path, "rev-parse", "--short", "HEAD")
	return strings.TrimSpace(out), err
}

// checkoutOf returns the path of an existing worktree holding branch, or "".
func checkoutOf(repo, branch string) (string, error) {
	out, err := git(repo, "worktree", "list", "--porcelain")
	if err != nil {
		return "", err
	}
	var current string
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			current = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch "):
			ref := strings.TrimPrefix(line, "branch ")
			if ref == "refs/heads/"+branch {
				return current, nil
			}
		}
	}
	return "", nil
}

func hasLocalBranch(repo, branch string) bool {
	_, err := git(repo, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s failed: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
