// Package run executes the external tools branchbox drives: docker, the
// devcontainer CLI and git. Keeping them behind one package makes the failure
// messages uniform and the command lines easy to audit.
package run

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Options tune one execution.
type Options struct {
	Dir string
	Env []string
	// Stream sends the command's output to the user's terminal as it runs,
	// which matters for long operations like an image build.
	Stream bool
	Stdin  *os.File
}

// Cmd runs a command and returns its combined output.
func Cmd(ctx context.Context, name string, args []string, o Options) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = o.Dir
	if len(o.Env) > 0 {
		cmd.Env = append(os.Environ(), o.Env...)
	}
	var buf bytes.Buffer
	if o.Stream {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	} else {
		cmd.Stdout = &buf
		cmd.Stderr = &buf
	}
	if o.Stdin != nil {
		cmd.Stdin = o.Stdin
	}
	if err := cmd.Run(); err != nil {
		return buf.String(), fmt.Errorf("%s %s failed: %w%s", name, strings.Join(args, " "), err, detail(buf.String()))
	}
	return buf.String(), nil
}

// Interactive runs a command wired straight to the user's terminal, for
// attaching to a shell or a tmux session.
func Interactive(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s failed: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

// Look reports whether a tool is on PATH, so branchbox can fail with a useful
// message instead of an exec error.
func Look(name string) error {
	if _, err := exec.LookPath(name); err != nil {
		return fmt.Errorf("%s is required but was not found on PATH", name)
	}
	return nil
}

func detail(out string) string {
	out = strings.TrimSpace(out)
	if out == "" {
		return ""
	}
	// Keep the tail: the actionable part of a docker or git failure is at the
	// end, and the head is usually progress noise.
	lines := strings.Split(out, "\n")
	if len(lines) > 12 {
		lines = lines[len(lines)-12:]
	}
	return ":\n" + strings.Join(lines, "\n")
}
