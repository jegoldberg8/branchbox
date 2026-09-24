package profile

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SearchDirs returns the directories scanned for profiles, nearest first: the
// user's own profiles win over the ones shipped with the tool, so a checked-in
// example can be overridden without editing the repo.
func SearchDirs(toolDir, stateDir string) []string {
	return []string{filepath.Join(stateDir, "profiles"), filepath.Join(toolDir, "profiles")}
}

// Discover loads every profile found in dirs. Later directories do not
// override earlier ones, matching SearchDirs' nearest-first order.
func Discover(dirs []string) ([]*Profile, error) {
	seen := map[string]bool{}
	var out []*Profile
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("failed to read profile directory %s: %w", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".toml" {
				continue
			}
			p, err := Load(filepath.Join(dir, e.Name()))
			if err != nil {
				return nil, err
			}
			if seen[p.Name] {
				continue
			}
			seen[p.Name] = true
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Resolve picks the profile to use. An explicit name (or a path to a profile
// file) always wins. Otherwise the profile whose repo contains cwd is used, so
// inside a checkout the project argument can be omitted entirely.
func Resolve(name, cwd string, dirs []string) (*Profile, error) {
	if strings.TrimSpace(name) != "" {
		if strings.Contains(name, string(os.PathSeparator)) || strings.HasSuffix(name, ".toml") {
			return Load(name)
		}
		all, err := Discover(dirs)
		if err != nil {
			return nil, err
		}
		for _, p := range all {
			if p.Name == name {
				return p, nil
			}
		}
		return nil, fmt.Errorf("unknown profile %q (looked in %s)", name, strings.Join(dirs, ", "))
	}

	all, err := Discover(dirs)
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve working directory: %w", err)
	}
	// Longest matching repo path wins, so a profile for a nested repo beats
	// one for its parent.
	var best *Profile
	for _, p := range all {
		if !within(abs, p.Repo) && !within(abs, p.Worktrees) {
			continue
		}
		if best == nil || len(p.Repo) > len(best.Repo) {
			best = p
		}
	}
	if best == nil {
		return nil, fmt.Errorf("no profile matches %s; pass a profile name or create one in %s", abs, dirs[0])
	}
	return best, nil
}

// within reports whether path is root or lives underneath it.
func within(path, root string) bool {
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel))
}
