// Package tmux runs a stack's services inside its container, one tmux window
// each. tmux rather than a process supervisor because the point of a branch
// stack is to be worked in: you attach, read the output, restart one service
// after an edit, and leave the rest running.
package tmux

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jegoldberg8/branchbox/internal/devcontainer"
	"github.com/jegoldberg8/branchbox/internal/docker"
)

// Session is the tmux session every stack uses. One per container, so the name
// does not need to vary.
const Session = "branchbox"

// EnsureSession creates the session if it is not already running.
func EnsureSession(ctx context.Context, container, workdir string) error {
	if _, err := docker.Exec(ctx, container, []string{"tmux", "has-session", "-t", Session}); err == nil {
		return nil
	}
	// A placeholder window keeps the session alive when every service window
	// is killed, so restarting one service does not tear down the session.
	_, err := docker.Exec(ctx, container, []string{
		"tmux", "new-session", "-d", "-s", Session, "-n", "shell",
		"-c", workdir, "bash", "-l",
	})
	if err != nil {
		return fmt.Errorf("failed to start the tmux session: %w", err)
	}
	return nil
}

// StartService runs one service in its own window, replacing any previous
// window of the same name so restarting is idempotent. Output is tee'd to the
// mounted log directory, so it is readable from the host without attaching.
func StartService(ctx context.Context, container, workdir, name, command string) error {
	if err := EnsureSession(ctx, container, workdir); err != nil {
		return err
	}
	_, _ = docker.Exec(ctx, container, []string{"tmux", "kill-window", "-t", Session + ":" + name})

	log := devcontainer.LogsDir + "/" + name + ".log"
	// A login shell so mise shims and direnv apply; `exec` inside the pipeline
	// would lose the tee, so the command is piped instead.
	//
	// Each run is delimited in the appended log. Without a marker, a failure
	// from an earlier run is indistinguishable from the current one when
	// reading the tail, which is exactly how a fixed OOM looks unfixed.
	script := fmt.Sprintf(
		"cd %s && printf '\\n=== branchbox: %s started %%s ===\\n' \"$(date -Is)\" >> %s && %s 2>&1 | tee -a %s",
		workdir, name, log, command, log)
	if _, err := docker.Exec(ctx, container, []string{
		"tmux", "new-window", "-d", "-t", Session, "-n", name,
		"-c", workdir, "bash", "-lc", script,
	}); err != nil {
		return fmt.Errorf("failed to start service %s: %w", name, err)
	}
	return nil
}

// StopService kills one service's window, leaving the rest of the stack alone.
func StopService(ctx context.Context, container, name string) error {
	_, err := docker.Exec(ctx, container, []string{"tmux", "kill-window", "-t", Session + ":" + name})
	return err
}

// StartRaw runs an arbitrary command in a named window without the log tee,
// for supervisors that write their own logs.
func StartRaw(ctx context.Context, container, workdir, name, command string) error {
	if err := EnsureSession(ctx, container, workdir); err != nil {
		return err
	}
	_, _ = docker.Exec(ctx, container, []string{"tmux", "kill-window", "-t", Session + ":" + name})
	if _, err := docker.Exec(ctx, container, []string{
		"tmux", "new-window", "-d", "-t", Session, "-n", name,
		"-c", workdir, "bash", "-lc", fmt.Sprintf("cd %s && %s", workdir, command),
	}); err != nil {
		return fmt.Errorf("failed to start %s: %w", name, err)
	}
	return nil
}

// Windows lists the service windows currently running in a container.
func Windows(ctx context.Context, container string) ([]string, error) {
	out, err := docker.Exec(ctx, container, []string{
		"tmux", "list-windows", "-t", Session, "-F", "#{window_name}",
	})
	if err != nil {
		// No session yet simply means nothing is running.
		return nil, nil
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "shell" {
			continue
		}
		names = append(names, line)
	}
	sort.Strings(names)
	return names, nil
}

// Attach opens the stack's tmux session on the user's terminal.
func Attach(container, window string) error {
	target := Session
	if window != "" {
		target += ":" + window
	}
	return docker.Attach(container, "tmux", "attach-session", "-t", target)
}
