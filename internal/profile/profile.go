// Package profile loads the per-project configuration that makes branchbox
// repo-agnostic. Everything project-specific (which repo, which services back
// it, which commands to run) lives in a profile file outside the target repo,
// so no target repository is ever modified.
package profile

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// PortSpec describes how one environment variable's port is allocated. The
// concrete value is base + slot*stride, where the slot is derived from the
// project and branch (see internal/ports).
type PortSpec struct {
	Base   int `toml:"base"`
	Stride int `toml:"stride"`
}

// Infra points at a Docker Compose file providing the shared backing services
// for every branch container of this profile. Optional: a profile with no
// infra simply runs its container on its own network.
type Infra struct {
	Compose string `toml:"compose"`
}

// Setup lists commands run once inside a freshly started container, before any
// service starts (for example `mise install`).
type Setup struct {
	Steps []string `toml:"steps"`
}

// Profile is one project's configuration.
type Profile struct {
	Name string `toml:"name"`
	// Repo is the main checkout. Branch containers run from git worktrees of
	// it, never from the checkout itself.
	Repo string `toml:"repo"`
	// Worktrees is where per-branch worktrees are created. Defaults to
	// <repo>-worktrees.
	Worktrees string `toml:"worktrees"`
	// Secrets are globs of gitignored local files in the main checkout that
	// each worktree needs. They are symlinked, so there is one place to rotate
	// them.
	Secrets  []string            `toml:"secrets"`
	Infra    Infra               `toml:"infra"`
	Env      map[string]string   `toml:"env"`
	Ports    map[string]PortSpec `toml:"ports"`
	Services map[string]string   `toml:"services"`
	Setup    Setup               `toml:"setup"`

	// Path is the file this profile was loaded from. Relative paths inside the
	// profile (such as infra.compose) resolve against its directory.
	Path string `toml:"-"`
}

// Load reads and validates a profile file.
func Load(path string) (*Profile, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve profile path %q: %w", path, err)
	}
	var p Profile
	if _, err := toml.DecodeFile(abs, &p); err != nil {
		return nil, fmt.Errorf("failed to parse profile %s: %w", abs, err)
	}
	p.Path = abs
	if err := p.normalize(); err != nil {
		return nil, err
	}
	return &p, nil
}

func (p *Profile) normalize() error {
	if strings.TrimSpace(p.Name) == "" {
		// A profile without an explicit name takes it from the file, so a
		// single-project profile can be two lines long.
		p.Name = strings.TrimSuffix(filepath.Base(p.Path), filepath.Ext(p.Path))
	}
	if strings.TrimSpace(p.Repo) == "" {
		return fmt.Errorf("profile %s: repo is required", p.Name)
	}
	repo, err := expand(p.Repo)
	if err != nil {
		return fmt.Errorf("profile %s: %w", p.Name, err)
	}
	p.Repo = repo

	if strings.TrimSpace(p.Worktrees) == "" {
		p.Worktrees = p.Repo + "-worktrees"
	} else {
		wt, err := expand(p.Worktrees)
		if err != nil {
			return fmt.Errorf("profile %s: %w", p.Name, err)
		}
		p.Worktrees = wt
	}

	for name, spec := range p.Ports {
		if spec.Base <= 0 {
			return fmt.Errorf("profile %s: port %s needs a positive base", p.Name, name)
		}
		if spec.Stride <= 0 {
			// One port per slot is the natural default and keeps the common
			// single-port case free of boilerplate.
			spec.Stride = 1
			p.Ports[name] = spec
		}
	}
	return nil
}

// ComposePath resolves infra.compose against the profile's own directory, so
// profiles stay relocatable.
func (p *Profile) ComposePath() string {
	if strings.TrimSpace(p.Infra.Compose) == "" {
		return ""
	}
	if filepath.IsAbs(p.Infra.Compose) {
		return p.Infra.Compose
	}
	return filepath.Join(filepath.Dir(p.Path), p.Infra.Compose)
}

// ServiceNames returns the configured services in a stable order, so help
// output and error messages do not shuffle between runs.
func (p *Profile) ServiceNames() []string {
	names := make([]string, 0, len(p.Services))
	for name := range p.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Network is the Docker network shared by this profile's infra and all of its
// branch containers.
func (p *Profile) Network() string { return "branchbox-" + p.Name }

// expand resolves a leading ~ and makes the path absolute.
func expand(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("failed to resolve home directory: %w", err)
		}
		path = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), "/"))
	}
	return filepath.Abs(path)
}
