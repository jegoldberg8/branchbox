package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jegoldberg8/branchbox/internal/devcontainer"
	"github.com/jegoldberg8/branchbox/internal/docker"
	"github.com/jegoldberg8/branchbox/internal/infra"
	"github.com/jegoldberg8/branchbox/internal/run"
	"github.com/jegoldberg8/branchbox/internal/tmux"
	"github.com/jegoldberg8/branchbox/internal/worktree"
)

func cmdShell(ctx context.Context, args []string) error {
	e, err := newEnv()
	if err != nil {
		return err
	}
	p, rest, err := e.resolveProfile(args)
	if err != nil {
		return err
	}
	ref := ""
	if len(rest) > 0 {
		ref = rest[0]
	} else {
		// Inside a branch checkout, attach to that branch's stack.
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		ref = filepath.Base(cwd)
	}
	st, err := e.store.Resolve(p.Name, ref)
	if err != nil {
		return err
	}
	if err := ensureRunning(ctx, st); err != nil {
		return err
	}
	window := ""
	if len(rest) > 1 {
		window = rest[1]
	}
	if err := tmux.EnsureSession(ctx, st.ContainerName, st.Worktree); err != nil {
		return err
	}
	return tmux.Attach(st.ContainerName, window)
}

func cmdExec(ctx context.Context, args []string) error {
	e, err := newEnv()
	if err != nil {
		return err
	}
	p, rest, err := e.resolveProfile(args)
	if err != nil {
		return err
	}
	if len(rest) < 2 {
		return fmt.Errorf("usage: branchbox exec [project] <branch> <command ...>")
	}
	st, err := e.store.Resolve(p.Name, rest[0])
	if err != nil {
		return err
	}
	if err := ensureRunning(ctx, st); err != nil {
		return err
	}
	// A login shell, so the command sees the same PATH an interactive user
	// would (mise shims, direnv).
	return docker.Attach(st.ContainerName, "bash", "-lc", strings.Join(rest[1:], " "))
}

func cmdPS(ctx context.Context, args []string) error {
	e, err := newEnv()
	if err != nil {
		return err
	}
	project := ""
	if p, _, err := e.resolveProfile(args); err == nil {
		project = p.Name
	}
	stacks, err := e.store.List(project)
	if err != nil {
		return err
	}
	if len(stacks) == 0 {
		fmt.Println("no stacks; start one with `branchbox up <branch> <service>`")
		return nil
	}
	for _, st := range stacks {
		status, err := docker.State(ctx, st.ContainerName)
		if err != nil {
			return err
		}
		if status == "" {
			status = "gone"
		}
		head := "?"
		if h, err := worktree.Head(st.Worktree); err == nil {
			head = h
		}
		var services []string
		if status == "running" {
			// Ask tmux rather than trusting the record: a service that
			// crashed is exactly what you are looking for here.
			if w, err := tmux.Windows(ctx, st.ContainerName); err == nil {
				services = w
			}
		}
		if len(services) == 0 {
			services = []string{"-"}
		}
		// A stack under process-compose shows one tmux window for the
		// supervisor, which says nothing about the services; ask the
		// supervisor instead.
		runner := ""
		if procs := composeProcesses(ctx, st.ContainerName); len(procs) > 0 {
			services = procs
			runner = " (process-compose)"
		}
		var portList []string
		for _, name := range sortedKeys(st.Ports) {
			portList = append(portList, fmt.Sprintf("%s=%d", name, st.Ports[name]))
		}
		fmt.Printf("%-24s %-9s %s (%s)\n", st.Project+"/"+st.Slug, status, st.Branch, head)
		fmt.Printf("    services: %s%s\n", strings.Join(services, ", "), runner)
		if len(portList) > 0 {
			fmt.Printf("    ports:    %s\n", strings.Join(portList, " "))
		}
		fmt.Printf("    worktree: %s\n", st.Worktree)
	}
	return nil
}

func cmdLogs(ctx context.Context, args []string) error {
	args, opt := flags(args, "follow")
	e, err := newEnv()
	if err != nil {
		return err
	}
	p, rest, err := e.resolveProfile(args)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return fmt.Errorf("usage: branchbox logs [project] <branch> [service]")
	}
	st, err := e.store.Resolve(p.Name, rest[0])
	if err != nil {
		return err
	}
	service := ""
	if len(rest) > 1 {
		service = rest[1]
	} else if len(st.Services) == 1 {
		service = st.Services[0]
	} else {
		return fmt.Errorf("stack %s runs %s; name one", st.Slug, strings.Join(st.Services, ", "))
	}
	path := filepath.Join(st.LogDir, service+".log")
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("no log for %s at %s", service, path)
	}
	tailArgs := []string{"-n", "200"}
	if opt["follow"] {
		tailArgs = append(tailArgs, "-f")
	}
	return run.Interactive("tail", append(tailArgs, path)...)
}

func cmdDown(ctx context.Context, args []string) error {
	args, opt := flags(args, "all")
	e, err := newEnv()
	if err != nil {
		return err
	}
	p, rest, err := e.resolveProfile(args)
	if err != nil {
		return err
	}
	var refs []string
	if opt["all"] {
		stacks, err := e.store.List(p.Name)
		if err != nil {
			return err
		}
		for _, st := range stacks {
			refs = append(refs, st.Slug)
		}
	} else {
		if len(rest) == 0 {
			return fmt.Errorf("usage: branchbox down [project] <branch> | --all")
		}
		refs = rest
	}
	for _, ref := range refs {
		st, err := e.store.Resolve(p.Name, ref)
		if err != nil {
			// A stack whose `up` failed partway leaves a container with no
			// state record. Fall back to the name that would have been used,
			// so a half-created stack is still removable.
			slug := worktree.Slug(ref)
			name := devcontainer.ContainerName(p.Name, slug)
			status, serr := docker.State(ctx, name)
			if serr != nil || status == "" {
				return err
			}
			if rerr := docker.Remove(ctx, name); rerr != nil {
				return rerr
			}
			fmt.Printf("branchbox: removed %s (no state record)\n", name)
			continue
		}
		if err := docker.Remove(ctx, st.ContainerName); err != nil {
			return err
		}
		if err := e.store.Remove(st.Project, st.Slug); err != nil {
			return err
		}
		// The worktree and its logs survive: the container is disposable,
		// the branch checkout and its output are not.
		fmt.Printf("branchbox: removed %s (worktree kept at %s)\n", st.ContainerName, st.Worktree)
	}
	return nil
}

func cmdInfra(ctx context.Context, args []string) error {
	args, opt := flags(args, "purge")
	if len(args) == 0 {
		return fmt.Errorf("usage: branchbox infra <up|down|status> [project]")
	}
	action, rest := args[0], args[1:]
	e, err := newEnv()
	if err != nil {
		return err
	}
	p, _, err := e.resolveProfile(rest)
	if err != nil {
		return err
	}
	if err := docker.EnsureDaemon(ctx); err != nil {
		return err
	}
	switch action {
	case "up":
		return infra.Up(ctx, p)
	case "down":
		return infra.Down(ctx, p, opt["purge"])
	case "status", "ps":
		out, err := infra.Status(ctx, p)
		if err != nil {
			return err
		}
		fmt.Print(out)
		return nil
	default:
		return fmt.Errorf("unknown infra action %q", action)
	}
}

func cmdImage(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "build" {
		return fmt.Errorf("usage: branchbox image build")
	}
	e, err := newEnv()
	if err != nil {
		return err
	}
	if err := docker.EnsureDaemon(ctx); err != nil {
		return err
	}
	return buildImage(ctx, e)
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}
