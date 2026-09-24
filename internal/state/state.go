// Package state records what branchbox started, so ps, logs, shell and down
// can act on a stack without recomputing how it was created.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Stack is one running (or previously started) branch stack.
type Stack struct {
	Project       string         `json:"project"`
	Branch        string         `json:"branch"`
	Slug          string         `json:"slug"`
	Worktree      string         `json:"worktree"`
	ContainerName string         `json:"container_name"`
	ConfigDir     string         `json:"config_dir"`
	LogDir        string         `json:"log_dir"`
	Image         string         `json:"image"`
	Slot          int            `json:"slot"`
	Ports         map[string]int `json:"ports"`
	Services      []string       `json:"services"`
	StartedAt     time.Time      `json:"started_at"`
}

// Store persists stacks as one JSON file per project/slug.
type Store struct{ dir string }

// New opens the store rooted at stateDir.
func New(stateDir string) *Store { return &Store{dir: filepath.Join(stateDir, "stacks")} }

func (s *Store) path(project, slug string) string {
	return filepath.Join(s.dir, project, slug+".json")
}

// Save writes a stack record, replacing any previous one.
func (s *Store) Save(st *Stack) error {
	path := s.path(st.Project, st.Slug)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("failed to create %s: %w", filepath.Dir(path), err)
	}
	body, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode stack state: %w", err)
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o644); err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	return nil
}

// Load reads one stack record.
func (s *Store) Load(project, slug string) (*Stack, error) {
	body, err := os.ReadFile(s.path(project, slug))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no stack %s/%s; start it with `branchbox up`", project, slug)
		}
		return nil, fmt.Errorf("failed to read stack state: %w", err)
	}
	var st Stack
	if err := json.Unmarshal(body, &st); err != nil {
		return nil, fmt.Errorf("failed to parse stack state: %w", err)
	}
	return &st, nil
}

// List returns every recorded stack, or only one project's when project is
// non-empty.
func (s *Store) List(project string) ([]*Stack, error) {
	var out []*Stack
	projects, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read %s: %w", s.dir, err)
	}
	for _, p := range projects {
		if !p.IsDir() || (project != "" && p.Name() != project) {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(s.dir, p.Name()))
		if err != nil {
			return nil, fmt.Errorf("failed to read %s: %w", p.Name(), err)
		}
		for _, e := range entries {
			if filepath.Ext(e.Name()) != ".json" {
				continue
			}
			st, err := s.Load(p.Name(), e.Name()[:len(e.Name())-len(".json")])
			if err != nil {
				return nil, err
			}
			out = append(out, st)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Project != out[j].Project {
			return out[i].Project < out[j].Project
		}
		return out[i].Slug < out[j].Slug
	})
	return out, nil
}

// Remove deletes a stack record after the container is gone.
func (s *Store) Remove(project, slug string) error {
	if err := os.Remove(s.path(project, slug)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove stack state: %w", err)
	}
	return nil
}

// Resolve finds a stack by slug or branch name within a project, so `down`
// and `logs` accept either spelling.
func (s *Store) Resolve(project, ref string) (*Stack, error) {
	all, err := s.List(project)
	if err != nil {
		return nil, err
	}
	var matches []*Stack
	for _, st := range all {
		if st.Slug == ref || st.Branch == ref {
			matches = append(matches, st)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no stack matches %q", ref)
	case 1:
		return matches[0], nil
	default:
		return nil, fmt.Errorf("%q matches %d stacks; pass a project name", ref, len(matches))
	}
}
