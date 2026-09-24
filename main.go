// Command branchbox runs any git branch of any project in its own dev
// container, with tmux inside and a jcode that joins the host's mesh.
//
// Projects are described by profiles kept outside the target repository, so
// branchbox never writes into the code it runs.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/jegoldberg8/branchbox/internal/jcode"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := root(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "branchbox: "+err.Error())
		os.Exit(1)
	}
}

const usage = `branchbox runs a git branch in its own dev container.

Usage:
  branchbox up [project] <branch> [service ...]   start a stack and its services
  branchbox run <branch> <service ...>            start more services in a running stack
  branchbox shell [branch] [window]               attach to the stack's tmux session
  branchbox exec <branch> <command ...>           run one command inside a stack
  branchbox ps [project]                          list stacks
  branchbox logs <branch> [service]               show a service's log
  branchbox down <branch>                         remove a stack's container
  branchbox infra <up|down|status> [project]      manage a profile's shared services
  branchbox image build                           rebuild the base image
  branchbox profiles                              list known profiles

Inside a project's checkout the project argument can be omitted.

Flags:
  --all       with down: every stack of the project
  --purge     with infra down: delete the data volumes too
  --follow    with logs: stream new output
  --rebuild   with up: rebuild the base image first
`

func root(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Print(usage)
		return nil
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "up":
		return cmdUp(ctx, rest)
	case "run":
		return cmdRun(ctx, rest)
	case "shell":
		return cmdShell(ctx, rest)
	case "exec":
		return cmdExec(ctx, rest)
	case "ps":
		return cmdPS(ctx, rest)
	case "logs":
		return cmdLogs(ctx, rest)
	case "down":
		return cmdDown(ctx, rest)
	case "infra":
		return cmdInfra(ctx, rest)
	case "image":
		return cmdImage(ctx, rest)
	case "profiles":
		return cmdProfiles(ctx, rest)
	case "bridge":
		// Internal: branchbox re-execs itself in this mode to relay the jcode
		// socket into a directory containers can see. Not in the usage text
		// because it is never invoked by hand.
		if len(rest) != 2 {
			return fmt.Errorf("usage: branchbox bridge <source-socket> <listen-socket>")
		}
		return jcode.RunBridge(rest[1], rest[0])
	default:
		return fmt.Errorf("unknown command %q; run `branchbox help`", cmd)
	}
}
