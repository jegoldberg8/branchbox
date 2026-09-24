package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jegoldberg8/branchbox/internal/paths"
	"github.com/jegoldberg8/branchbox/internal/profile"
	"github.com/jegoldberg8/branchbox/internal/state"
)

// env holds the resolved locations branchbox works with. Every command starts
// by building one, so the tool/state split stays in a single place.
type env struct {
	tool  string
	state string
	store *state.Store
	dirs  []string
}

func newEnv() (*env, error) {
	tool, err := paths.ToolDir()
	if err != nil {
		return nil, err
	}
	st, err := paths.StateDir()
	if err != nil {
		return nil, err
	}
	return &env{tool: tool, state: st, store: state.New(st), dirs: profile.SearchDirs(tool, st)}, nil
}

// flags splits leading/trailing `--name` arguments out of a command line, so
// each command can read its options without a flag package ceremony that would
// fight with positional branch and service names.
func flags(args []string, known ...string) ([]string, map[string]bool) {
	set := map[string]bool{}
	allowed := map[string]bool{}
	for _, k := range known {
		allowed[k] = true
	}
	var rest []string
	for _, a := range args {
		if strings.HasPrefix(a, "--") && allowed[strings.TrimPrefix(a, "--")] {
			set[strings.TrimPrefix(a, "--")] = true
			continue
		}
		rest = append(rest, a)
	}
	return rest, set
}

// resolveProfile interprets the optional leading project argument. Positional
// arguments are ambiguous by nature (`up <branch>` and `up <project> <branch>`
// both being valid), so a first argument is treated as a project only when it
// actually names one.
func (e *env) resolveProfile(args []string) (*profile.Profile, []string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to resolve the working directory: %w", err)
	}
	if len(args) > 0 {
		if p, err := profile.Resolve(args[0], cwd, e.dirs); err == nil {
			return p, args[1:], nil
		}
	}
	p, err := profile.Resolve("", cwd, e.dirs)
	if err != nil {
		return nil, nil, err
	}
	return p, args, nil
}

// stackDir is where a stack's generated devcontainer.json lives: outside the
// target repository, which is what keeps that repository untouched.
func (e *env) stackDir(project, slug string) string {
	return filepath.Join(e.state, "stacks", project, slug+".config")
}

func cmdProfiles(_ context.Context, _ []string) error {
	e, err := newEnv()
	if err != nil {
		return err
	}
	all, err := profile.Discover(e.dirs)
	if err != nil {
		return err
	}
	if len(all) == 0 {
		fmt.Printf("no profiles found; add one to %s\n", e.dirs[0])
		return nil
	}
	for _, p := range all {
		services := strings.Join(p.ServiceNames(), ", ")
		if services == "" {
			services = "-"
		}
		fmt.Printf("%-20s %s\n  services: %s\n  profile:  %s\n", p.Name, p.Repo, services, p.Path)
	}
	return nil
}
