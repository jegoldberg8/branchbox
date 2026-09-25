package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/jegoldberg8/branchbox/internal/docker"
	"github.com/jegoldberg8/branchbox/internal/procompose"
	"github.com/jegoldberg8/branchbox/internal/tmux"
)

// cmdCompose opens an attached process-compose TUI over a stack's services.
//
// The tmux runner is better for working inside one service; this is better for
// watching several, because start, restart, status and per-process logs are on
// one screen. Both read the same profile, so the commands are identical either
// way.
func cmdCompose(ctx context.Context, args []string) error {
	args, opt := flags(args, "stop", "detach")
	e, err := newEnv()
	if err != nil {
		return err
	}
	p, rest, err := e.resolveProfile(args)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return fmt.Errorf("usage: branchbox compose [project] <branch> [service ...]")
	}
	st, err := e.store.Resolve(p.Name, rest[0])
	if err != nil {
		return err
	}
	if err := ensureRunning(ctx, st); err != nil {
		return err
	}

	if opt["stop"] {
		if err := procompose.Stop(ctx, st.ContainerName); err != nil {
			return err
		}
		fmt.Printf("branchbox: stopped the process-compose project in %s\n", st.Slug)
		return nil
	}

	services := rest[1:]
	if len(services) == 0 {
		// Default to whatever the stack was started with, falling back to the
		// profile's full set for a stack started with no services.
		services = st.Services
	}
	cfg, err := procompose.Build(p, st.Worktree, services, st.Ports)
	if err != nil {
		return err
	}

	// tmux and process-compose would otherwise run the same service twice,
	// competing for its port.
	if windows, err := tmux.Windows(ctx, st.ContainerName); err == nil {
		for _, w := range windows {
			if _, managed := cfg.Processes[w]; managed {
				if err := tmux.StopService(ctx, st.ContainerName, w); err != nil {
					return err
				}
				fmt.Printf("branchbox: stopped the tmux window for %s; process-compose owns it now\n", w)
			}
		}
	}

	if opt["detach"] {
		if procompose.Running(ctx, st.ContainerName) {
			fmt.Printf("branchbox: already running; attach with `branchbox compose %s`\n", st.Slug)
			return nil
		}
		if err := procompose.Write(ctx, st.ContainerName, cfg); err != nil {
			return err
		}
		// Started through tmux so it outlives this command, which is what
		// makes a later attach possible.
		if err := tmux.StartRaw(ctx, st.ContainerName, st.Worktree, "process-compose",
			procompose.UpCommand()+" --tui=false"); err != nil {
			return err
		}
		fmt.Printf("branchbox: started %s under process-compose; attach with `branchbox compose %s`\n",
			strings.Join(cfg.Names(), ", "), st.Slug)
		return nil
	}

	fmt.Printf("branchbox: %s (%s)\n", strings.Join(cfg.Names(), ", "), st.Branch)
	return procompose.Attached(ctx, st.ContainerName, cfg)
}

// composeStatuses returns the processes a stack runs under process-compose and
// how each is doing, or nil when it is not using it. `ps` reports these instead
// of the tmux window, which is just the supervisor and says nothing about
// whether the services behind it are alive.
func composeStatuses(ctx context.Context, container string) []procompose.Status {
	if status, err := docker.State(ctx, container); err != nil || status != "running" {
		return nil
	}
	return procompose.Statuses(ctx, container)
}
